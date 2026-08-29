// Пакет usecase — сценарии модуля «Сроки»: кэш остатков (прогрев при старте),
// чтение снапшота для страниц, запись ручных скидок из UI.
//
// Модуль — единственный владелец product_stock: все записи идут через него
// по цепочке «БД → кэш → публикация события». Приёмка и подбор будут ходить
// сюда же (AcceptStock/PickStock — адаптеры появятся вместе с их модулями,
// см. план ~/.hermes/plans/stock-module.md).
package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"warehouseHelper/internal/stock"
)

// Repository — контракт хранилища остатков, реализуется postgres-репозиторием.
type Repository interface {
	// LoadAllStock возвращает все лоты с данными каталога, отсортированные
	// по (group_name, name, best_before) — клиент рендерит последовательно.
	LoadAllStock(ctx context.Context) ([]stock.Product, error)
	// SetManualDiscount обновляет ручные скидки лота по PK; строки нет — stock.ErrLotNotFound.
	SetManualDiscount(ctx context.Context, productID string, bestBefore time.Time, generalManual, telegramManual *int16) error
}

// Publisher — получатель событий об изменениях остатков (вебсокет-хаб).
type Publisher interface {
	PublishStockChange(e stock.Event)
}

// maxDiscount — верхняя граница скидки в процентах (CHECK в БД дублирует).
const maxDiscount = 100

// StockUseCase — кэш остатков + сценарии записи. Кэш — read-модель:
// единственный писатель — сам usecase (репо → кэш → publish).
type StockUseCase struct {
	repo Repository
	pub  Publisher

	mu     sync.RWMutex
	cache  map[string]*stock.Product // product_id → товар (Lots по возрастанию best_before)
	byCode map[string]string         // internal_code → product_id (шов для будущей приёмки)
}

// NewStockUseCase создаёт сценарий с хранилищем и публикатором (хаб может быть nil).
func NewStockUseCase(repo Repository, pub Publisher) *StockUseCase {
	return &StockUseCase{repo: repo, pub: pub}
}

// WarmUp прогревает кэш всеми лотами при включении программы.
func (uc *StockUseCase) WarmUp(ctx context.Context) error {
	products, err := uc.repo.LoadAllStock(ctx)
	if err != nil {
		return fmt.Errorf("warm up stock cache: %w", err)
	}

	cache := make(map[string]*stock.Product, len(products))
	byCode := make(map[string]string, len(products))
	for i := range products {
		p := &products[i]
		if p.InternalCode != "" {
			byCode[p.InternalCode] = p.ID
		}
		cache[p.ID] = p
	}

	uc.mu.Lock()
	uc.cache = cache
	uc.byCode = byCode
	uc.mu.Unlock()

	return nil
}

// Snapshot возвращает копию всех товаров с лотами, отсортированную по
// (group_name, name) — клиент рендерит группы и строки последовательно.
// Лоты внутри товара — по возрастанию best_before (ближайший срок слева).
func (uc *StockUseCase) Snapshot() []stock.Product {
	uc.mu.RLock()
	out := make([]stock.Product, 0, len(uc.cache))
	for _, p := range uc.cache {
		cp := *p
		cp.Lots = append([]stock.Lot(nil), p.Lots...)
		out = append(out, cp)
	}
	uc.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		g := strings.ToLower(out[i].GroupName)
		h := strings.ToLower(out[j].GroupName)
		if g != h {
			return g < h
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return out
}

// SetManualDiscount записывает ручные скидки лота из UI (попап по количеству).
// generalManual/telegramManual: 0..100; nil — сброс (NULL). Значения копируются
// в кэш, событие публикуется в хаб.
func (uc *StockUseCase) SetManualDiscount(ctx context.Context, productID string, bestBefore time.Time, generalManual, telegramManual *int16) error {
	if err := validateManualDiscount("скидка сайта", generalManual); err != nil {
		return err
	}
	if err := validateManualDiscount("скидка ТГ", telegramManual); err != nil {
		return err
	}

	uc.mu.RLock()
	p, ok := uc.cache[productID]
	if !ok {
		uc.mu.RUnlock()
		return stock.ErrProductNotFound
	}
	lot := findLot(p, bestBefore)
	uc.mu.RUnlock()
	if lot == nil {
		return stock.ErrLotNotFound
	}

	if err := uc.repo.SetManualDiscount(ctx, productID, bestBefore, generalManual, telegramManual); err != nil {
		return err
	}

	uc.mu.Lock()
	p, ok = uc.cache[productID] // перечитываем под записью — кэш мог смениться
	if !ok {
		uc.mu.Unlock()
		return stock.ErrProductNotFound
	}
	lot = findLot(p, bestBefore)
	if lot == nil {
		uc.mu.Unlock()
		return stock.ErrLotNotFound
	}
	lot.GeneralManual = cloneInt16(generalManual)
	lot.TelegramManual = cloneInt16(telegramManual)
	uc.mu.Unlock()

	if uc.pub != nil {
		uc.pub.PublishStockChange(stock.Event{Kind: stock.EventLotUpsert, ProductID: productID, Lot: lot})
	}

	return nil
}

// findLot ищет лот по сроку годности в товаре (без блокировок — вызывает
// только под удерживаемой блокировкой кэша).
func findLot(p *stock.Product, bestBefore time.Time) *stock.Lot {
	for i := range p.Lots {
		if p.Lots[i].BestBefore.Equal(bestBefore) {
			return &p.Lots[i]
		}
	}

	return nil
}

// validateManualDiscount проверяет ручную скидку: nil — сброс, иначе 0..100.
func validateManualDiscount(label string, v *int16) error {
	if v == nil {
		return nil
	}
	if *v < 0 || *v > maxDiscount {
		return fmt.Errorf("%s должна быть в диапазоне 0..100", label)
	}

	return nil
}

// cloneInt16 копирует значение указателя (кэш не держит указатели из запросов).
func cloneInt16(v *int16) *int16 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

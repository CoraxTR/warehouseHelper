// Пакет usecase — сценарии модуля «Сроки»: кэш остатков (прогрев при старте),
// чтение снапшота для страниц, запись ручных скидок из UI, замена остатков
// по сканам («Обновить сроки»).
//
// Модуль — единственный владелец product_stock: все записи идут через него
// по цепочке «БД → кэш → публикация события». Приёмка и подбор будут ходить
// сюда же (AcceptStock/PickStock — адаптеры появятся вместе с их модулями,
// см. план ~/.hermes/plans/stock-module.md).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/stock"
)

// Repository — контракт хранилища остатков, реализуется postgres-репозиторием.
type Repository interface {
	// LoadAllStock возвращает все лоты с данными каталога, отсортированные
	// по (group_name, name, best_before) — клиент рендерит последовательно.
	LoadAllStock(ctx context.Context) ([]stock.Product, error)
	// SetManualDiscount обновляет ручные скидки лота по PK; строки нет — stock.ErrLotNotFound.
	SetManualDiscount(ctx context.Context, productID string, bestBefore time.Time, generalManual, telegramManual *int16) error
	// LoadProductsByCodes возвращает товары каталога по internal_code
	// (включая товары без остатков) — карта code → товар.
	LoadProductsByCodes(ctx context.Context, codes []string) (map[string]stock.Product, error)
	// LoadProductByID возвращает товар каталога по id; строки нет — stock.ErrProductNotFound.
	LoadProductByID(ctx context.Context, productID string) (stock.Product, error)
	// LoadGroupNameByCode возвращает название первой группы с кодом
	// internal_code[1:4]; пустая строка — группы нет.
	LoadGroupNameByCode(ctx context.Context, groupCode string) (string, error)
	// ReplaceStockLots применяет замену остатков товаров в одной транзакции:
	// upsert лотов (qty = целевое, produced_on = COALESCE(существующего, нового),
	// ручные скидки — целевые) и удаление лотов вне сканов.
	ReplaceStockLots(ctx context.Context, writes []stock.ProductWrite) error
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

// ValidLengths возвращает допустимые длины внутренних штрих-кодов — правила
// разбирателя для клиентского фильтра страницы сканирования (без дублирования
// формата в JS; значение приходит от владельца формата).
func (uc *StockUseCase) ValidLengths() []int {
	return innercode.ValidLengths()
}

// PageContext — контекст страницы «Обновить сроки»: ограничение сканов,
// баннер и ожидаемый код.
type PageContext struct {
	ProductID string // ограничение по товару (пусто — нет)
	GroupCode string // ограничение по группе (пусто — нет)
	Name      string // отображаемое имя (баннер)
	Code      string // ожидаемый internal_code (для ограничения по товару)
}

// UpdatePageContext собирает контекст страницы «Обновить сроки» по параметрам
// перехода: product_id — ограничение по товару (ожидаемый internal_code
// резолвится из каталога), group — ограничение по коду группы (3 цифры).
// Без параметров — полное обновление без ограничений.
func (uc *StockUseCase) UpdatePageContext(ctx context.Context, productID, groupCode string) (PageContext, error) {
	pc := PageContext{}
	switch {
	case productID != "":
		p, err := uc.repo.LoadProductByID(ctx, productID)
		if err != nil {
			return PageContext{}, err
		}
		if p.InternalCode == "" {
			return PageContext{}, fmt.Errorf("у товара %q нет внутреннего кода", p.Name)
		}
		pc.ProductID, pc.Name, pc.Code = p.ID, p.Name, p.InternalCode
	case groupCode != "":
		name, err := uc.repo.LoadGroupNameByCode(ctx, groupCode)
		if err != nil {
			return PageContext{}, err
		}
		pc.GroupCode = groupCode
		pc.Name = name
	}
	return pc, nil
}

// ReplaceRequest — замена остатков по сканам «Обновить сроки».
// Scans — сырые штрих-коды; ExpectedProductID/ExpectedGroupCode — ограничения
// страницы (пусто = нет ограничения, полное обновление).
type ReplaceRequest struct {
	Scans             []string
	ExpectedProductID string
	ExpectedGroupCode string
}

// ReplaceStock заменяет остатки сканированных товаров ровно по сканам:
// лоты из сканов апсертятся (qty = сумма сканов по лоту), лоты, которых нет
// в сканах, удаляются. Ручные скидки сохраняются у неистёкших лотов
// (best_before >= сегодня), у истёкших — сбрасываются; «просто»-скидки
// не трогаются. Валидация и запись атомарны: любая ошибка → ничего не меняется.
func (uc *StockUseCase) ReplaceStock(ctx context.Context, req ReplaceRequest) error {
	if len(req.Scans) == 0 {
		return errors.New("нет сканов")
	}

	// 1. Разбор всех сканов разбирателем + проверка ограничения по группе.
	type parsedScan struct {
		code       string
		qty        int64
		producedOn time.Time
		bestBefore time.Time
	}
	parsed := make([]parsedScan, 0, len(req.Scans))
	codes := make([]string, 0, len(req.Scans))
	seen := map[string]struct{}{}
	for _, raw := range req.Scans {
		raw = strings.TrimSpace(raw)
		c, err := innercode.Parse(raw)
		if err != nil {
			if errors.Is(err, innercode.ErrNotInternal) {
				return stock.ErrScanNotInternal
			}
			return stock.ErrScanInvalid
		}
		if req.ExpectedGroupCode != "" && c.InternalCode[1:4] != req.ExpectedGroupCode {
			return stock.ErrScanGroupMismatch
		}
		parsed = append(parsed, parsedScan{
			code:       c.InternalCode,
			qty:        int64(c.Qty),
			producedOn: c.ProdDate,
			bestBefore: normalizeDate(c.ExpDate),
		})
		if _, ok := seen[c.InternalCode]; !ok {
			seen[c.InternalCode] = struct{}{}
			codes = append(codes, c.InternalCode)
		}
	}

	// 2. Резолв каталога одним запросом.
	byCode, err := uc.repo.LoadProductsByCodes(ctx, codes)
	if err != nil {
		return fmt.Errorf("load products by codes: %w", err)
	}
	byID := make(map[string]stock.Product, len(byCode))
	for _, p := range byCode {
		byID[p.ID] = p
	}

	// 3. Группировка сканов по (товар, срок) и проверка ограничения по товару.
	type agg struct {
		qty        int64
		producedOn time.Time
	}
	batches := map[string]map[time.Time]*agg{}
	var productOrder []string
	for _, s := range parsed {
		p, ok := byCode[s.code]
		if !ok {
			return fmt.Errorf("%w: код %s", stock.ErrProductNotFound, s.code)
		}
		if req.ExpectedProductID != "" && p.ID != req.ExpectedProductID {
			return stock.ErrScanProductMismatch
		}
		if _, ok := batches[p.ID]; !ok {
			batches[p.ID] = map[time.Time]*agg{}
			productOrder = append(productOrder, p.ID)
		}
		a := batches[p.ID][s.bestBefore]
		if a == nil {
			a = &agg{}
			batches[p.ID][s.bestBefore] = a
		}
		a.qty += s.qty
		if a.producedOn.IsZero() {
			a.producedOn = s.producedOn
		}
	}

	// 4. План замены по товарам: финальные лоты, целевые скидки, удаления.
	// Даты нормализованы к UTC-полуночи (normalizeDate) — иначе ключи map
	// time.Time с разными зонами (кэш из БД vs разбор innercode) не совпадают,
	// и существующие лоты «теряются» (скидки сбросятся, лоты уйдут в deletes).
	today := normalizeDate(time.Now())
	type plan struct {
		inCache bool
		upserts []stock.Lot      // финальные лоты (кэш + события)
		writes  []stock.LotWrite // целевые значения для репозитория
		deletes []time.Time      // best_before удаляемых лотов
	}
	plans := make([]plan, len(productOrder))

	uc.mu.RLock()
	for i, pid := range productOrder {
		pl := &plans[i]
		cur, ok := uc.cache[pid]
		pl.inCache = ok

		existing := map[time.Time]stock.Lot{}
		if ok {
			for _, l := range cur.Lots {
				existing[normalizeDate(l.BestBefore)] = l
			}
		}
		batch := batches[pid]

		bbs := make([]time.Time, 0, len(batch))
		for bb := range batch {
			bbs = append(bbs, bb)
		}
		sort.Slice(bbs, func(a, b int) bool { return bbs[a].Before(bbs[b]) })

		for _, bb := range bbs {
			a := batch[bb]
			ex, hasEx := existing[bb]
			var genMan, tgMan *int16
			if hasEx && !bb.Before(today) {
				genMan = cloneInt16(ex.GeneralManual)
				tgMan = cloneInt16(ex.TelegramManual)
			}
			produced := a.producedOn
			if hasEx && ex.ProducedOn != nil {
				produced = *ex.ProducedOn
			}
			lot := stock.Lot{
				BestBefore:     bb,
				Qty:            a.qty,
				ProducedOn:     &produced,
				General:        cloneInt16(ex.General),
				Telegram:       cloneInt16(ex.Telegram),
				GeneralManual:  genMan,
				TelegramManual: tgMan,
			}
			pl.upserts = append(pl.upserts, lot)
			pl.writes = append(pl.writes, stock.LotWrite{
				BestBefore:     bb,
				Qty:            a.qty,
				ProducedOn:     a.producedOn,
				GeneralManual:  genMan,
				TelegramManual: tgMan,
			})
		}

		for bb := range existing {
			if _, in := batch[bb]; !in {
				pl.deletes = append(pl.deletes, bb)
			}
		}
		sort.Slice(pl.deletes, func(a, b int) bool { return pl.deletes[a].Before(pl.deletes[b]) })
	}
	uc.mu.RUnlock()

	// 5. Запись в БД (одна транзакция).
	writes := make([]stock.ProductWrite, 0, len(plans))
	for i := range plans {
		writes = append(writes, stock.ProductWrite{
			ProductID: productOrder[i],
			Upserts:   plans[i].writes,
			Deletes:   plans[i].deletes,
		})
	}
	if err := uc.repo.ReplaceStockLots(ctx, writes); err != nil {
		return fmt.Errorf("replace stock lots: %w", err)
	}

	// 6. Кэш: удаления, upsert'ы, сортировка.
	uc.mu.Lock()
	for i, pid := range productOrder {
		pl := &plans[i]
		cur, ok := uc.cache[pid]
		if !ok {
			cat, ok := byID[pid]
			if !ok {
				uc.mu.Unlock()
				return fmt.Errorf("%w: %s", stock.ErrProductNotFound, pid)
			}
			cp := cat
			cur = &cp
			uc.cache[pid] = cur
			if cur.InternalCode != "" {
				uc.byCode[cur.InternalCode] = pid
			}
		}
		lots := cur.Lots[:0]
		for _, l := range cur.Lots {
			if containsTime(pl.deletes, l.BestBefore) {
				continue
			}
			lots = append(lots, l)
		}
		for _, lot := range pl.upserts {
			idx := -1
			for j := range lots {
				if lots[j].BestBefore.Equal(lot.BestBefore) {
					idx = j
					break
				}
			}
			if idx >= 0 {
				lots[idx] = lot
			} else {
				lots = append(lots, lot)
			}
		}
		sort.Slice(lots, func(a, b int) bool { return lots[a].BestBefore.Before(lots[b].BestBefore) })
		cur.Lots = lots
	}
	uc.mu.Unlock()

	// 7. События: сначала удаления, потом upsert'ы (клиент применяет к своему состоянию).
	for i, pid := range productOrder {
		pl := &plans[i]
		for _, bb := range pl.deletes {
			if uc.pub != nil {
				uc.pub.PublishStockChange(stock.Event{Kind: stock.EventLotDelete, ProductID: pid, BestBefore: bb})
			}
		}
		for j := range pl.upserts {
			lot := pl.upserts[j]
			if uc.pub != nil {
				uc.pub.PublishStockChange(stock.Event{Kind: stock.EventLotUpsert, ProductID: pid, Lot: &lot})
			}
		}
	}

	return nil
}

// normalizeDate приводит дату к UTC-полуночи: единое представление DATE
// для ключей map и сравнений, независимо от зоны источника (БД, innercode).
func normalizeDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// containsTime проверяет наличие даты в отсортированном списке.
func containsTime(list []time.Time, t time.Time) bool {
	for _, v := range list {
		if v.Equal(t) {
			return true
		}
	}
	return false
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

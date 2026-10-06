// Сценарий инвентаризации: вид → группа позиций каталога → сборка
// предпросмотра и создание документа в МС. Швы объявлены здесь (потребителем),
// реализуют их каталог (*gucase.GoodsUseCase) и клиент МС (*client.MSAPIClient)
// — модуль не читает чужие таблицы и не ходит в МС напрямую.
package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"warehouseHelper/internal/inventory"
	"warehouseHelper/internal/msclient/client"
)

var (
	// ErrNoType — вид инвентаризации не задан (страница должна была подставить).
	ErrNoType = errors.New("inventory: не задан вид инвентаризации")
	// ErrEmptyGroup — в выбранном виде нет ни одной позиции каталога:
	// документ создавать не из чего.
	ErrEmptyGroup = errors.New("inventory: в виде инвентаризации нет позиций")
)

// Catalog — каталог товаров (реализует *gucase.GoodsUseCase, модуль «Продукция»).
type Catalog interface {
	// InventoryTypes — виды инвентаризации каталога.
	InventoryTypes(ctx context.Context) ([]string, error)
	// InventoryProducts — позиции вида инвентаризации (все товары группы).
	InventoryProducts(ctx context.Context, inventoryType string) ([]inventory.Product, error)
}

// MSClient — создание документа в МойСклад (реализует *client.MSAPIClient).
type MSClient interface {
	// CreateInventory — POST /entity/inventory с позициями.
	CreateInventory(ctx context.Context, positions []client.MSInventoryPosition) (client.MSInventoryDocument, error)
	// InventoryStoreConfigured — задан ли склад МС (страница предупреждает оператора).
	InventoryStoreConfigured() bool
}

// UseCase — сценарий инвентаризации.
type UseCase struct {
	catalog Catalog
	ms      MSClient
}

// New собирает сценарий инвентаризации из каталога и клиента МС.
func New(catalog Catalog, ms MSClient) *UseCase {
	return &UseCase{catalog: catalog, ms: ms}
}

// Types — виды инвентаризации для страницы выбора.
func (uc *UseCase) Types(ctx context.Context) ([]string, error) {
	return uc.catalog.InventoryTypes(ctx)
}

// Group — позиции одного вида инвентаризации (страница сканирования: состав
// группы, имена и коды для локального резолва на клиенте).
func (uc *UseCase) Group(ctx context.Context, inventoryType string) ([]inventory.Product, error) {
	view := strings.TrimSpace(inventoryType)
	if view == "" {
		return nil, ErrNoType
	}
	return uc.catalog.InventoryProducts(ctx, view)
}

// Preview — сборка отчёта по сканам: ВСЕ позиции группы, факт по сканам,
// закупочные цены. Ошибка (невалидный скан / скан не из группы) — как есть от
// inventory.Aggregate, наружу не подменять.
func (uc *UseCase) Preview(
	ctx context.Context,
	inventoryType string,
	scans []string,
) (inventory.Preview, error) {
	view := strings.TrimSpace(inventoryType)
	if view == "" {
		return inventory.Preview{}, ErrNoType
	}
	products, err := uc.catalog.InventoryProducts(ctx, view)
	if err != nil {
		return inventory.Preview{}, err
	}
	return inventory.Aggregate(products, scans)
}

// Conduct — создание документа инвентаризации в МС: тот же Preview, затем
// строки документа (inventory.Positions) в клиент МС.
func (uc *UseCase) Conduct(
	ctx context.Context,
	inventoryType string,
	scans []string,
) (client.MSInventoryDocument, error) {
	preview, err := uc.Preview(ctx, inventoryType, scans)
	if err != nil {
		return client.MSInventoryDocument{}, err
	}
	if preview.Total == 0 {
		return client.MSInventoryDocument{}, ErrEmptyGroup
	}
	return uc.ms.CreateInventory(ctx, msPositions(preview))
}

// StoreConfigured — настроен ли склад МС (пусто — страница сканирования
// предупреждает оператора и не даёт создать документ).
func (uc *UseCase) StoreConfigured() bool {
	return uc.ms.InventoryStoreConfigured()
}

// guestScan — строка гостя совместной инвентаризации: несёт штрих-код и номер
// для курсора. Гости шлют объекты вида {"raw":"<штрихкод>","seq":N}; домен
// инвентаризации не меняется — тот же скан, что и у хоста.
type guestScan struct {
	Raw string `json:"raw"`
}

// MergeGuestScans вынимает штрих-коды из строк гостей и доклеивает их к сканам
// хоста: получается ОДИН набор, который уходит в Conduct одним вызовом (документ
// в МС создаёт только хозяин). Порядок — свои, затем гостевые (в порядке
// отправки чанков). Строку без raw пропускаем (гость прислал пустышку), а
// невалидный JSON — ошибка: непонятную строку не угадываем.
//
// Вынесено в сценарий, а не в слой доставки: склейку и единственный вызов
// Conduct проверяем юнит-тестом (в package internal/delivery/http тесты писать
// нельзя — шаблоны парсятся в init по относительным путям).
func MergeGuestScans(own []string, guests []json.RawMessage) ([]string, error) {
	out := make([]string, 0, len(own)+len(guests))
	out = append(out, own...)

	for _, raw := range guests {
		var g guestScan
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, err
		}

		if strings.TrimSpace(g.Raw) == "" {
			continue
		}

		out = append(out, g.Raw)
	}

	return out, nil
}

// msPositions переводит строки документа домена (inventory.Position) в позиции
// клиента МС. Порядок и нулевые строки сохраняются: Positions отдаёт все строки
// отчёта, включая непросканированные.
func msPositions(preview inventory.Preview) []client.MSInventoryPosition {
	positions := inventory.Positions(preview)
	out := make([]client.MSInventoryPosition, 0, len(positions))
	for _, pos := range positions {
		out = append(out, client.MSInventoryPosition{
			AssortmentID: pos.ProductID,
			Quantity:     pos.Quantity,
			PriceKop:     pos.PriceKop,
		})
	}
	return out
}

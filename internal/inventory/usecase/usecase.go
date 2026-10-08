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

// Sroki — шов модуля «Сроки» (реализует *sucase.StockUseCase): замена остатков
// вида инвентаризации по сканам. Объявлен здесь потребителем — модуль
// инвентаризации не читает чужие таблицы и не ходит в сток напрямую.
type Sroki interface {
	// ReplaceInventoryLots — каждый код вида получает лоты ровно по сканам,
	// код без сканов обнуляется. Валидация и запись атомарны.
	ReplaceInventoryLots(ctx context.Context, codes, scans []string) error
}

// UseCase — сценарий инвентаризации.
type UseCase struct {
	catalog Catalog
	ms      MSClient
	sroki   Sroki
}

// New собирает сценарий инвентаризации из каталога, клиента МС и модуля «Сроки».
func New(catalog Catalog, ms MSClient, sroki Sroki) *UseCase {
	return &UseCase{catalog: catalog, ms: ms, sroki: sroki}
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

// Preview — сборка отчёта по сканам: ВСЕ позиции группы, факт по сканам
// (общая строка scans и «Отложка» hold — раздельно), закупочные цены. Ошибка
// (невалидный скан / скан не из группы) — как есть от inventory.Aggregate,
// наружу не подменять.
func (uc *UseCase) Preview(
	ctx context.Context,
	inventoryType string,
	scans, hold []string,
) (inventory.Preview, error) {
	view := strings.TrimSpace(inventoryType)
	if view == "" {
		return inventory.Preview{}, ErrNoType
	}
	products, err := uc.catalog.InventoryProducts(ctx, view)
	if err != nil {
		return inventory.Preview{}, err
	}
	return inventory.Aggregate(products, scans, hold)
}

// SrokiOutcome — итог шага сроков для страницы (чистить сканы можно только
// после успеха ОБОИХ шагов, так что оператору надо знать, что сроки обновлены).
type SrokiOutcome string

const (
	// SrokiUpdated — остатки «Сроков» заменены по сканам общей строки.
	SrokiUpdated SrokiOutcome = "updated"
	// SrokiSkipped — шаг сроков пропущен: в общей строке нет ни одного скана
	// (страховка от обнуления целого вида), документ МС всё равно создан.
	SrokiSkipped SrokiOutcome = "skipped"
)

// Conduct — создание документа инвентаризации в МС. Порядок шагов: СНАЧАЛА
// сроки (замена остатков вида по сканам общей строки), ПОТОМ документ МС. Сбой
// сроков — документ не создаётся: оператор видит ошибку, сканы не чистятся.
//
// Отложка (hold) в сценарий сроков не уходит: её единицы уже списаны подбором,
// но входят в количество документа (Line.Total).
//
// Страховка от обнуления вида: если в общей строке нет ни одного скана, шаг
// сроков пропускается (SrokiSkipped). «Не нашли» и «не сканировали» по пустому
// списку неразличимы — ждёт подтверждения владельца.
func (uc *UseCase) Conduct(
	ctx context.Context,
	inventoryType string,
	scans, hold []string,
) (client.MSInventoryDocument, SrokiOutcome, error) {
	preview, err := uc.Preview(ctx, inventoryType, scans, hold)
	if err != nil {
		return client.MSInventoryDocument{}, "", err
	}
	if preview.Total == 0 {
		return client.MSInventoryDocument{}, "", ErrEmptyGroup
	}

	if len(scans) == 0 {
		doc, err := uc.ms.CreateInventory(ctx, msPositions(preview))
		return doc, SrokiSkipped, err
	}

	if err := uc.sroki.ReplaceInventoryLots(ctx, scanableCodes(preview), scans); err != nil {
		return client.MSInventoryDocument{}, "", err
	}

	doc, err := uc.ms.CreateInventory(ctx, msPositions(preview))
	if err != nil {
		return client.MSInventoryDocument{}, SrokiUpdated, err
	}

	return doc, SrokiUpdated, nil
}

// scanableCodes — коды склада позиций группы в порядке группы, только непустые:
// товар без кода склада в замену остатков не попадает (в МС его лоты не ведём).
func scanableCodes(preview inventory.Preview) []string {
	codes := make([]string, 0, len(preview.Lines))
	for _, line := range preview.Lines {
		if line.InternalCode != "" {
			codes = append(codes, line.InternalCode)
		}
	}

	return codes
}

// StoreConfigured — настроен ли склад МС (пусто — страница сканирования
// предупреждает оператора и не даёт создать документ).
func (uc *UseCase) StoreConfigured() bool {
	return uc.ms.InventoryStoreConfigured()
}

// guestScan — строка гостя совместной инвентаризации: несёт штрих-код, признак
// отложки и номер для курсора. Гости шлют объекты вида
// {"raw":"<штрихкод>","hold":true,"seq":N}; домен инвентаризации не меняется —
// тот же скан, что и у хоста.
type guestScan struct {
	Raw string `json:"raw"`
	// Hold — скан лёг в «Отложку» (строка стока уже списана подбором).
	Hold bool `json:"hold"`
	// ManualProductID — товар, выбранный гостем вручную (скан без штрих-кода).
	// Общая валидация комнаты такую строку пропускает, а провести по ней
	// инвентаризацию нельзя: сообщаем ошибкой, а не теряем строку молча.
	ManualProductID string `json:"manual_product_id"`
}

// GuestScans — склейка сканов хоста и гостей ОДНИМ набором: общая строка (Scans)
// и «Отложка» (Hold) раздельно, как и на странице хоста.
type GuestScans struct {
	Scans []string
	Hold  []string
}

// MergeGuestScans вынимает штрих-коды из строк гостей и доклеивает их к сканам
// хоста: получается ОДИН набор (Scans и Hold раздельно), который уходит в
// Conduct одним вызовом (документ в МС создаёт только хозяин). Порядок — свои,
// затем гостевые (в порядке отправки чанков); гостевая строка с hold:true идёт в
// Hold, без hold — в Scans. Строку без raw пропускаем (гость прислал пустышку), а
// невалидный JSON — ошибка: непонятную строку не угадываем.
//
// Вынесено в сценарий, а не в слой доставки: склейку и единственный вызов
// Conduct проверяем юнит-тестом (в package internal/delivery/http тесты писать
// нельзя — шаблоны парсятся в init по относительным путям).
func MergeGuestScans(own, ownHold []string, guests []json.RawMessage) (GuestScans, error) {
	out := GuestScans{
		Scans: make([]string, 0, len(own)+len(guests)),
		Hold:  make([]string, 0, len(ownHold)),
	}
	out.Scans = append(out.Scans, own...)
	out.Hold = append(out.Hold, ownHold...)

	for _, raw := range guests {
		var g guestScan
		if err := json.Unmarshal(raw, &g); err != nil {
			return GuestScans{}, err
		}

		raw := strings.TrimSpace(g.Raw)
		if raw == "" {
			// Пустышку пропускаем молча, а вот строку с товаром по internal id —
			// нет: провести её нельзя, и молчание потеряло бы позицию гостя.
			if strings.TrimSpace(g.ManualProductID) != "" {
				return GuestScans{}, errors.New("inventory: строка гостя без штрих-кода (товар выбран вручную)")
			}

			continue
		}

		if g.Hold {
			out.Hold = append(out.Hold, raw)
			continue
		}
		out.Scans = append(out.Scans, raw)
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

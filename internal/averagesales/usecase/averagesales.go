// Пакет usecase — сценарии модуля «Средние продажи».
package usecase

import (
	"context"
	"fmt"
	"path"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/msclient/client"
)

// Repository — хранилище оборотов (реализует PGClient).
type Repository interface {
	// UpsertMonthlyTurnover пишет месячные обороты батчем (ON CONFLICT DO UPDATE).
	UpsertMonthlyTurnover(ctx context.Context, rows []averagesales.TurnoverRow) error
	// UpsertWeeklyTurnover пишет недельные обороты батчем.
	UpsertWeeklyTurnover(ctx context.Context, rows []averagesales.TurnoverRow) error
	// LastMonthlyTurnover — последние n строк месячного оборота товара
	// (по убыванию period_start).
	LastMonthlyTurnover(ctx context.Context, productID string, n int) ([]averagesales.TurnoverRow, error)
	// LastWeeklyTurnover — последние n строк недельного оборота товара.
	LastWeeklyTurnover(ctx context.Context, productID string, n int) ([]averagesales.TurnoverRow, error)
	// ProductsWithoutMonthlyTurnover — id товаров без единой строки месячного оборота
	// (стартовая дозаливка).
	ProductsWithoutMonthlyTurnover(ctx context.Context) ([]string, error)
	// HasMonthlyTurnover — есть ли у товара хоть одна строка месячного оборота.
	HasMonthlyTurnover(ctx context.Context, productID string) (bool, error)
	// HasWeeklyTurnover — есть ли у товара хоть одна строка недельного оборота.
	HasWeeklyTurnover(ctx context.Context, productID string) (bool, error)
}

// SalesClient — отчёт прибыльности МС (реализует *client.MSAPIClient;
// рейт-лимит — воркерпул, напрямую к МС модуль не ходит).
type SalesClient interface {
	FetchProfitTurnover(ctx context.Context, from, to time.Time, interval string, filter client.ProfitFilter) ([]client.ProfitRow, error)
}

// ProductReader — чтение каталога (реализует *gucase.GoodsUseCase).
type ProductReader interface {
	// TurnoverProduct — поля товара, нужные модулю оборотов.
	TurnoverProduct(ctx context.Context, id string) (*averagesales.TurnoverProduct, error)
	// TurnoverProductsByIDs — те же поля для списка товаров.
	TurnoverProductsByIDs(ctx context.Context, ids []string) ([]averagesales.TurnoverProduct, error)
}

// UseCase — сценарии модуля «Средние продажи».
type UseCase struct {
	repo     Repository
	ms       SalesClient
	products ProductReader
	backfill *backfillRunner
}

// NewUseCase создаёт юзкейс: хранилище, клиент отчёта МС, читатель каталога.
func NewUseCase(repo Repository, ms SalesClient, products ProductReader) *UseCase {
	uc := &UseCase{repo: repo, ms: ms, products: products}
	uc.backfill = newBackfillRunner(uc)
	return uc
}

// AverageSales — средние продажи товара за окно (месячные 12 / недельные 5).
// Последовательность: вытащить из БД последние 13/6 интервалов (+ текущий период,
// если его нет в БД — он всё равно рефрешится) → перезапросить по ним продажи из
// МС (возвраты «задним числом») → апсёрт в БД → перечитать → расчёт окна.
// Возвращает среднее по правилу владельца (неполное окно — по имеющемуся, даже
// один текущий незакрытый → avg = current/1); (nil, nil) — продаж не было ВООБЩЕ
// (ни одного интервала: новый товар без единых продаж — см. пометку в domain.go).
// Товара нет в каталоге — ошибка.
func (uc *UseCase) AverageSales(ctx context.Context, productID string) (*float64, error) {
	prod, err := uc.products.TurnoverProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("товар %s: %w", productID, err)
	}

	n, interval, last := 12, "month", uc.repo.LastMonthlyTurnover
	if prod.TrackWeekly {
		n, interval = 5, "week"
		last = uc.repo.LastWeeklyTurnover
	}

	rows, err := last(ctx, productID, n+1)
	if err != nil {
		return nil, fmt.Errorf("читать обороты товара %s: %w", productID, err)
	}

	// Набор интервалов рефреша: даты вытащенных строк + текущий незакрытый период.
	periodStart := currentPeriodStart(interval, time.Now())
	dates := map[time.Time]struct{}{periodStart: {}}
	for _, r := range rows {
		dates[r.PeriodStart] = struct{}{}
	}

	// Перезапрос по интервалам (возвраты «задним числом») и апсёрт.
	var upsert []averagesales.TurnoverRow
	for d := range dates {
		to := periodEnd(d, interval)
		res, err := uc.ms.FetchProfitTurnover(ctx, d, to, interval, client.ProfitFilter{ProductIDs: []string{productID}})
		if err != nil {
			return nil, fmt.Errorf("перезапросить продажи %s за %s: %w", productID, d.Format("2006-01-02"), err)
		}
		for _, r := range res {
			qty, ok := uc.calcQty(r, *prod)
			if !ok {
				continue
			}
			upsert = append(upsert, averagesales.TurnoverRow{ProductID: productID, PeriodStart: d, Qty: qty})
		}
	}
	if len(upsert) > 0 {
		if err := uc.upsertRows(ctx, prod.TrackWeekly, upsert); err != nil {
			return nil, fmt.Errorf("апсёрт оборотов товара %s: %w", productID, err)
		}
	}

	// Перечитать окно и разделить на завершённые/текущий.
	rows, err = last(ctx, productID, n+1)
	if err != nil {
		return nil, fmt.Errorf("перечитать обороты товара %s: %w", productID, err)
	}

	var finished []averagesales.TurnoverRow
	var current *averagesales.TurnoverRow
	for _, r := range rows {
		switch {
		case r.PeriodStart.Before(periodStart):
			if len(finished) < n {
				finished = append(finished, r)
			}
		case r.PeriodStart.Equal(periodStart):
			c := r
			current = &c
		}
	}

	return windowAvg(finished, current, n)
}

// BackfillProducts ставит товары в очередь первичного бэкфилла (при первом
// добавлении в каталог). Возвращается сразу — запросы к МС идут в фоне,
// чтобы не задерживать выгрузку каталога.
func (uc *UseCase) BackfillProducts(ctx context.Context, productIDs []string) {
	uc.backfill.enqueue(productIDs)
}

// BackfillMissing — стартовая дозаливка товаров без месячной истории
// (вызывается из app.go после старта, выполняется в фоне). Товары групп —
// общим запросом productFolder на период, безгрупповые — пачками.
func (uc *UseCase) BackfillMissing() {
	uc.backfill.runMissing()
}

// Stop останавливает фоновые задачи бэкфилла (при завершении приложения).
func (uc *UseCase) Stop() {
	uc.backfill.stop()
}

// upsertRows пишет батч в нужную таблицу по периодичности товара.
func (uc *UseCase) upsertRows(ctx context.Context, weekly bool, rows []averagesales.TurnoverRow) error {
	if weekly {
		return uc.repo.UpsertWeeklyTurnover(ctx, rows)
	}
	return uc.repo.UpsertMonthlyTurnover(ctx, rows)
}

// calcQty переводит продажи из отчёта в штуки: весовые (uom кг/г/т) делятся на
// средний вес штуки (нет веса — интервал пропускаем), штучные — как есть.
func (uc *UseCase) calcQty(r client.ProfitRow, p averagesales.TurnoverProduct) (float64, bool) {
	net := r.SellQuantity - r.ReturnQuantity
	if !isWeightedUOM(p.UOM) {
		return net, true
	}
	if p.AverageWeight == nil || *p.AverageWeight <= 0 {
		return 0, false
	}
	return net / *p.AverageWeight, true
}

// isWeightedUOM — весовой ли товар (единица измерения кг/г/т; как в каталоге).
func isWeightedUOM(uom string) bool {
	switch uom {
	case "кг", "г", "т":
		return true
	}
	return false
}

// productIDFromHref вырезает id товара из href отчёта (последний сегмент пути).
func productIDFromHref(href string) string {
	return path.Base(href)
}

package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/msclient/client"
)

// Пакетные обновления оборотов для потребителей среднего (шов модуля скидок).
//
// Отличие от AverageSales: он работает с ОДНИМ товаром и перед расчётом
// перезапрашивает из МС каждый период окна (13 месяцев / 6 недель). Модулю
// скидок нужен оборот по СОТНЯМ товаров на каждом часовом тике, поэтому здесь
// запросы идут пачками: один запрос отчёта на период на группу товаров или
// пачку из noGroupBatch товаров — цена обновления падает в десятки раз.
//
// Полное окно перезапрашивается редко (09:00 и первый запуск дня: забирает
// возвраты задним числом), текущий незакрытый период — по запросу тика.
// Среднее всегда считается обычным правилом окна (windowAvg) по данным БД:
// завершённые периоды + текущий.

// Размеры окна средних: месячный — год, недельный — 5 недель (см. AGENTS.md
// модуля). Среднее считается по завершённым периодам окна и текущему.
const (
	monthlyWindow = 12
	weeklyWindow  = 5
)

// RefreshWindow обновляет из МС ВСЕ периоды окна перечисленных товаров
// (12 завершённых месяцев + текущий / 5 недель + текущий) и возвращает
// действующий средний оборот за период (шт). Товары без продаж в карту не
// попадают (данных нет).
func (uc *UseCase) RefreshWindow(ctx context.Context, productIDs []string) (map[string]float64, error) {
	done := metrics.Track(trackPkg, "RefreshWindow")
	defer done()

	return uc.refreshTurnover(ctx, productIDs, false)
}

// RefreshCurrent обновляет из МС только текущий незакрытый период перечисленных
// товаров и возвращает действующий средний оборот за период (шт): завершённые
// периоды берутся из БД, свежий — из отчёта. Рабочий вызов часового тика.
func (uc *UseCase) RefreshCurrent(ctx context.Context, productIDs []string) (map[string]float64, error) {
	done := metrics.Track(trackPkg, "RefreshCurrent")
	defer done()

	return uc.refreshTurnover(ctx, productIDs, true)
}

// Averages возвращает действующий средний оборот за период (шт) по данным БД,
// НЕ обращаясь к МС: то, что известно про товары сейчас. Товары без продаж
// в карту не попадают.
func (uc *UseCase) Averages(ctx context.Context, productIDs []string) (map[string]float64, error) {
	done := metrics.Track(trackPkg, "Averages")
	defer done()

	ids := uniqueIDs(productIDs)
	out := make(map[string]float64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	prods, err := uc.turnoverProducts(ctx, ids)
	if err != nil {
		return nil, err
	}

	for _, p := range prods {
		avg, err := uc.cachedAverage(ctx, p)
		if err != nil {
			return nil, err
		}
		if avg != nil {
			out[p.ID] = *avg
		}
	}

	return out, nil
}

// refreshTurnover — общий шаг обновления: товары → параметры окна → запросы
// отчёта пачками (по периодам) → апсёрт → средние из БД.
func (uc *UseCase) refreshTurnover(ctx context.Context, productIDs []string, currentOnly bool) (map[string]float64, error) {
	ids := uniqueIDs(productIDs)
	if len(ids) == 0 {
		return map[string]float64{}, nil
	}

	prods, err := uc.turnoverProducts(ctx, ids)
	if err != nil {
		return nil, err
	}

	active := make(map[string]averagesales.TurnoverProduct, len(prods))
	for _, p := range prods {
		active[p.ID] = p
	}

	for _, weekly := range []bool{false, true} {
		plan := uc.windowPlan(weekly)
		subset := filterTrackWeekly(active, weekly)
		if len(subset) == 0 {
			continue
		}

		periods := []time.Time{currentPeriodStart(plan.interval, uc.now())}
		if !currentOnly {
			periods = append(completedPeriodStarts(plan.interval, plan.n, uc.now()), periods...)
		}

		if err := uc.refreshPeriods(ctx, plan.interval, periods, subset); err != nil {
			return nil, err
		}
	}

	return uc.Averages(ctx, ids)
}

// turnoverProducts — срез каталога для перечисленных товаров (uom, вес,
// недельный учёт, группа); порядок — как отдал каталог.
func (uc *UseCase) turnoverProducts(ctx context.Context, ids []string) ([]averagesales.TurnoverProduct, error) {
	prods, err := uc.products.TurnoverProductsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("товары для оборотов: %w", err)
	}
	return prods, nil
}

// windowPlan — параметры окна интервала: размер окна, имя интервала отчёта и
// чтение последних строк оборота товара из БД.
type windowPlan struct {
	n        int
	interval string
	last     func(ctx context.Context, productID string, n int) ([]averagesales.TurnoverRow, error)
}

// windowPlan — план окна товара: недельный ряд (5 + текущий) или месячный
// (12 + текущий).
func (uc *UseCase) windowPlan(trackWeekly bool) windowPlan {
	if trackWeekly {
		return windowPlan{n: weeklyWindow, interval: intervalWeek, last: uc.repo.LastWeeklyTurnover}
	}
	return windowPlan{n: monthlyWindow, interval: intervalMonth, last: uc.repo.LastMonthlyTurnover}
}

// refreshPeriods — запросы отчёта по каждому периоду: группы товаров одним
// запросом (ProductFolderID), безгрупповые — пачками по noGroupBatch.
func (uc *UseCase) refreshPeriods(ctx context.Context, interval string, periods []time.Time, active map[string]averagesales.TurnoverProduct) error {
	groups, singles := splitByFolder(active)

	for _, period := range periods {
		for folder, memberIDs := range groups {
			filter := client.ProfitFilter{ProductFolderID: folder}
			rows, err := uc.fetchGroupRows(ctx, period, interval, filter, toSet(memberIDs), active, map[string]int{})
			if err != nil {
				return fmt.Errorf("обновить обороты группы %s за %s: %w", folder, period.Format(time.DateOnly), err)
			}
			if err := uc.upsertByInterval(ctx, interval, rows); err != nil {
				return fmt.Errorf("записать обороты группы %s за %s: %w", folder, period.Format(time.DateOnly), err)
			}
		}

		for i := 0; i < len(singles); i += noGroupBatch {
			end := min(i+noGroupBatch, len(singles))
			chunk := singles[i:end]

			filter := client.ProfitFilter{ProductIDs: chunk}
			rows, err := uc.fetchGroupRows(ctx, period, interval, filter, toSet(chunk), active, map[string]int{})
			if err != nil {
				return fmt.Errorf("обновить обороты пачки за %s: %w", period.Format(time.DateOnly), err)
			}
			if err := uc.upsertByInterval(ctx, interval, rows); err != nil {
				return fmt.Errorf("записать обороты пачки за %s: %w", period.Format(time.DateOnly), err)
			}
		}
	}

	return nil
}

// cachedAverage — средний оборот товара по данным БД (без МС). nil — продаж
// не было ни за один интервал (данных нет).
func (uc *UseCase) cachedAverage(ctx context.Context, p averagesales.TurnoverProduct) (*float64, error) {
	plan := uc.windowPlan(p.TrackWeekly)

	rows, err := plan.last(ctx, p.ID, plan.n+1)
	if err != nil {
		return nil, fmt.Errorf("обороты товара %s: %w", p.ID, err)
	}

	finished, current := splitWindow(rows, plan.n, currentPeriodStart(plan.interval, uc.now()))
	avg, err := windowAvg(finished, current, plan.n)
	if errors.Is(err, ErrNoData) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("среднее товара %s: %w", p.ID, err)
	}

	return avg, nil
}

// splitWindow — разделение строк оборота (по убыванию периода) на завершённые
// периоды окна (не больше n) и текущий незакрытый период. Общее правило
// AverageSales и пакетных обновлений: строки за пределами окна пропускаются.
func splitWindow(rows []averagesales.TurnoverRow, n int, periodStart time.Time) (finished []averagesales.TurnoverRow, current *averagesales.TurnoverRow) {
	for _, r := range rows {
		switch {
		case r.PeriodStart.Before(periodStart):
			if len(finished) < n {
				finished = append(finished, r)
			}
		case r.PeriodStart.Equal(periodStart):
			c := r
			current = &c
		default:
			// Строки за пределами окна (будущие/дубли) — пропуск.
		}
	}
	return finished, current
}

// uniqueIDs — id без повторов и пустых значений, порядок первого появления.
func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// filterTrackWeekly — товары с недельным (true) или месячным (false) учётом.
func filterTrackWeekly(active map[string]averagesales.TurnoverProduct, weekly bool) map[string]averagesales.TurnoverProduct {
	out := make(map[string]averagesales.TurnoverProduct, len(active))
	for id, p := range active {
		if p.TrackWeekly == weekly {
			out[id] = p
		}
	}
	return out
}

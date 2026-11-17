package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/metrics"
)

// Calendar отдаёт по товарам ячейки окна (шт и кг) вместе с действующим
// средним, помечая периоды, которые участвуют в расчёте.
//
// active — периоды, в которых товар был доступен (хоть один день in_stock или
// orderable): такая ячейка подходит для расчёта, даже если продаж в ней не
// было, — это 0, а не прочерк. Периоды, которых нет ни в продажах, ни в
// active, в календарь не попадают вовсе (прочерк, ячейка не участвует).
// active == nil — товар считается доступным там, где есть продажи (страница
// без подключённого модуля доступности работает по-старому).
//
// Данные берутся из БД: свежесть окна обеспечивает часовой тик
// (RefreshCurrent), страница в МС не ходит — иначе открытие календаря
// вытягивало бы десятки запросов отчёта прибыльности.
//
// Товары без единых активных ячеек в карту не попадают (ErrNoData — пометка
// модуля).
func (uc *UseCase) Calendar(ctx context.Context, productIDs []string, active map[string][]time.Time) (map[string]averagesales.SalesCalendar, error) {
	done := metrics.Track(trackPkg, "Calendar")
	defer done()

	ids := uniqueIDs(productIDs)
	out := make(map[string]averagesales.SalesCalendar, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	prods, err := uc.turnoverProducts(ctx, ids)
	if err != nil {
		return nil, err
	}

	now := uc.now()
	for _, plan := range uc.windowPlans() {
		subset := plan.trackIDs(prods)
		if len(subset) == 0 {
			continue
		}

		since := windowSince(plan.interval, plan.n, now)
		// Активные ячейки могут увести окно в прошлое дальше обычного окна:
		// недоступные периоды в окно не входят и не тратят его ячеек.
		if earliest := earliestActive(active, subset); !earliest.IsZero() && earliest.Before(since) {
			since = earliest
		}

		rows, err := plan.window(ctx, subset, since)
		if err != nil {
			return nil, fmt.Errorf("обороты окна %d товаров: %w", len(subset), err)
		}

		byProduct := rowsByProduct(rows)
		for _, id := range subset {
			cal, err := calendarForProduct(byProduct[id], active[id], plan, now)
			if errors.Is(err, ErrNoData) {
				continue // продаж и доступности не было ни за один период
			}
			if err != nil {
				return nil, fmt.Errorf("календарь товара %s: %w", id, err)
			}
			out[id] = *cal
		}
	}

	return out, nil
}

// earliestActive — самая дальняя активная ячейка выборки: нижняя граница
// чтения оборотов. Пустое время — активных периодов нет.
func earliestActive(active map[string][]time.Time, ids []string) time.Time {
	var out time.Time
	for _, id := range ids {
		for _, p := range active[id] {
			if out.IsZero() || p.Before(out) {
				out = p
			}
		}
	}

	return out
}

// calendarForProduct — окно одного товара: среднее и периоды с пометкой
// участия в расчёте. Порядок периодов — по убыванию (свежие первыми).
func calendarForProduct(rows []averagesales.TurnoverRow, active []time.Time, plan windowPlan, now time.Time) (*averagesales.SalesCalendar, error) {
	currentStart := currentPeriodStart(plan.interval, now)

	cells := cellsByPeriod(rows, active, currentStart)

	window, err := activeWindow(cells, currentStart, plan.n)
	if err != nil {
		return nil, err
	}

	used := make(map[time.Time]struct{}, len(window))
	sum := 0.0
	for _, r := range window {
		used[r.PeriodStart] = struct{}{}
		sum += r.Qty
	}

	periods := make([]averagesales.PeriodSales, 0, len(cells))
	for _, r := range cells {
		_, inWindow := used[r.PeriodStart]
		periods = append(periods, averagesales.PeriodSales{
			PeriodStart: r.PeriodStart,
			Qty:         r.Qty,
			Kg:          r.Kg,
			InWindow:    inWindow,
		})
	}

	return &averagesales.SalesCalendar{
		Avg:     sum / float64(len(window)),
		Periods: periods,
	}, nil
}

// cellsByPeriod — ячейки товара по убыванию периода: продажи периода плюс
// активные периоды без продаж (Qty = 0). Периоды позже текущего незакрытого не
// берутся: их ещё нет.
func cellsByPeriod(rows []averagesales.TurnoverRow, active []time.Time, currentStart time.Time) []averagesales.TurnoverRow {
	byPeriod := make(map[time.Time]averagesales.TurnoverRow, len(rows)+len(active))
	for _, r := range rows {
		if r.PeriodStart.After(currentStart) {
			continue
		}
		byPeriod[r.PeriodStart] = r
	}
	for _, p := range active {
		if p.After(currentStart) {
			continue
		}
		if _, ok := byPeriod[p]; ok {
			continue
		}
		byPeriod[p] = averagesales.TurnoverRow{PeriodStart: p}
	}

	out := make([]averagesales.TurnoverRow, 0, len(byPeriod))
	for _, r := range byPeriod {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b averagesales.TurnoverRow) int { return b.PeriodStart.Compare(a.PeriodStart) })

	return out
}

// activeWindow — окно среднего по активным ячейкам (решение владельца
// 17.11.2026): n последних ячеек (5 недель / 12 месяцев), делитель —
// фактическое число ячеек окна (неполное окно — по имеющемуся).
//
// Текущий незакрытый период участвует по прежнему правилу: если он больше
// самого дальнего периода окна — заменяет его.
//
// cells — по убыванию периода. ErrNoData — ячеек нет вовсе.
func activeWindow(cells []averagesales.TurnoverRow, currentStart time.Time, n int) ([]averagesales.TurnoverRow, error) {
	if len(cells) == 0 {
		return nil, ErrNoData
	}

	var current *averagesales.TurnoverRow
	finished := make([]averagesales.TurnoverRow, 0, len(cells))
	for _, r := range cells {
		if r.PeriodStart.Equal(currentStart) {
			c := r
			current = &c
			continue
		}
		finished = append(finished, r)
	}

	includeCurrent := current != nil && (len(finished) < n || current.Qty > finished[min(len(finished), n)-1].Qty)

	window := make([]averagesales.TurnoverRow, 0, n)
	if includeCurrent {
		window = append(window, finished[:min(len(finished), n-1)]...)
		window = append(window, *current)
	} else {
		window = append(window, finished[:min(len(finished), n)]...)
	}
	if len(window) == 0 {
		return nil, ErrNoData
	}

	return window, nil
}

// MonthlyTurnover — продажи товаров по месяцам за периоды не раньше since
// (календарь: произвольный исторический диапазон, листание по месяцам).
// Среднее и окно здесь не считаются — это данные для отрисовки.
func (uc *UseCase) MonthlyTurnover(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error) {
	done := metrics.Track(trackPkg, "MonthlyTurnover")
	defer done()

	ids := uniqueIDs(productIDs)
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := uc.repo.MonthlyTurnoverWindowByProducts(ctx, ids, since)
	if err != nil {
		return nil, fmt.Errorf("месячные обороты %d товаров: %w", len(ids), err)
	}

	return rows, nil
}

// WeeklyTurnover — то же для недельного ряда: берутся только товары с
// недельным учётом (у остальных недельной истории не бывает).
func (uc *UseCase) WeeklyTurnover(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error) {
	done := metrics.Track(trackPkg, "WeeklyTurnover")
	defer done()

	ids := uniqueIDs(productIDs)
	if len(ids) == 0 {
		return nil, nil
	}

	prods, err := uc.turnoverProducts(ctx, ids)
	if err != nil {
		return nil, err
	}

	weekly := make([]string, 0, len(prods))
	for _, p := range prods {
		if p.TrackWeekly {
			weekly = append(weekly, p.ID)
		}
	}
	if len(weekly) == 0 {
		return nil, nil
	}

	rows, err := uc.repo.WeeklyTurnoverWindowByProducts(ctx, weekly, since)
	if err != nil {
		return nil, fmt.Errorf("недельные обороты %d товаров: %w", len(weekly), err)
	}

	return rows, nil
}

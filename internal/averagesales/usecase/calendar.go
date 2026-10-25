package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/metrics"
)

// Calendar отдаёт по товарам продажи окна (шт и кг) вместе с действующим
// средним, помечая периоды, которые участвуют в расчёте среднего.
//
// Данные берутся из БД: свежесть окна обеспечивает часовой тик
// (RefreshCurrent), страница в МС не ходит — иначе открытие календаря
// вытягивало бы десятки запросов отчёта прибыльности.
//
// Товары без единых продаж в карту не попадают (ErrNoData — пометка модуля).
func (uc *UseCase) Calendar(ctx context.Context, productIDs []string) (map[string]averagesales.SalesCalendar, error) {
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

		rows, err := plan.window(ctx, subset, windowSince(plan.interval, plan.n, now))
		if err != nil {
			return nil, fmt.Errorf("обороты окна %d товаров: %w", len(subset), err)
		}

		byProduct := rowsByProduct(rows)
		for _, id := range subset {
			cal, err := calendarForProduct(byProduct[id], plan, now)
			if errors.Is(err, ErrNoData) {
				continue // продаж не было ни за один период
			}
			if err != nil {
				return nil, fmt.Errorf("календарь товара %s: %w", id, err)
			}
			out[id] = *cal
		}
	}

	return out, nil
}

// calendarForProduct — окно одного товара: среднее и периоды с пометкой
// участия в расчёте. Порядок периодов — как из репозитория (свежие первыми).
func calendarForProduct(rows []averagesales.TurnoverRow, plan windowPlan, now time.Time) (*averagesales.SalesCalendar, error) {
	finished, current := splitWindow(rows, plan.n, currentPeriodStart(plan.interval, now))

	window, err := averageWindow(finished, current, plan.n)
	if err != nil {
		return nil, err
	}

	used := make(map[time.Time]struct{}, len(window))
	sum := 0.0
	for _, r := range window {
		used[r.PeriodStart] = struct{}{}
		sum += r.Qty
	}

	periods := make([]averagesales.PeriodSales, 0, len(rows))
	for _, r := range rows {
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

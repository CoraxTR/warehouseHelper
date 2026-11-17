package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/metrics"
)

// Имена гранулярности столбцов календаря продаж (как в goods/usecase.Tab*).
const (
	intervalWeekName  = "week"
	intervalMonthName = "month"
)

// AvailablePeriods — периоды доступности товаров для календаря продаж: товар
// был в наличии (in_stock) или доступен для заказа (orderable) хотя бы один
// день периода. Такой период — подходящая ячейка расчёта: продаж в нём могло
// не быть, и это ноль, а не прочерк.
//
// Считается на рендере по строкам дней (кэша нет): бэктречная недоступность —
// правка календаря «Доступность» задним числом — видна сразу, вместе с
// каскадом коэффициента.
//
// interval — гранулярность столбцов календаря ("week" с понедельника, иначе
// месяц). Пустой диапазон или границы наоборот — пустая карта.
func (uc *UseCase) AvailablePeriods(ctx context.Context, interval string, from, to time.Time) (map[string][]time.Time, error) {
	done := metrics.Track(trackPkg, "AvailablePeriods")
	defer done()

	out := map[string][]time.Time{}
	if !from.Before(to) {
		return out, nil
	}
	if interval != intervalWeekName {
		interval = intervalMonthName
	}

	return uc.repo.AvailablePeriods(ctx, interval, from, to)
}

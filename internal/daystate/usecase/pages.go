package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/daystate"
	"warehouseHelper/internal/metrics"
)

// AvailabilityProduct — строка календаря «Доступность товаров».
type AvailabilityProduct struct {
	ProductID string
	Name      string
	GroupName string
	// Orderable — доступность по дням месяца (индекс = день − 1):
	// строки дня нет — доступна по умолчанию (true).
	Orderable []bool
}

// AvailabilityPage — данные календаря «Доступность товаров» за месяц.
type AvailabilityPage struct {
	Month    time.Time // первый день месяца
	Days     int
	Products []AvailabilityProduct
}

// Availability собирает календарь доступности: все товары каталога
// (по группам) × дни месяца; доступность — из строк product_day_state
// (orderable), будущие даты и отсутствующие строки — доступны по умолчанию.
func (uc *UseCase) Availability(ctx context.Context, month time.Time) (*AvailabilityPage, error) {
	done := metrics.Track(trackPkg, "Availability")
	defer done()

	products, err := uc.catalog.CatalogProducts(ctx)
	if err != nil {
		return nil, err
	}
	from, to := monthRange(month)
	rows, err := uc.repo.ListByRange(ctx, from, to)
	if err != nil {
		return nil, err
	}

	page := &AvailabilityPage{Month: firstOfMonth(month), Days: daysInMonth(month)}
	page.Products = make([]AvailabilityProduct, 0, len(products))
	for _, p := range products {
		ap := AvailabilityProduct{
			ProductID: p.ID,
			Name:      p.Name,
			GroupName: p.GroupName,
			Orderable: make([]bool, page.Days),
		}
		for i := range ap.Orderable {
			ap.Orderable[i] = true
		}
		for date, d := range rows[p.ID] {
			if idx := date.Day() - 1; idx >= 0 && idx < page.Days {
				ap.Orderable[idx] = d.Orderable
			}
		}
		page.Products = append(page.Products, ap)
	}
	return page, nil
}

// ReportProduct — строка «Отчёта по наличию».
type ReportProduct struct {
	ProductID string
	Name      string
	GroupName string
	Cells     []daystate.ReportCell // по дням месяца
}

// StockReportPage — данные «Отчёта по наличию» за месяц.
type StockReportPage struct {
	Month    time.Time
	Days     int
	Today    time.Time
	Products []ReportProduct
}

// StockReport собирает матрицу отчёта: товары каталога (по группам) × дни
// месяца; ячейки — по правилам владельца (daystate.CellFor), даты в будущем
// и отсутствующие строки — пустые.
func (uc *UseCase) StockReport(ctx context.Context, month time.Time) (*StockReportPage, error) {
	done := metrics.Track(trackPkg, "StockReport")
	defer done()

	products, err := uc.catalog.CatalogProducts(ctx)
	if err != nil {
		return nil, err
	}
	from, to := monthRange(month)
	rows, err := uc.repo.ListByRange(ctx, from, to)
	if err != nil {
		return nil, err
	}

	today := normalizeDate(uc.now())
	page := &StockReportPage{Month: firstOfMonth(month), Days: daysInMonth(month), Today: today}
	page.Products = make([]ReportProduct, 0, len(products))
	for _, p := range products {
		rp := ReportProduct{ProductID: p.ID, Name: p.Name, GroupName: p.GroupName, Cells: make([]daystate.ReportCell, page.Days)}
		byDate := rows[p.ID]
		for i := 0; i < page.Days; i++ {
			date := page.Month.AddDate(0, 0, i)
			d, ok := byDate[date]
			if !ok {
				rp.Cells[i] = daystate.CellFor(nil, date, today)
				continue
			}
			rp.Cells[i] = daystate.CellFor(&d, date, today)
		}
		page.Products = append(page.Products, rp)
	}
	return page, nil
}

// firstOfMonth — первый день месяца (UTC-полночь).
func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// daysInMonth — число дней в месяце.
func daysInMonth(t time.Time) int {
	next := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	return next.AddDate(0, 0, -1).Day()
}

// monthRange — [первый день месяца, последний день месяца].
func monthRange(t time.Time) (from, to time.Time) {
	first := firstOfMonth(t)
	return first, first.AddDate(0, 0, daysInMonth(t)-1)
}

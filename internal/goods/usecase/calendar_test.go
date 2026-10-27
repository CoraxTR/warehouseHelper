package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/ordercoeff"
)

// stubSales — данные продаж для календаря (обороты — averagesales).
type stubSales struct {
	cal     map[string]averagesales.SalesCalendar
	monthly []averagesales.TurnoverRow
	weekly  []averagesales.TurnoverRow
	err     error

	monthlySince time.Time
}

func (s *stubSales) Calendar(context.Context, []string) (map[string]averagesales.SalesCalendar, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.cal, nil
}

func (s *stubSales) MonthlyTurnover(_ context.Context, _ []string, since time.Time) ([]averagesales.TurnoverRow, error) {
	s.monthlySince = since
	if s.err != nil {
		return nil, s.err
	}

	return s.monthly, nil
}

func (s *stubSales) WeeklyTurnover(context.Context, []string, time.Time) ([]averagesales.TurnoverRow, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.weekly, nil
}

// stubCoeffs — коэффициенты периодов (ordercoeff).
type stubCoeffs struct {
	values map[string]map[time.Time]ordercoeff.PeriodCoeff
	err    error
}

func (s *stubCoeffs) PeriodsBatch(context.Context, []string, ordercoeff.PeriodType, []time.Time) (map[string]map[time.Time]ordercoeff.PeriodCoeff, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.values, nil
}

// calendarNow — среда 16.09.2026: текущая неделя с 14.09, текущий месяц 09.2026.
var calendarNow = time.Date(2026, time.September, 16, 10, 0, 0, 0, time.UTC)

func newCalendarUC(repo *stubProductsRepo, sales *stubSales, coeffs *stubCoeffs) *GoodsUseCase {
	uc := NewGoodsUseCase(nil, nil, repo, nil)
	uc.SetCalendarStore(sales, coeffs)
	uc.now = func() time.Time { return calendarNow }

	return uc
}

func calendarProducts() *stubProductsRepo {
	return &stubProductsRepo{search: []domain.Product{
		{ID: "p1", Name: "Стейк", InternalCode: "10010001", GroupName: "Мясо 001", TrackWeekly: true, UOM: "кг"},
		{ID: "p2", Name: "Соус", InternalCode: "10020001", GroupName: "Бакалея", TrackWeekly: false, UOM: "шт"},
	}}
}

func TestCalendarPage_Week(t *testing.T) {
	week := currentWeekStart(calendarNow)
	kg := 12.345

	sales := &stubSales{cal: map[string]averagesales.SalesCalendar{
		"p1": {
			Avg: 7.2,
			Periods: []averagesales.PeriodSales{
				{PeriodStart: week, Qty: 8.5, Kg: &kg, InWindow: true},
				{PeriodStart: week.AddDate(0, 0, -7), Qty: 6, InWindow: true},
			},
		},
	}}
	coeffs := &stubCoeffs{values: map[string]map[time.Time]ordercoeff.PeriodCoeff{
		"p1": {week: {ProductID: "p1", PeriodStart: week, Coeff: -2, Discount: 2}},
	}}

	uc := newCalendarUC(calendarProducts(), sales, coeffs)

	page, err := uc.CalendarPage(context.Background(), TabWeek, "", "", "")
	if err != nil {
		t.Fatalf("CalendarPage: %v", err)
	}

	if len(page.PeriodTitles) != calendarWeeks {
		t.Fatalf("столбцов = %d, want %d", len(page.PeriodTitles), calendarWeeks)
	}
	if page.PeriodTitles[len(page.PeriodTitles)-1] != "14.09" {
		t.Fatalf("последний столбец = %q, want 14.09 (текущая неделя)", page.PeriodTitles[len(page.PeriodTitles)-1])
	}

	rows := 0
	for _, g := range page.Groups {
		rows += len(g.Rows)
		if g.Name == "Бакалея" {
			t.Fatal("на недельной вкладке не должно быть товаров без недельного учёта")
		}
	}
	if rows != 1 {
		t.Fatalf("строк = %d, want 1", rows)
	}

	row := page.Groups[0].Rows[0]
	last := row.Cells[len(row.Cells)-1]
	if last.KgText != "12,345" {
		t.Fatalf("кг в ячейке = %q, want 12,345", last.KgText)
	}
	if last.QtyText != "8,5" {
		t.Fatalf("штуки в ячейке = %q, want 8,5", last.QtyText)
	}
	if !last.InWindow {
		t.Fatal("текущая неделя должна быть помечена как участвующая в среднем")
	}
	if last.Coeff != "-2" {
		t.Fatalf("коэффициент ячейки = %q, want -2", last.Coeff)
	}
	if !strings.Contains(last.Title, "коэф. -2") || !strings.Contains(last.Title, "12,345 кг") {
		t.Fatalf("подсказка = %q, want период/кг/коэф", last.Title)
	}
	if row.AvgText != "7,2" {
		t.Fatalf("среднее = %q, want 7,2", row.AvgText)
	}
	if row.CoeffText != "-2" {
		t.Fatalf("коэф строки = %q, want -2 (текущий период)", row.CoeffText)
	}
}

func TestCalendarPage_MonthCurrent(t *testing.T) {
	month := currentMonthStart(calendarNow)
	sales := &stubSales{}
	sales.cal = map[string]averagesales.SalesCalendar{
		"p2": {
			Avg: 30,
			Periods: []averagesales.PeriodSales{
				{PeriodStart: month, Qty: 30, InWindow: true},
				{PeriodStart: month.AddDate(0, -1, 0), Qty: 20, InWindow: true},
			},
		},
	}
	uc := newCalendarUC(calendarProducts(), sales, &stubCoeffs{})

	page, err := uc.CalendarPage(context.Background(), TabMonth, "", "", "")
	if err != nil {
		t.Fatalf("CalendarPage: %v", err)
	}

	if len(page.PeriodTitles) != calendarMonths {
		t.Fatalf("столбцов = %d, want %d", len(page.PeriodTitles), calendarMonths)
	}
	if page.MonthParam != "2026-09" {
		t.Fatalf("месяц = %q, want 2026-09", page.MonthParam)
	}
	if !page.NextDisabled {
		t.Fatal("из текущего месяца листать вперёд нельзя")
	}

	// Все товары (и штучные тоже) — 2 строки в 2 группах.
	rows := 0
	for _, g := range page.Groups {
		rows += len(g.Rows)
	}
	if rows != 2 {
		t.Fatalf("строк = %d, want 2 (на месячной вкладке все товары)", rows)
	}

	var sauce *CalendarRow
	for i := range page.Groups {
		for j := range page.Groups[i].Rows {
			if page.Groups[i].Rows[j].ProductID == "p2" {
				sauce = &page.Groups[i].Rows[j]
			}
		}
	}
	if sauce == nil {
		t.Fatal("штучный товар p2 не попал в календарь")
	}
	if got := sauce.Cells[len(sauce.Cells)-1]; got.KgText != "" {
		t.Fatalf("у штучного товара кг быть не должно, получил %q", got.KgText)
	}
	if !sauce.Cells[len(sauce.Cells)-1].InWindow {
		t.Fatal("текущий месяц должен быть в окне среднего")
	}
	if sauce.AvgText != "30" {
		t.Fatalf("среднее = %q, want 30", sauce.AvgText)
	}
}

func TestCalendarPage_MonthHistory(t *testing.T) {
	month := currentMonthStart(calendarNow).AddDate(0, -2, 0) // 2026-07
	sales := &stubSales{monthly: []averagesales.TurnoverRow{
		{ProductID: "p1", PeriodStart: month, Qty: 5},
	}}
	uc := newCalendarUC(calendarProducts(), sales, &stubCoeffs{})

	page, err := uc.CalendarPage(context.Background(), TabMonth, "2026-07", "", "")
	if err != nil {
		t.Fatalf("CalendarPage: %v", err)
	}

	if page.MonthParam != "2026-07" {
		t.Fatalf("месяц = %q, want 2026-07", page.MonthParam)
	}
	if page.NextDisabled {
		t.Fatal("из исторического месяца листать вперёд можно")
	}
	first := month.AddDate(0, -(calendarMonths - 1), 0)
	if !sales.monthlySince.Equal(first) {
		t.Fatalf("since = %s, want %s (первый столбец окна, заканчивающегося выбранным месяцем)", sales.monthlySince, first)
	}

	var steak *CalendarRow
	for i := range page.Groups {
		for j := range page.Groups[i].Rows {
			if page.Groups[i].Rows[j].ProductID == "p1" {
				steak = &page.Groups[i].Rows[j]
			}
		}
	}
	if steak == nil {
		t.Fatal("товар p1 не попал в исторический месяц")
	}
	last := steak.Cells[len(steak.Cells)-1]
	if last.InWindow {
		t.Fatal("в историческом диапазоне обводки окна быть не должно")
	}
	if last.QtyText != "5" {
		t.Fatalf("штуки за месяц = %q, want 5", last.QtyText)
	}
	if steak.AvgText != "" {
		t.Fatalf("среднее в историческом диапазоне = %q, want пусто", steak.AvgText)
	}
}

func TestCalendarPage_NotWired(t *testing.T) {
	uc := NewGoodsUseCase(nil, nil, calendarProducts(), nil)

	if _, err := uc.CalendarPage(context.Background(), TabWeek, "", "", ""); err == nil {
		t.Fatal("CalendarPage без данных продаж = nil, want ошибку")
	}
}

func TestCalendarPage_EmptyWeekTab(t *testing.T) {
	repo := &stubProductsRepo{search: []domain.Product{
		{ID: "p2", Name: "Соус", GroupName: "Бакалея", TrackWeekly: false},
	}}
	uc := newCalendarUC(repo, &stubSales{}, &stubCoeffs{})

	page, err := uc.CalendarPage(context.Background(), TabWeek, "", "", "")
	if err != nil {
		t.Fatalf("CalendarPage: %v", err)
	}
	if !page.Empty {
		t.Fatal("недельная вкладка без недельных товаров должна быть пустой")
	}
}

func TestCalendarPage_DataError(t *testing.T) {
	broken := errors.New("нет связи")
	uc := newCalendarUC(calendarProducts(), &stubSales{err: broken}, &stubCoeffs{})

	if _, err := uc.CalendarPage(context.Background(), TabWeek, "", "", ""); err == nil {
		t.Fatal("ошибка данных проглочена")
	}
}

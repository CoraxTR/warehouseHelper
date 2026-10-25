package usecase

import (
	"context"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
)

// Календарь продаж: строки окна + пометка периодов, участвующих в среднем.
// Правило окна — владельца (windowAvg): n завершённых либо n−1 завершённых
// + текущий, если текущий горячее самой дальней завершённой.

func newCalendarUC(repo *stubRepo, prods *stubProducts) *UseCase {
	uc := NewUseCase(repo, &stubSales{}, prods)
	uc.now = func() time.Time { return refreshNow }

	return uc
}

func TestCalendar_MonthlyWindowMarksUsedPeriods(t *testing.T) {
	now := refreshNow
	current := currentPeriodStart(intervalMonth, now)

	rows := make([]averagesales.TurnoverRow, 0, 12)
	for i := 1; i <= 12; i++ {
		start := time.Date(current.Year(), current.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -i, 0)
		rows = append(rows, averagesales.TurnoverRow{ProductID: "p1", PeriodStart: start, Qty: float64(i)})
	}

	kg := 12.5
	repo := &stubRepo{monthly: append([]averagesales.TurnoverRow{{
		ProductID: "p1", PeriodStart: current, Qty: 3, Kg: &kg,
	}}, rows...)}

	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "кг", AverageWeight: floatPtr(2), TrackWeekly: false},
	}}

	got, err := newCalendarUC(repo, prods).Calendar(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}

	cal, ok := got["p1"]
	if !ok {
		t.Fatal("товара нет в календаре")
	}

	// Текущий период: 3 шт против самой дальней завершённой (12 шт) — не
	// горячее, значит окно = 12 завершённых месяцев, текущий не участвует.
	if cal.Avg != 6.5 {
		t.Fatalf("Avg = %v, want 6.5 (среднее 12 завершённых: 1..12)", cal.Avg)
	}

	var used, unused int
	for _, p := range cal.Periods {
		if p.InWindow {
			used++
		} else {
			unused++
		}
	}
	if used != 12 || unused != 1 {
		t.Fatalf("в окне %d, вне %d; want 12 и 1", used, unused)
	}

	if cal.Periods[0].Kg == nil || *cal.Periods[0].Kg != 12.5 {
		t.Fatalf("кг текущего периода = %v, want 12.5", cal.Periods[0].Kg)
	}
	if cal.Periods[0].PeriodStart != current {
		t.Fatalf("первый период = %s, want текущий %s", cal.Periods[0].PeriodStart, current)
	}
}

func TestCalendar_CurrentEntersWindowWhenHot(t *testing.T) {
	now := refreshNow
	current := currentPeriodStart(intervalWeek, now)

	rows := []averagesales.TurnoverRow{{ProductID: "p1", PeriodStart: current, Qty: 100}}
	for i := 1; i <= 5; i++ {
		start := current.AddDate(0, 0, -7*i)
		rows = append(rows, averagesales.TurnoverRow{ProductID: "p1", PeriodStart: start, Qty: float64(i)})
	}

	repo := &stubRepo{weekly: rows}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: true},
	}}

	got, err := newCalendarUC(repo, prods).Calendar(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}

	cal := got["p1"]
	// Текущая неделя (100) горячее самой дальней завершённой (5) → окно
	// = 4 завершённых + текущая: (1+2+3+4+100)/5 = 22.
	if cal.Avg != 22 {
		t.Fatalf("Avg = %v, want 22", cal.Avg)
	}

	var used int
	for _, p := range cal.Periods {
		if p.InWindow {
			used++
		}
	}
	if used != 5 {
		t.Fatalf("в окне %d периодов, want 5", used)
	}
}

func TestCalendar_SkipsProductWithoutSales(t *testing.T) {
	repo := &stubRepo{}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}

	got, err := newCalendarUC(repo, prods).Calendar(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("товар без продаж попал в календарь: %+v", got)
	}
}

func TestCalendar_EmptyIDs(t *testing.T) {
	got, err := newCalendarUC(&stubRepo{}, &stubProducts{}).Calendar(context.Background(), nil)
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("пустой список должен дать пустую карту, got %+v", got)
	}
}

func floatPtr(v float64) *float64 { return &v }

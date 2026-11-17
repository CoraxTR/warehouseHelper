package usecase

import (
	"context"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
)

// Окно среднего собирается из 12 АКТИВНЫХ месяцев, а не из 12 последних: у
// редкого товара ячейки растянуты по годам, и чтение ровно 12 периодов находило
// бы меньше 12 ячеек — среднее считалось бы по неполному окну, а на странице не
// было бы видно всех участвующих ячеек.
func TestCalendarReadsBackForTwelveActiveMonths(t *testing.T) {
	current := currentPeriodStart(intervalMonth, refreshNow)

	// Доступности у товара нет (active == nil): ячейки дают только продажи.
	// Товар продаётся раз в квартал: 12 активных месяцев от текущего до -33.
	rows := make([]averagesales.TurnoverRow, 0, 12)
	for i := range 12 {
		rows = append(rows, averagesales.TurnoverRow{ProductID: "p1", PeriodStart: current.AddDate(0, -3*i, 0), Qty: 10})
	}

	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := newCalendarUC(&stubRepo{monthly: rows}, prods)

	got, err := uc.Calendar(context.Background(), []string{"p1"}, nil)
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}

	cal, ok := got["p1"]
	if !ok {
		t.Fatal("товара нет в календаре: окно не собралось")
	}
	if cal.Avg != 10 {
		t.Errorf("avg = %v, want 10 (12 активных ячеек по 10 шт)", cal.Avg)
	}

	inWindow, oldest := 0, time.Time{}
	for _, ps := range cal.Periods {
		if !ps.InWindow {
			continue
		}
		inWindow++
		if oldest.IsZero() || ps.PeriodStart.Before(oldest) {
			oldest = ps.PeriodStart
		}
	}
	if inWindow != 12 {
		t.Errorf("в окне %d ячеек, want 12", inWindow)
	}
	if want := current.AddDate(0, -33, 0); !oldest.Equal(want) {
		t.Errorf("дальняя ячейка окна = %s, want %s", oldest.Format(time.DateOnly), want.Format(time.DateOnly))
	}
}

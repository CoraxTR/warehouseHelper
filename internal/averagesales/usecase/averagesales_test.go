package usecase

import (
	"context"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/msclient/client"
)

func TestAverageSales_Monthly(t *testing.T) {
	now := time.Now()
	curStart := currentPeriodStart("month", now)

	// 12 завершённых месяцев до текущего: свежайший qty 1 … самый дальний qty 12.
	monthly := make([]averagesales.TurnoverRow, 0, 12)
	for i := 1; i <= 12; i++ {
		monthly = append(monthly, averagesales.TurnoverRow{
			ProductID:   "p1",
			PeriodStart: curStart.AddDate(0, -i, 0),
			Qty:         float64(i),
		})
	}

	sales := &stubSales{rowsFn: func(_, _ time.Time, _ string, _ client.ProfitFilter) []client.ProfitRow {
		return []client.ProfitRow{profitRow("p1", 10, 1)}
	}}
	repo := &stubRepo{monthly: monthly}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: false},
	}}
	uc := NewUseCase(repo, sales, products)

	avg, err := uc.AverageSales(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AverageSales() error: %v", err)
	}
	if avg == nil {
		t.Fatal("AverageSales() = nil, want среднее")
	}
	if *avg != 78.0/12.0 {
		t.Errorf("AverageSales() = %v, want %v", *avg, 78.0/12.0)
	}

	// Рефреш: 13 запросов (12 завершённых + текущий месяц), всегда по товару.
	if sales.calls != 13 {
		t.Errorf("запросов к МС = %d, want 13 (12 завершённых + текущий)", sales.calls)
	}
	for i, f := range sales.filters {
		if len(f.ProductIDs) != 1 || f.ProductIDs[0] != "p1" {
			t.Errorf("запрос %d: filter = %+v, want ProductIDs=[p1]", i, f)
		}
	}

	// Апсёрт: 12 завершённых + текущий месяц (qty 9 = 10 − 1).
	if len(repo.upsM) != 13 {
		t.Fatalf("апсёртнуто строк = %d, want 13", len(repo.upsM))
	}
	hasCurrent := false
	for _, r := range repo.upsM {
		if r.PeriodStart.Equal(curStart) {
			hasCurrent = true
			if r.Qty != 9 {
				t.Errorf("текущий месяц qty = %v, want 9", r.Qty)
			}
		}
	}
	if !hasCurrent {
		t.Errorf("текущий месяц %v не дозапрошен (нет в апсёрте)", curStart.Format(time.DateOnly))
	}
}

func TestAverageSales_Weekly(t *testing.T) {
	now := time.Now()
	curWeek := currentPeriodStart("week", now)

	// 5 завершённых недель: свежайшая qty 5 … самая дальняя qty 1.
	weekly := make([]averagesales.TurnoverRow, 0, 5)
	for i := 1; i <= 5; i++ {
		weekly = append(weekly, averagesales.TurnoverRow{
			ProductID:   "p1",
			PeriodStart: curWeek.AddDate(0, 0, -7*i),
			Qty:         float64(6 - i),
		})
	}

	sales := &stubSales{rowsFn: func(_, _ time.Time, _ string, _ client.ProfitFilter) []client.ProfitRow {
		return []client.ProfitRow{profitRow("p1", 10, 1)}
	}}
	repo := &stubRepo{weekly: weekly}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: true},
	}}
	uc := NewUseCase(repo, sales, products)

	avg, err := uc.AverageSales(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AverageSales() error: %v", err)
	}
	if avg == nil {
		t.Fatal("AverageSales() = nil, want среднее")
	}
	if *avg != 15.0/5.0 {
		t.Errorf("AverageSales() = %v, want %v", *avg, 15.0/5.0)
	}

	if sales.calls != 6 {
		t.Errorf("запросов к МС = %d, want 6 (5 завершённых + текущая неделя)", sales.calls)
	}
	if len(repo.upsW) != 6 {
		t.Fatalf("апсёртнуто строк = %d, want 6", len(repo.upsW))
	}
	hasCurrent := false
	for _, r := range repo.upsW {
		if r.PeriodStart.Equal(curWeek) {
			hasCurrent = true
			if r.Qty != 9 {
				t.Errorf("текущая неделя qty = %v, want 9", r.Qty)
			}
		}
	}
	if !hasCurrent {
		t.Errorf("текущая неделя %v не дозапрошена (нет в апсёрте)", curWeek.Format("2006-01-02"))
	}
}

func TestAverageSales_ErrorFromMS(t *testing.T) {
	sales := &stubSales{err: context.DeadlineExceeded}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := NewUseCase(repo, sales, products)

	if _, err := uc.AverageSales(context.Background(), "p1"); err == nil {
		t.Fatal("AverageSales() error = nil, want ошибку МС")
	}
}

func TestAverageSales_ProductNotFound(t *testing.T) {
	sales := &stubSales{}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{}}
	uc := NewUseCase(repo, sales, products)

	if _, err := uc.AverageSales(context.Background(), "nope"); err == nil {
		t.Fatal("AverageSales() error = nil, want ErrProductNotFound")
	}
}

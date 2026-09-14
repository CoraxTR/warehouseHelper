package usecase

import (
	"context"
	"math"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/msclient/client"
)

// Пакетные обновления оборотов (шов модуля скидок): цена обновления — запрос
// на ПЕРИОД на пачку товаров, а не 13 запросов на товар.

var refreshNow = time.Date(2026, time.September, 14, 10, 0, 0, 0, time.Local) // понедельник

func newRefreshUC(repo *stubRepo, sales *stubSales, prods *stubProducts) *UseCase {
	uc := NewUseCase(repo, sales, prods)
	uc.now = func() time.Time { return refreshNow }

	return uc
}

func TestAveragesReadsWindowWithoutMS(t *testing.T) {
	sales := &stubSales{}
	week := currentPeriodStart(intervalWeek, refreshNow)
	repo := &stubRepo{weekly: []averagesales.TurnoverRow{ // по убыванию периода, как из БД
		{ProductID: "p1", PeriodStart: week, Qty: 5},
		{ProductID: "p1", PeriodStart: week.AddDate(0, 0, -7), Qty: 10},
		{ProductID: "p1", PeriodStart: week.AddDate(0, 0, -14), Qty: 20},
	}}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: true},
	}}

	uc := newRefreshUC(repo, sales, prods)

	got, err := uc.Averages(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}
	if sales.calls != 0 {
		t.Errorf("Averages() сходил в МС %d раз, ожидалось 0 (только БД)", sales.calls)
	}
	// Неполное окно: завершённых 2 (< 5) → окно = 20, 10 + текущий 5 → 35/3.
	want := 35.0 / 3
	if math.Abs(got["p1"]-want) > 1e-9 {
		t.Errorf("Averages()[p1] = %v, want %v", got["p1"], want)
	}
}

func TestRefreshCurrentFetchesOnlyCurrentPeriod(t *testing.T) {
	sales := &stubSales{rowsFn: func(_, _ time.Time, _ string, _ client.ProfitFilter) []client.ProfitRow {
		return []client.ProfitRow{profitRow("p1", 8, 0)}
	}}
	month := currentPeriodStart(intervalMonth, refreshNow)
	repo := &stubRepo{monthly: []averagesales.TurnoverRow{
		{ProductID: "p1", PeriodStart: month, Qty: 8},
		{ProductID: "p1", PeriodStart: month.AddDate(0, -1, 0), Qty: 4},
	}}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}

	uc := newRefreshUC(repo, sales, prods)

	got, err := uc.RefreshCurrent(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("RefreshCurrent() error: %v", err)
	}
	if sales.calls != 1 {
		t.Errorf("RefreshCurrent() запросов в МС = %d, ожидался 1 (только текущий месяц)", sales.calls)
	}
	if len(repo.upsM) != 1 || !repo.upsM[0].PeriodStart.Equal(month) || repo.upsM[0].Qty != 8 {
		t.Errorf("RefreshCurrent() записал %v, ожидалась одна строка текущего месяца qty=8", repo.upsM)
	}
	// Завершённых 1 (< 12) → окно = 4 + текущий 8 → среднее 6.
	if math.Abs(got["p1"]-6) > 1e-9 {
		t.Errorf("RefreshCurrent()[p1] = %v, want 6", got["p1"])
	}
}

func TestRefreshWindowFetchesWholeWindow(t *testing.T) {
	sales := &stubSales{}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}

	uc := newRefreshUC(&stubRepo{}, sales, prods)

	if _, err := uc.RefreshWindow(context.Background(), []string{"p1"}); err != nil {
		t.Fatalf("RefreshWindow() error: %v", err)
	}
	// 12 завершённых месяцев + текущий — по одному запросу отчёта.
	if sales.calls != monthlyWindow+1 {
		t.Errorf("RefreshWindow() запросов в МС = %d, ожидалось %d", sales.calls, monthlyWindow+1)
	}
}

func TestRefreshWithoutProductsDoesNotTouchMS(t *testing.T) {
	sales := &stubSales{}
	uc := newRefreshUC(&stubRepo{}, sales, &stubProducts{})

	got, err := uc.RefreshCurrent(context.Background(), nil)
	if err != nil {
		t.Fatalf("RefreshCurrent(nil) error: %v", err)
	}
	if len(got) != 0 || sales.calls != 0 {
		t.Errorf("RefreshCurrent(nil) = %v, запросов в МС %d; ожидалась пустая карта без запросов", got, sales.calls)
	}
}

func TestAveragesWithoutSalesSkipsProduct(t *testing.T) {
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := newRefreshUC(&stubRepo{}, &stubSales{}, prods)

	got, err := uc.Averages(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}
	if _, ok := got["p1"]; ok {
		t.Errorf("Averages() вернул товар без продаж: %v", got)
	}
}

func TestUniqueIDs(t *testing.T) {
	got := uniqueIDs([]string{"a", "", "a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("uniqueIDs() = %v, want [a b]", got)
	}
}

func TestSplitWindowLimitsFinished(t *testing.T) {
	start := currentPeriodStart(intervalWeek, refreshNow)
	rows := []averagesales.TurnoverRow{
		{ProductID: "p1", PeriodStart: start, Qty: 1},
		{ProductID: "p1", PeriodStart: start.AddDate(0, 0, -7), Qty: 2},
		{ProductID: "p1", PeriodStart: start.AddDate(0, 0, -14), Qty: 3},
		{ProductID: "p1", PeriodStart: start.AddDate(0, 0, 7), Qty: 4}, // будущее — пропуск
	}

	finished, current := splitWindow(rows, 1, start)
	if len(finished) != 1 || finished[0].Qty != 2 {
		t.Errorf("finished = %v, want ровно одну строку qty=2", finished)
	}
	if current == nil || current.Qty != 1 {
		t.Errorf("current = %v, want строку текущего периода qty=1", current)
	}
}

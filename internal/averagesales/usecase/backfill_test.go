package usecase

import (
	"context"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/msclient/client"
)

// rowsEverywhere — продавался всегда (любой период, любой фильтр).
func rowsEverywhere(_, _ time.Time, _ string, f client.ProfitFilter) []client.ProfitRow {
	rows := make([]client.ProfitRow, 0, len(f.ProductIDs)+2)
	if f.ProductFolderID != "" {
		rows = append(rows, profitRow("p1", 5, 0), profitRow("p2", 5, 0))
	}
	for _, id := range f.ProductIDs {
		rows = append(rows, profitRow(id, 5, 0))
	}
	return rows
}

// fixedNow — фиксированные часы тестов: сентябрь 2026, последний завершённый
// месяц — август 2026 (детерминизм счётчиков запросов).
var fixedNow = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

func TestBackfillProduct_MonthlyStopsAt12(t *testing.T) {
	sales := &stubSales{rowsFn: rowsEverywhere}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := NewUseCase(repo, sales, products)
	uc.now = func() time.Time { return fixedNow }

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	// Товар продавался каждый из последних 12 завершённых месяцев —
	// 12 not null, дальше вглубь не идём.
	if sales.calls != 12 {
		t.Errorf("запросов = %d, want 12 (стоп по 12 not null)", sales.calls)
	}
	if len(repo.upsM) != 12 {
		t.Errorf("апсёртнуто строк = %d, want 12", len(repo.upsM))
	}

	// Первый период — последний ЗАВЕРШЁННЫЙ месяц (август 2026), а не январь
	// прошлого года: окно средних наполняется свежими периодами.
	if len(sales.froms) == 0 || !sales.froms[0].Equal(time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("первый период = %v, want 2026-08-01 (последний завершённый)", sales.froms[0])
	}
	for _, f := range sales.froms {
		if f.Equal(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("текущий незакрытый месяц не должен бэкфиллиться: %v", f)
		}
	}
}

func TestBackfillProduct_Reaches2014(t *testing.T) {
	sales := &stubSales{rowsFn: func(from, _ time.Time, _ string, _ client.ProfitFilter) []client.ProfitRow {
		if from.Year() == 2014 && from.Month() == 3 {
			return []client.ProfitRow{profitRow("p1", 5, 0)}
		}
		return nil
	}}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := NewUseCase(repo, sales, products)
	uc.now = func() time.Time { return fixedNow }

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	// От последнего завершённого (авг 2026) вглубь до 2014: 8 месяцев 2026 +
	// 12 лет × 12 = 152 запроса; 12 not null не набрались — упёрлись в 2014.
	if sales.calls != 152 {
		t.Errorf("запросов = %d, want 152 (предел 2014)", sales.calls)
	}
	if len(repo.upsM) != 1 {
		t.Errorf("апсёртнуто строк = %d, want 1 (март 2014)", len(repo.upsM))
	}
}

func TestBackfillProduct_WeeklyOnlyForTrackWeekly(t *testing.T) {
	sales := &stubSales{rowsFn: rowsEverywhere}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: true},
	}}
	uc := NewUseCase(repo, sales, products)
	uc.now = func() time.Time { return fixedNow }

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	// Месячный бэкфилл (12 запросов, стоп по 12) + недельный (5 запросов, стоп по 5).
	if sales.calls != 12+5 {
		t.Errorf("запросов = %d, want %d", sales.calls, 12+5)
	}
	if len(repo.upsW) != 5 {
		t.Errorf("недельных строк = %d, want 5", len(repo.upsW))
	}
}

func TestBackfillProduct_NoSales(t *testing.T) {
	sales := &stubSales{rowsFn: func(_, _ time.Time, _ string, _ client.ProfitFilter) []client.ProfitRow {
		return nil
	}}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := NewUseCase(repo, sales, products)
	uc.now = func() time.Time { return fixedNow }

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	if sales.calls != 152 {
		t.Errorf("запросов = %d, want 152 (продаж нет — прошли до 2014)", sales.calls)
	}
	if len(repo.upsM) != 0 {
		t.Errorf("апсёртнуто строк = %d, want 0", len(repo.upsM))
	}
}

func TestBackfillProduct_Idempotent(t *testing.T) {
	sales := &stubSales{rowsFn: rowsEverywhere}
	repo := &stubRepo{hasM: true, hasW: true}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: true},
	}}
	uc := NewUseCase(repo, sales, products)

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	if sales.calls != 0 {
		t.Errorf("запросов = %d, want 0 (история уже есть)", sales.calls)
	}
}

func TestBackfillProduct_NotInCatalog(t *testing.T) {
	sales := &stubSales{}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{}}
	uc := NewUseCase(repo, sales, products)

	if err := uc.backfillProduct(context.Background(), "ghost"); err != nil {
		t.Fatalf("backfillProduct() для отсутствующего товара: %v, want nil (пропуск)", err)
	}
	if sales.calls != 0 {
		t.Errorf("запросов = %d, want 0", sales.calls)
	}
}

func TestBackfillMissing_GroupsAndSingles(t *testing.T) {
	sales := &stubSales{rowsFn: rowsEverywhere}
	repo := &stubRepo{missingM: []string{"p1", "p2", "p3"}}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", FolderID: "f1"},
		"p2": {ID: "p2", UOM: "шт", FolderID: "f1"},
		"p3": {ID: "p3", UOM: "шт"},
	}}
	uc := NewUseCase(repo, sales, products)
	uc.now = func() time.Time { return fixedNow }

	if err := uc.backfillMissing(context.Background()); err != nil {
		t.Fatalf("backfillMissing() error: %v", err)
	}

	// Последние 12 завершённых месяцев: 12 запросов по группе f1 (productFolder)
	// + 12 по пачке [p3]; все товары продавались каждый месяц — стоп по 12.
	groupCalls, singleCalls := 0, 0
	for _, f := range sales.filters {
		if f.ProductFolderID == "f1" {
			groupCalls++
		}
		if len(f.ProductIDs) == 1 && f.ProductIDs[0] == "p3" {
			singleCalls++
		}
	}
	if groupCalls != 12 {
		t.Errorf("групповых запросов = %d, want 12", groupCalls)
	}
	if singleCalls != 12 {
		t.Errorf("запросов пачки = %d, want 12", singleCalls)
	}

	// 3 товара × 12 завершённых месяцев окна.
	if len(repo.upsM) != 36 {
		t.Errorf("апсёртнуто строк = %d, want 36 (3 товара × 12 месяцев)", len(repo.upsM))
	}
}

// TestBackfillMissing_WeeklyIndependent — недельная дозаливка не зависит от
// месячной: месячных дыр нет, недельная селекция вернула товар — недели
// дозаливаются (раньше недельный набор был подмножеством месячного и такой
// товар не дозаливался никогда).
func TestBackfillMissing_WeeklyIndependent(t *testing.T) {
	sales := &stubSales{rowsFn: rowsEverywhere}
	repo := &stubRepo{missingW: []string{"p1"}}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: true},
	}}
	uc := NewUseCase(repo, sales, products)
	uc.now = func() time.Time { return fixedNow }

	if err := uc.backfillMissing(context.Background()); err != nil {
		t.Fatalf("backfillMissing() error: %v", err)
	}

	if sales.calls != 5 {
		t.Errorf("запросов = %d, want 5 (недельное окно, стоп по 5)", sales.calls)
	}
	if len(repo.upsW) != 5 {
		t.Errorf("недельных строк = %d, want 5", len(repo.upsW))
	}
}

// TestBackfillMissing_WeeklyFiltersTrackWeekly — недельная селекция может
// вернуть товар без track_weekly (у него просто нет недельных строк) —
// он не должен попасть в недельную дозаливку.
func TestBackfillMissing_WeeklyFiltersTrackWeekly(t *testing.T) {
	sales := &stubSales{rowsFn: rowsEverywhere}
	repo := &stubRepo{missingW: []string{"p1"}}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"}, // без track_weekly — в недельную дозаливку не берём
	}}
	uc := NewUseCase(repo, sales, products)
	uc.now = func() time.Time { return fixedNow }

	if err := uc.backfillMissing(context.Background()); err != nil {
		t.Fatalf("backfillMissing() error: %v", err)
	}

	if sales.calls != 0 {
		t.Errorf("запросов = %d, want 0 (не track_weekly — не дозаливаем)", sales.calls)
	}
}

func TestCompletedPeriodStarts(t *testing.T) {
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

	monthly := completedPeriodStarts(intervalMonth, 3, now)
	wantMonthly := []time.Time{
		time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
	}
	if len(monthly) != 3 {
		t.Fatalf("месячных периодов = %d, want 3", len(monthly))
	}
	for i, want := range wantMonthly {
		if !monthly[i].Equal(want) {
			t.Errorf("месяц[%d] = %v, want %v", i, monthly[i], want)
		}
	}

	weekly := completedPeriodStarts(intervalWeek, 2, now)
	wantWeekly := []time.Time{
		time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC),
	}
	if len(weekly) != 2 {
		t.Fatalf("недельных периодов = %d, want 2", len(weekly))
	}
	for i, want := range wantWeekly {
		if !weekly[i].Equal(want) {
			t.Errorf("неделя[%d] = %v, want %v", i, weekly[i], want)
		}
	}
}

func TestBackfillMissing_NoProducts(t *testing.T) {
	sales := &stubSales{}
	repo := &stubRepo{} // without пуст
	products := &stubProducts{}
	uc := NewUseCase(repo, sales, products)

	if err := uc.backfillMissing(context.Background()); err != nil {
		t.Fatalf("backfillMissing() error: %v", err)
	}
	if sales.calls != 0 {
		t.Errorf("запросов = %d, want 0", sales.calls)
	}
}

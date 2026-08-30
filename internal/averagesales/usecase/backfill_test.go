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
	var rows []client.ProfitRow
	if f.ProductFolderID != "" {
		rows = append(rows, profitRow("p1", 5, 0), profitRow("p2", 5, 0))
	}
	for _, id := range f.ProductIDs {
		rows = append(rows, profitRow(id, 5, 0))
	}
	return rows
}

func TestBackfillProduct_MonthlyStopsAt12(t *testing.T) {
	sales := &stubSales{rowsFn: rowsEverywhere}
	repo := &stubRepo{}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := NewUseCase(repo, sales, products)

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	// Товар продавался каждый месяц прошлого года — 12 not null, дальше не идём.
	if sales.calls != 12 {
		t.Errorf("запросов = %d, want 12 (стоп по 12 not null)", sales.calls)
	}
	if len(repo.upsM) != 12 {
		t.Errorf("апсёртнуто строк = %d, want 12", len(repo.upsM))
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

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	// 12 лет (2025..2014) × 12 месяцев = 144 запроса; 12 not null не набрались —
	// упёрлись в 2014.
	if sales.calls != 144 {
		t.Errorf("запросов = %d, want 144 (предел 2014)", sales.calls)
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

	if err := uc.backfillProduct(context.Background(), "p1"); err != nil {
		t.Fatalf("backfillProduct() error: %v", err)
	}

	if sales.calls != 144 {
		t.Errorf("запросов = %d, want 144 (продаж нет — прошли до 2014)", sales.calls)
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
	repo := &stubRepo{without: []string{"p1", "p2", "p3"}}
	products := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", FolderID: "f1"},
		"p2": {ID: "p2", UOM: "шт", FolderID: "f1"},
		"p3": {ID: "p3", UOM: "шт"},
	}}
	uc := NewUseCase(repo, sales, products)

	if err := uc.backfillMissing(context.Background()); err != nil {
		t.Fatalf("backfillMissing() error: %v", err)
	}

	// Месяцы прошлого года: 12 запросов по группе f1 (productFolder) + 12 по пачке [p3].
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

	// 3 товара × 12 месяцев прошлого года.
	if len(repo.upsM) != 36 {
		t.Errorf("апсёртнуто строк = %d, want 36 (3 товара × 12 месяцев)", len(repo.upsM))
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

package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
	"warehouseHelper/internal/stock"
)

type stubStock struct{ products []stock.Product }

func (s *stubStock) Snapshot() []stock.Product { return s.products }

type stubSales struct {
	avg map[string]float64
	err error
}

func (s *stubSales) WeeklyAverages(context.Context, []string) (map[string]float64, error) {
	return s.avg, s.err
}

type stubSurplus struct {
	lots []discounts.SurplusLot
	err  error
}

func (s *stubSurplus) SurplusLots(context.Context, time.Time) ([]discounts.SurplusLot, error) {
	return s.lots, s.err
}

type stubNotifier struct{ texts []string }

func (n *stubNotifier) NotifyWarehouseManagementHTML(text string) error {
	n.texts = append(n.texts, text)

	return nil
}

func testNow() time.Time {
	return time.Date(2026, time.November, 17, 16, 0, 0, 0, time.UTC)
}

func newReportUC(st *stubStock, sales *stubSales, surplus *stubSurplus) *UseCase {
	uc := New(st, sales, surplus, &stubNotifier{})
	uc.now = testNow

	return uc
}

// Три блока: остатки двух первых считает отчёт, избыток приходит от модуля
// скидок и печатается позицией со сроком, накопленным остатком и планом продаж.
func TestBuildThreeBlocks(t *testing.T) {
	st := &stubStock{products: []stock.Product{
		{ID: "p1", Name: "Пусто"},
		{ID: "p2", Name: "Кончается", Lots: []stock.Lot{{Qty: 3}}},
	}}
	sales := &stubSales{avg: map[string]float64{"p2": 5}}
	surplus := &stubSurplus{lots: []discounts.SurplusLot{
		{ProductID: "p3", Name: "Кальби", BestBefore: time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC), CumQty: 120, PlannedQty: 45, Coeff: 2.7},
	}}

	parts, err := newReportUC(st, sales, surplus).Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("частей = %d, want 3: %v", len(parts), parts)
	}
	if !strings.Contains(parts[0], "НЕТ В НАЛИЧИИ · 1") || !strings.Contains(parts[0], "Пусто") {
		t.Errorf("блок «нет в наличии» = %q", parts[0])
	}
	if !strings.Contains(parts[1], "КОНЧАЕТСЯ · 1") {
		t.Errorf("блок «кончается» = %q", parts[1])
	}
	if !strings.Contains(parts[2], "ИЗБЫТОК · 1") {
		t.Errorf("блок «избыток» = %q", parts[2])
	}
	if !strings.Contains(parts[2], "до 21.11") || !strings.Contains(parts[2], "накоплено 120 шт · продадим до срока ~45 шт · коэф 2,7") {
		t.Errorf("содержимое блока «избыток» = %q", parts[2])
	}
}

// Ошибка чтения избытков не отменяет первые два блока и не роняет отчёт.
func TestBuildSurplusErrorKeepsOtherBlocks(t *testing.T) {
	st := &stubStock{products: []stock.Product{{ID: "p1", Name: "Пусто"}}}
	sales := &stubSales{avg: map[string]float64{}}
	surplus := &stubSurplus{err: errors.New("шов недоступен")}

	parts, err := newReportUC(st, sales, surplus).Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(parts) != 1 || !strings.Contains(parts[0], "НЕТ В НАЛИЧИИ") {
		t.Fatalf("части = %v, want один блок «нет в наличии»", parts)
	}
}

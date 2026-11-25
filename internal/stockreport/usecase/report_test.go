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

type stubNotifier struct {
	texts []string
}

func (n *stubNotifier) NotifyWarehouseManagementRich(markdown string) error {
	n.texts = append(n.texts, markdown)

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

// Отчёт уходит ОДНИМ rich-сообщением: три блока под стрелкой (<details>),
// «кончается» и «избыток» — таблицами. Нарезка на части по 3500 байт ушла
// вместе с PNG-выгрузкой (решение владельца 25.11.2026).
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
	if len(parts) != 1 {
		t.Fatalf("частей = %d, want 1 (одно rich-сообщение): %v", len(parts), parts)
	}

	md := parts[0].Text
	for _, want := range []string{
		"<summary>🟥 НЕТ В НАЛИЧИИ · 1</summary>",
		"1. Пусто",
		"<summary>🟨 КОНЧАЕТСЯ · 1</summary>",
		"<tr><td>Кончается</td><td>3</td><td>5</td></tr>",
		"<summary>🟦 ИЗБЫТОК · 1</summary>",
		"<td>Кальби</td><td>21.11</td><td>120 / ~45</td><td>—</td>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в сообщении нет %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "коэф") {
		t.Errorf("коэффициент избытка остался в таблице:\n%s", md)
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
	if len(parts) != 1 || !strings.Contains(parts[0].Text, "НЕТ В НАЛИЧИИ") {
		t.Fatalf("части = %v, want одно сообщение с блоком «нет в наличии»", parts)
	}
	if strings.Contains(parts[0].Text, "ИЗБЫТОК") {
		t.Errorf("блок избытка напечатан при ошибке шва:\n%s", parts[0].Text)
	}
}

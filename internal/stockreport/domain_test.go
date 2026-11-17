package stockreport

import (
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

func avg(v float64) *float64 { return new(v) }

// Split: нулевой остаток уходит ТОЛЬКО в «нет в наличии»; без истории продаж
// сравнивать не с чем; избыток здесь не считается вовсе — его отдаёт модуль
// скидок.
func TestSplit(t *testing.T) {
	got := Split([]Position{
		{Name: "Пусто", Qty: 0, AvgWeek: avg(5)},
		{Name: "Кончается", Qty: 3, AvgWeek: avg(5)},
		{Name: "Ровно норма", Qty: 5, AvgWeek: avg(5)},
		{Name: "Без истории", Qty: 40},
		{Name: "Выше нормы", Qty: 40, AvgWeek: avg(5)},
	})

	if len(got.OutOfStock) != 1 || got.OutOfStock[0].Name != "Пусто" {
		t.Fatalf("нет в наличии = %+v, want только «Пусто»", got.OutOfStock)
	}
	if len(got.LowStock) != 2 {
		t.Fatalf("кончается = %+v, want «Кончается» и «Ровно норма»", got.LowStock)
	}
	if len(got.Surplus) != 0 {
		t.Errorf("избыток = %+v, want пусто (это расчёт модуля скидок)", got.Surplus)
	}
}

// LowStockItems: слово «продажа» вместо «среднее» и округление до целого
// (решение владельца 17.11.2026: 4,6 → 5).
func TestLowStockItemsRoundsAndNames(t *testing.T) {
	r := Report{LowStock: []Position{{Name: "Сыр", Qty: 3, AvgWeek: avg(4.6)}}}

	items := r.LowStockItems()
	if len(items) != 1 {
		t.Fatalf("блоков = %d, want 1", len(items))
	}
	if !strings.Contains(items[0], "продажа 5/нед") {
		t.Errorf("строка %q, want «продажа 5/нед»", items[0])
	}
	if strings.Contains(items[0], "среднее") {
		t.Errorf("строка %q: слово «среднее» осталось", items[0])
	}
}

// Блок избытка: номер, срок, накопленный остаток, план продаж и коэффициент.
func TestSurplusItems(t *testing.T) {
	r := Report{Surplus: []discounts.SurplusLot{
		{Name: "Кальби <нерезанные>", BestBefore: time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC), CumQty: 120, PlannedQty: 45, Coeff: 2.7},
		{Name: "Специя", BestBefore: time.Date(2026, time.December, 3, 0, 0, 0, 0, time.UTC), CumQty: 30, PlannedQty: 12, Coeff: 1.4},
	}}

	items := r.SurplusItems()
	if len(items) != 2 {
		t.Fatalf("блоков = %d, want 2", len(items))
	}
	if !strings.Contains(items[0], "<b>1. Кальби &lt;нерезанные&gt;</b> · до 21.11") {
		t.Errorf("первый блок = %q", items[0])
	}
	if !strings.Contains(items[0], "накоплено 120 шт · продадим до срока ~45 шт · коэф 2,7") {
		t.Errorf("числа первого блока = %q", items[0])
	}
	if !strings.Contains(items[1], "<b>2. Специя</b> · до 03.12") {
		t.Errorf("второй блок = %q", items[1])
	}
}

// Пустой отчёт: отправлять нечего.
func TestReportEmpty(t *testing.T) {
	if !(Report{}).Empty() {
		t.Error("пустой отчёт считается непустым")
	}
	r := Report{Surplus: []discounts.SurplusLot{{Name: "x"}}}
	if r.Empty() {
		t.Error("отчёт с избытком считается пустым")
	}
}

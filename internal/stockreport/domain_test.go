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
func TestSurplusItemsGroupsByProduct(t *testing.T) {
	forty := int16(40)
	r := Report{Surplus: []discounts.SurplusLot{
		{ProductID: "p1", Name: "Кальби", BestBefore: time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC), CumQty: 120, PlannedQty: 45, Coeff: 2.7, Expiry: &forty},
		{ProductID: "p1", Name: "Кальби", BestBefore: time.Date(2026, time.December, 2, 0, 0, 0, 0, time.UTC), CumQty: 160, PlannedQty: 95, Coeff: 1.7},
		{ProductID: "p2", Name: "Специя", BestBefore: time.Date(2026, time.December, 3, 0, 0, 0, 0, time.UTC), CumQty: 30, PlannedQty: 12, Coeff: 1.6},
	}}

	items := r.SurplusItems()
	if len(items) != 2 {
		t.Fatalf("блоков = %d, want 2 (по товарам): %v", len(items), items)
	}
	if !strings.Contains(items[0], "<b>1. Кальби</b> · коэф до 2,7") {
		t.Errorf("заголовок %q, want «1. Кальби · коэф до 2,7»", items[0])
	}
	if strings.Count(items[0], "до 21.11") != 1 || !strings.Contains(items[0], "до 02.12") {
		t.Errorf("сроки %q, want два: 21.11 и 02.12", items[0])
	}
	if !strings.Contains(items[0], "накоплено 160 шт · продадим ~95 шт") {
		t.Errorf("строка дальнего срока %q, want накопление 160 и план 95", items[0])
	}
	if !strings.Contains(items[0], "уже по сроку 40 %") {
		t.Errorf("строка %q, want пометку «уже по сроку 40 %%»", items[0])
	}
	if !strings.Contains(items[1], "<b>2. Специя</b>") {
		t.Errorf("второй блок %q, want нумерацию 2", items[1])
	}
}

func TestSurplusItemsMarksExpiry(t *testing.T) {
	forty, ten := int16(40), int16(10)
	r := Report{Surplus: []discounts.SurplusLot{
		{ProductID: "p1", Name: "Кальби", BestBefore: time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC), CumQty: 120, PlannedQty: 45, Coeff: 2.7, Expiry: &forty},
		{ProductID: "p1", Name: "Кальби", BestBefore: time.Date(2026, time.December, 2, 0, 0, 0, 0, time.UTC), CumQty: 160, PlannedQty: 95, Coeff: 1.7, Expiry: &ten},
	}}

	items := r.SurplusItems()
	if len(items) != 1 {
		t.Fatalf("блоков = %d, want 1 (товар один)", len(items))
	}
	if !strings.Contains(items[0], "уже по сроку 40 %") {
		t.Errorf("строка %q, want пометку «уже по сроку 40 %%»", items[0])
	}
	if strings.Contains(items[0], "уже по сроку 10 %") {
		t.Errorf("строка %q: пометка при равной избытку ступени (10 %%)", items[0])
	}
}

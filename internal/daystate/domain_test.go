package daystate

import (
	"reflect"
	"testing"
	"time"
)

func i16(v int16) *int16 { return &v }

func b(v bool) *bool { return &v }

func TestDiscountFromLots(t *testing.T) {
	tests := []struct {
		name string
		lots []LotState
		want *int16
	}{
		{"пусто — скидки нет", nil, nil},
		{"все без скидки", []LotState{{Qty: 1}, {Qty: 2}}, nil},
		{"один лот со скидкой", []LotState{{Qty: 1, EffectiveGeneral: i16(5)}}, i16(5)},
		{"берётся максимум", []LotState{
			{Qty: 1, EffectiveGeneral: i16(5)},
			{Qty: 1, EffectiveGeneral: i16(20)},
			{Qty: 1},
		}, i16(20)},
		{"ноль — заданная скидка, не отсутствие", []LotState{{Qty: 1, EffectiveGeneral: i16(0)}}, i16(0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DiscountFromLots(tc.lots)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DiscountFromLots = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInStockFromLots(t *testing.T) {
	tests := []struct {
		name string
		lots []LotState
		want bool
	}{
		{"пусто — нет", nil, false},
		{"есть лот с qty>0", []LotState{{Qty: 0}, {Qty: 3}}, true},
		{"все нули — нет", []LotState{{Qty: 0}, {Qty: 0}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := InStockFromLots(tc.lots); got != tc.want {
				t.Errorf("InStockFromLots = %v, want %v", got, tc.want)
			}
		})
	}
}

func stockDate() time.Time {
	return time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
}

func baseDay() DayState {
	return DayState{ProductID: "p1", Date: stockDate()}
}

// Переходы наличия и маркер sold_out_today.
func TestApplyStockChange_StockTransitions(t *testing.T) {
	// Переход «было в наличии → стало нет»: маркер + эмит.
	cur := baseDay()
	cur.InStock = b(true)
	next, soldOutNow, backInStock := ApplyStockChange(cur, nil)
	if !next.SoldOutToday || !soldOutNow || backInStock || next.InStock == nil || *next.InStock {
		t.Errorf("переход в ноль: soldOutToday=%v soldOutNow=%v backInStock=%v inStock=%v", next.SoldOutToday, soldOutNow, backInStock, next.InStock)
	}

	// Уже закончился — перехода нет, эмита нет.
	cur = baseDay()
	cur.InStock = b(false)
	cur.SoldOutToday = true
	next, soldOutNow, backInStock = ApplyStockChange(cur, nil)
	if soldOutNow || backInStock || !next.SoldOutToday {
		t.Errorf("было нет в наличии: soldOutNow=%v backInStock=%v soldOutToday=%v", soldOutNow, backInStock, next.SoldOutToday)
	}

	// Приход после sold_out: маркер НЕ сбрасывается, но «появился» — да.
	cur = baseDay()
	cur.InStock = b(false)
	cur.SoldOutToday = true
	next, soldOutNow, backInStock = ApplyStockChange(cur, []LotState{{Qty: 5}})
	if soldOutNow || !backInStock || !next.SoldOutToday || next.InStock == nil || !*next.InStock {
		t.Errorf("приход после sold_out: soldOutNow=%v backInStock=%v soldOutToday=%v inStock=%v", soldOutNow, backInStock, next.SoldOutToday, next.InStock)
	}

	// Неизвестное состояние (будущая строка) → становится известно, эмита нет.
	cur = baseDay() // InStock nil
	next, soldOutNow, backInStock = ApplyStockChange(cur, []LotState{{Qty: 2}})
	if soldOutNow || backInStock || next.InStock == nil || !*next.InStock {
		t.Errorf("nil → в наличии: soldOutNow=%v backInStock=%v inStock=%v", soldOutNow, backInStock, next.InStock)
	}
}

// Скидка дня: повышения логируются, понижение/снятие — нет.
func TestApplyStockChange_Discounts(t *testing.T) {
	// Повышение скидки: 5 → 10, append в increases.
	cur := baseDay()
	cur.InStock = b(true)
	cur.Discount = i16(5)
	cur.DiscountIncreases = []int16{7}
	next, _, _ := ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(10)}})
	if next.Discount == nil || *next.Discount != 10 {
		t.Errorf("discount = %v, want 10", next.Discount)
	}
	if !reflect.DeepEqual(next.DiscountIncreases, []int16{7, 10}) {
		t.Errorf("increases = %v, want [7 10]", next.DiscountIncreases)
	}

	// Скидка появилась (NULL → 7): повышение.
	cur = baseDay()
	cur.InStock = b(true)
	next, _, _ = ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(7)}})
	if !reflect.DeepEqual(next.DiscountIncreases, []int16{7}) {
		t.Errorf("increases = %v, want [7]", next.DiscountIncreases)
	}

	// Скидка ноль (NULL → 0): значение, но НЕ повышение.
	cur = baseDay()
	cur.InStock = b(true)
	next, _, _ = ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(0)}})
	if next.Discount == nil || *next.Discount != 0 {
		t.Errorf("discount = %v, want 0", next.Discount)
	}
	if len(next.DiscountIncreases) != 0 {
		t.Errorf("increases = %v, want пусто (0 — не повышение)", next.DiscountIncreases)
	}

	// Понижение 10 → 5: колонка меняется, increases не растёт.
	cur = baseDay()
	cur.InStock = b(true)
	cur.Discount = i16(10)
	cur.DiscountIncreases = []int16{10}
	next, _, _ = ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(5)}})
	if next.Discount == nil || *next.Discount != 5 {
		t.Errorf("discount = %v, want 5", next.Discount)
	}
	if !reflect.DeepEqual(next.DiscountIncreases, []int16{10}) {
		t.Errorf("increases = %v, want [10] (понижение не логируется)", next.DiscountIncreases)
	}

	// Снятие скидки 10 → NULL: колонка NULL, increases не растёт.
	cur = baseDay()
	cur.InStock = b(true)
	cur.Discount = i16(10)
	next, _, _ = ApplyStockChange(cur, []LotState{{Qty: 1}})
	if next.Discount != nil {
		t.Errorf("discount = %v, want nil", next.Discount)
	}
	if len(next.DiscountIncreases) != 0 {
		t.Errorf("increases = %v, want пусто", next.DiscountIncreases)
	}
}

func reportDate() time.Time {
	return time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)
}

// Приоритет правил ячейки отчёта (сверху вниз).
func TestCellFor(t *testing.T) {
	today := reportDate()

	tests := []struct {
		name string
		d    *DayState
		date time.Time
		want CellKind
		text string
	}{
		{"будущая дата — пусто", nil, today.Add(24 * time.Hour), CellEmpty, ""},
		{"нет строки — пусто", nil, today, CellEmpty, ""},
		{"in_stock NULL — пусто", &DayState{Date: today, InStock: nil, Orderable: true}, today, CellEmpty, ""},
		{"недоступна — серая", &DayState{Date: today, InStock: b(true), Orderable: false}, today, CellGray, "0%"},
		{"закончилась — красная x", &DayState{Date: today, InStock: b(false), Orderable: true}, today, CellRed, "x"},
		{"в наличии — белая", &DayState{Date: today, InStock: b(true), Orderable: true}, today, CellPlain, "0%"},
		{"в наличии + скидка — жёлтая", &DayState{Date: today, InStock: b(true), Discount: i16(15), Orderable: true}, today, CellYellow, "15%"},
		{"sold_out + скидка — жёлтая с красным шрифтом", &DayState{Date: today, InStock: b(true), Discount: i16(15), SoldOutToday: true, Orderable: true}, today, CellYellowRed, "15%"},
		{"sold_out — красная", &DayState{Date: today, InStock: b(true), SoldOutToday: true, Orderable: true}, today, CellRed, "0%"},
		{"серая выигрывает у sold_out", &DayState{Date: today, InStock: b(true), SoldOutToday: true, Orderable: false}, today, CellGray, "0%"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CellFor(tc.d, tc.date, today)
			if got.Kind != tc.want || got.Text != tc.text {
				t.Errorf("CellFor = {%s %q}, want {%s %q}", got.Kind, got.Text, tc.want, tc.text)
			}
		})
	}
}

// Текст ячейки: цепочка скидок и стейт конца дня.
func TestTextFor(t *testing.T) {
	tests := []struct {
		name string
		d    *DayState
		want string
	}{
		{"скидки нет — 0%", &DayState{InStock: b(true)}, "0%"},
		{"старт без изменений", &DayState{InStock: b(true), DiscountStart: i16(10), Discount: i16(10)}, "10%"},
		{"цепочка повышений", &DayState{InStock: b(true), DiscountStart: i16(10), DiscountIncreases: []int16{15, 20}, Discount: i16(20)}, "10% → 15% → 20%"},
		{"понижение — финал отличается", &DayState{InStock: b(true), DiscountStart: i16(10), Discount: i16(5)}, "10% → 5%"},
		{"закончилась — финал x", &DayState{InStock: b(false), DiscountStart: i16(10), DiscountIncreases: []int16{15}, Discount: i16(15)}, "10% → 15% → x"},
		{"сразу закончилась — просто x", &DayState{InStock: b(false)}, "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TextFor(tc.d); got != tc.want {
				t.Errorf("TextFor = %q, want %q", got, tc.want)
			}
		})
	}
}

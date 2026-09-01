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

func TestApplyStockChange(t *testing.T) {
	date := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	base := DayState{ProductID: "p1", Date: date}

	// Переход «было в наличии → стало нет»: маркер + эмит.
	cur := base
	cur.InStock = b(true)
	next, soldOutNow := ApplyStockChange(cur, nil)
	if !next.SoldOutToday || !soldOutNow || next.InStock == nil || *next.InStock {
		t.Errorf("переход в ноль: soldOutToday=%v soldOutNow=%v inStock=%v", next.SoldOutToday, soldOutNow, next.InStock)
	}

	// Уже закончился — перехода нет, эмита нет.
	cur = base
	cur.InStock = b(false)
	cur.SoldOutToday = true
	next, soldOutNow = ApplyStockChange(cur, nil)
	if soldOutNow || !next.SoldOutToday {
		t.Errorf("было нет в наличии: soldOutNow=%v soldOutToday=%v", soldOutNow, next.SoldOutToday)
	}

	// Приход не сбрасывает маркер и не эмитит.
	cur = base
	cur.InStock = b(false)
	cur.SoldOutToday = true
	next, soldOutNow = ApplyStockChange(cur, []LotState{{Qty: 5}})
	if soldOutNow || !next.SoldOutToday || next.InStock == nil || !*next.InStock {
		t.Errorf("приход после sold_out: soldOutNow=%v soldOutToday=%v inStock=%v", soldOutNow, next.SoldOutToday, next.InStock)
	}

	// Неизвестное состояние (будущая строка) → становится известно, эмита нет.
	cur = base // InStock nil
	next, soldOutNow = ApplyStockChange(cur, []LotState{{Qty: 2}})
	if soldOutNow || next.InStock == nil || !*next.InStock {
		t.Errorf("nil → в наличии: soldOutNow=%v inStock=%v", soldOutNow, next.InStock)
	}

	// Повышение скидки: 5 → 10, append в increases.
	cur = base
	cur.InStock = b(true)
	cur.Discount = i16(5)
	cur.DiscountIncreases = []int16{7}
	next, _ = ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(10)}})
	if next.Discount == nil || *next.Discount != 10 {
		t.Errorf("discount = %v, want 10", next.Discount)
	}
	if !reflect.DeepEqual(next.DiscountIncreases, []int16{7, 10}) {
		t.Errorf("increases = %v, want [7 10]", next.DiscountIncreases)
	}

	// Скидка появилась (NULL → 7): повышение.
	cur = base
	cur.InStock = b(true)
	next, _ = ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(7)}})
	if !reflect.DeepEqual(next.DiscountIncreases, []int16{7}) {
		t.Errorf("increases = %v, want [7]", next.DiscountIncreases)
	}

	// Скидка ноль (NULL → 0): значение, но НЕ повышение.
	cur = base
	cur.InStock = b(true)
	next, _ = ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(0)}})
	if next.Discount == nil || *next.Discount != 0 {
		t.Errorf("discount = %v, want 0", next.Discount)
	}
	if len(next.DiscountIncreases) != 0 {
		t.Errorf("increases = %v, want пусто (0 — не повышение)", next.DiscountIncreases)
	}

	// Понижение 10 → 5: колонка меняется, increases не растёт.
	cur = base
	cur.InStock = b(true)
	cur.Discount = i16(10)
	cur.DiscountIncreases = []int16{10}
	next, _ = ApplyStockChange(cur, []LotState{{Qty: 1, EffectiveGeneral: i16(5)}})
	if next.Discount == nil || *next.Discount != 5 {
		t.Errorf("discount = %v, want 5", next.Discount)
	}
	if !reflect.DeepEqual(next.DiscountIncreases, []int16{10}) {
		t.Errorf("increases = %v, want [10] (понижение не логируется)", next.DiscountIncreases)
	}

	// Снятие скидки 10 → NULL: колонка NULL, increases не растёт.
	cur = base
	cur.InStock = b(true)
	cur.Discount = i16(10)
	next, _ = ApplyStockChange(cur, []LotState{{Qty: 1}})
	if next.Discount != nil {
		t.Errorf("discount = %v, want nil", next.Discount)
	}
	if len(next.DiscountIncreases) != 0 {
		t.Errorf("increases = %v, want пусто", next.DiscountIncreases)
	}
}

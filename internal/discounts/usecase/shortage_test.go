package usecase

import "testing"

// Недостаток: наличие <= продажи в день × (дней до поставки + 1).

func TestIsShortage(t *testing.T) {
	tests := []struct {
		name      string
		available int64
		rate      float64
		lead      int
		want      bool
	}{
		{name: "ровно на порог — недостаток", available: 8, rate: 2, lead: 3, want: true},
		{name: "на единицу больше порога — нет", available: 9, rate: 2, lead: 3, want: false},
		{name: "пустой склад — недостаток", available: 0, rate: 1.5, lead: 2, want: true},
		{name: "дробный порог 2.5×4=10", available: 10, rate: 2.5, lead: 3, want: true},
		{name: "дробный порог: 11 уже хватает", available: 11, rate: 2.5, lead: 3, want: false},
		{name: "срок не заполнен — не судим", available: 0, rate: 5, lead: 0, want: false},
		{name: "продаж нет — не судим", available: 0, rate: 0, lead: 4, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsShortage(tt.available, tt.rate, tt.lead); got != tt.want {
				t.Fatalf("IsShortage(%d, %v, %d) = %v, want %v", tt.available, tt.rate, tt.lead, got, tt.want)
			}
		})
	}
}

func TestShortages(t *testing.T) {
	// p1: два лота (5 и 5) — наличие 10 = CumQty последнего лота, продажи 1/день,
	// срок 4 дня → нужно 5 — наличие хватает.
	// p2: один лот 2 шт, продажи 1/день, срок 3 → нужно 4 — недостаток.
	// p3: лот есть, продаж нет (HasRate false) — признак не определён.
	// p4: срок известен, но товара в выборке нет — не судим.
	pairs := []PairState{
		{ProductID: "p1", Qty: 5, CumQty: 5, HasRate: true, Rate: 1},
		{ProductID: "p1", Qty: 5, CumQty: 10, HasRate: true, Rate: 1},
		{ProductID: "p2", Qty: 2, CumQty: 2, HasRate: true, Rate: 1},
		{ProductID: "p3", Qty: 4, CumQty: 4, HasRate: false},
	}
	lead := map[string]int{"p1": 4, "p2": 3, "p3": 2, "p4": 1}

	got := Shortages(pairs, lead)

	if v, ok := got["p1"]; !ok || v {
		t.Fatalf("p1 = (%v, %v), want (false, есть в карте): наличие 10 > 5", v, ok)
	}
	if v, ok := got["p2"]; !ok || !v {
		t.Fatalf("p2 = (%v, %v), want (true, есть в карте): наличие 2 <= 4", v, ok)
	}
	if _, ok := got["p3"]; ok {
		t.Fatal("p3 без продаж в карту попадать не должен")
	}
	if _, ok := got["p4"]; ok {
		t.Fatal("p4 без лотов в карту попадать не должен")
	}
}

func TestShortages_Empty(t *testing.T) {
	if got := Shortages(nil, nil); len(got) != 0 {
		t.Fatalf("пустые данные = %v, want пустую карту", got)
	}
}

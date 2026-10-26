package usecase

import (
	"context"
	"testing"

	"warehouseHelper/internal/domain"
)

// Срок поставки из дней заказа/доставки варианта (решение владельца:
// минимальное значение по доступным вариантам отгрузки).

func TestVariantLeadDays(t *testing.T) {
	tests := []struct {
		name     string
		order    []int16
		delivery []int16
		want     int
		wantOK   bool
	}{
		{name: "пн → чт", order: []int16{1}, delivery: []int16{4}, want: 3, wantOK: true},
		{name: "пн и чт → ср: ближе от пн", order: []int16{1, 4}, delivery: []int16{3}, want: 2, wantOK: true},
		{name: "пн → пн: доставка следующего круга", order: []int16{1}, delivery: []int16{1}, want: 7, wantOK: true},
		{name: "пн → вс", order: []int16{1}, delivery: []int16{7}, want: 6, wantOK: true},
		{name: "пт → пн (через выходные)", order: []int16{5}, delivery: []int16{1}, want: 3, wantOK: true},
		{name: "нет дней доставки", order: []int16{1}, delivery: nil, wantOK: false},
		{name: "нет дней заказа", order: nil, delivery: []int16{2}, wantOK: false},
		{name: "мусор в днях", order: []int16{0, 8}, delivery: []int16{2}, wantOK: false},
		{name: "мусор в доставке не мешает валидному", order: []int16{1}, delivery: []int16{0, 3}, want: 2, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := variantLeadDays(tt.order, tt.delivery)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("срок = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMinLeadDays(t *testing.T) {
	variants := []domain.OrderSchedule{
		{OrderDays: []int16{1}, DeliveryDays: []int16{6}}, // 5 дней
		{OrderDays: []int16{2}, DeliveryDays: []int16{4}}, // 2 дня — минимум
		{OrderDays: []int16{3}, DeliveryDays: nil},        // срок не выводится
		{OrderDays: nil, DeliveryDays: []int16{5}},        // срок не выводится
	}

	got, ok := minLeadDays(variants)
	if !ok || got != 2 {
		t.Fatalf("minLeadDays = (%d, %v), want (2, true)", got, ok)
	}

	if _, ok := minLeadDays(nil); ok {
		t.Fatal("без вариантов срок выводиться не должен")
	}
}

func TestProductLeadDays(t *testing.T) {
	repo := &stubProductsRepo{}
	sched := &stubOrderSchedules{byProducts: []domain.OrderSchedule{
		{ProductID: "p1", SupplierID: "s1", OrderDays: []int16{1}, DeliveryDays: []int16{5}},
		{ProductID: "p1", SupplierID: "s2", OrderDays: []int16{1}, DeliveryDays: []int16{3}},
		{ProductID: "p2", SupplierID: "s1", OrderDays: []int16{4}, DeliveryDays: []int16{4}},
		{ProductID: "p3", SupplierID: "s1", OrderDays: []int16{2}, DeliveryDays: nil},
	}}
	uc := newSchedulesUC(t, repo, sched, &stubSupplierDirectory{})

	got, err := uc.ProductLeadDays(context.Background(), []string{"p1", "p2", "p3", "p4"})
	if err != nil {
		t.Fatalf("ProductLeadDays: %v", err)
	}
	if got["p1"] != 2 {
		t.Fatalf("p1 = %d, want 2 (минимум по поставщикам)", got["p1"])
	}
	if got["p2"] != 7 {
		t.Fatalf("p2 = %d, want 7 (заказ и доставка в один день)", got["p2"])
	}
	if _, ok := got["p3"]; ok {
		t.Fatal("p3: без дней доставки срок не выводится")
	}
	if _, ok := got["p4"]; ok {
		t.Fatal("p4: без вариантов заказа срок не выводится")
	}
}

func TestProductLeadDays_NotWired(t *testing.T) {
	uc := NewGoodsUseCase(nil, nil, &stubProductsRepo{}, nil)

	if _, err := uc.ProductLeadDays(context.Background(), []string{"p1"}); err == nil {
		t.Fatal("без хранилища вариантов должен быть отказ")
	}

	// Пустой список — пустая карта даже без хранилища.
	got, err := uc.ProductLeadDays(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("пустой список = (%v, %v), want пусто без ошибки", got, err)
	}
}

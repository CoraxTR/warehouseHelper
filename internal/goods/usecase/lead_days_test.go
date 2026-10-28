package usecase

import (
	"context"
	"testing"

	"warehouseHelper/internal/domain"
)

// Срок поставки товара — из delay_days вариантов (0 = на следующий день),
// минимум по тем вариантам, где срок заполнен.

func TestProductLeadDays_MinAcrossVariants(t *testing.T) {
	zero, three, seven := int16(0), int16(3), int16(7)
	repo := &stubProductsRepo{get: &domain.Product{ID: "p1", Name: "Стейк"}}
	sched := &stubOrderSchedules{byProducts: []domain.OrderSchedule{
		{ID: 1, ProductID: "p1", SupplierID: "sup-1", Name: "Общий", DelayDays: &three},
		{ID: 2, ProductID: "p1", SupplierID: "sup-1", Name: "Спец.", DelayDays: &zero},
		{ID: 3, ProductID: "p2", SupplierID: "sup-1", Name: "Общий", DelayDays: &seven},
		{ID: 4, ProductID: "p3", SupplierID: "sup-1", Name: "Общий"}, // срок не заполнен
	}}
	uc := newSchedulesUC(t, repo, sched, &stubSupplierDirectory{})

	got, err := uc.ProductLeadDays(context.Background(), []string{"p1", "p2", "p3"})
	if err != nil {
		t.Fatalf("ProductLeadDays: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("товаров со сроком = %d, want 2 (p3 без срока не считается): %+v", len(got), got)
	}
	if got["p1"] != 0 {
		t.Fatalf("p1 срок = %d, want 0 (минимум по вариантам)", got["p1"])
	}
	if got["p2"] != 7 {
		t.Fatalf("p2 срок = %d, want 7", got["p2"])
	}
	if _, ok := got["p3"]; ok {
		t.Fatal("p3 без заполненного срока попал в карту")
	}
}

func TestProductLeadDays_EmptyAndNoData(t *testing.T) {
	repo := &stubProductsRepo{get: &domain.Product{ID: "p1"}}
	uc := newSchedulesUC(t, repo, &stubOrderSchedules{}, &stubSupplierDirectory{})

	got, err := uc.ProductLeadDays(context.Background(), nil)
	if err != nil {
		t.Fatalf("ProductLeadDays(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("пустой список дал %+v, want пустую карту", got)
	}

	got, err = uc.ProductLeadDays(context.Background(), []string{"p9"})
	if err != nil {
		t.Fatalf("ProductLeadDays(p9): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("товар без вариантов дал %+v, want пустую карту", got)
	}
}

func TestProductLeadDays_NotWired(t *testing.T) {
	uc := NewGoodsUseCase(nil, nil, &stubProductsRepo{}, nil)
	if _, err := uc.ProductLeadDays(context.Background(), []string{"p1"}); err == nil {
		t.Fatal("ProductLeadDays без хранилища = nil, want ошибку")
	}
}

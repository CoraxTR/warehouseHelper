package usecase

import (
	"context"
	"errors"
	"testing"

	"warehouseHelper/internal/domain"
)

// stubOrdersRepo — заглушка OrderRepository для тестов поиска заказа.
type stubOrdersRepo struct {
	byName       *domain.InternalOrder
	byRefGo      *domain.InternalOrder
	err          error
	lastNameArg  string
	lastRefGoArg string
}

// Компиляционная проверка: стаб реализует OrderRepository.
var _ OrderRepository = (*stubOrdersRepo)(nil)

func (s *stubOrdersRepo) GetAllOrders(context.Context) ([]*domain.InternalOrder, error) {
	return nil, nil
}

func (s *stubOrdersRepo) UpdateOrders(context.Context, []*domain.InternalOrder) error {
	return nil
}

func (s *stubOrdersRepo) DeleteOrder(context.Context, string) error {
	return nil
}

func (s *stubOrdersRepo) GetOrdersByIDs(context.Context, []string) ([]*domain.InternalOrder, error) {
	return nil, nil
}

func (s *stubOrdersRepo) GetOrderByName(_ context.Context, name string) (*domain.InternalOrder, error) {
	s.lastNameArg = name

	return s.byName, s.err
}

func (s *stubOrdersRepo) GetOrderByRefGoNumber(_ context.Context, refgoNumber string) (*domain.InternalOrder, error) {
	s.lastRefGoArg = refgoNumber

	return s.byRefGo, s.err
}

func TestOrdersUseCaseGetOrderByName(t *testing.T) {
	want := &domain.InternalOrder{}
	want.SetName("MS-12345")

	repo := &stubOrdersRepo{byName: want}
	uc := NewOrdersUseCase(repo, nil, nil, nil)

	got, err := uc.GetOrderByName(context.Background(), "MS-12345")
	if err != nil {
		t.Fatalf("GetOrderByName error: %v", err)
	}

	if got == nil || got.GetName() != "MS-12345" {
		t.Fatalf("GetOrderByName = %+v, want order with name MS-12345", got)
	}

	if repo.lastNameArg != "MS-12345" {
		t.Errorf("GetOrderByName called with %q, want %q", repo.lastNameArg, "MS-12345")
	}
}

func TestOrdersUseCaseGetOrderByRefGoNumber(t *testing.T) {
	want := &domain.InternalOrder{}
	want.SetRefGoNumber("9001")

	repo := &stubOrdersRepo{byRefGo: want}
	uc := NewOrdersUseCase(repo, nil, nil, nil)

	got, err := uc.GetOrderByRefGoNumber(context.Background(), "9001")
	if err != nil {
		t.Fatalf("GetOrderByRefGoNumber error: %v", err)
	}

	if got == nil || got.GetRefGoNumber() != "9001" {
		t.Fatalf("GetOrderByRefGoNumber = %+v, want order with refGoNumber 9001", got)
	}

	if repo.lastRefGoArg != "9001" {
		t.Errorf("GetOrderByRefGoNumber called with %q, want %q", repo.lastRefGoArg, "9001")
	}
}

func TestOrdersUseCaseGetOrderNotFound(t *testing.T) {
	repo := &stubOrdersRepo{} // nil для обоих — заказ не найден
	uc := NewOrdersUseCase(repo, nil, nil, nil)

	got, err := uc.GetOrderByName(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetOrderByName error: %v", err)
	}

	if got != nil {
		t.Fatalf("GetOrderByName = %+v, want nil", got)
	}
}

func TestOrdersUseCaseGetOrderRepoError(t *testing.T) {
	wantErr := errors.New("db down")

	repo := &stubOrdersRepo{err: wantErr}
	uc := NewOrdersUseCase(repo, nil, nil, nil)

	_, err := uc.GetOrderByRefGoNumber(context.Background(), "9001")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetOrderByRefGoNumber error = %v, want %v", err, wantErr)
	}
}

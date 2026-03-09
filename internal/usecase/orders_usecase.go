package usecase

import (
	"context"
	"warehouseHelper/internal/domain"
)

type OrderRepository interface {
	GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error)
	UpdateOrders(ctx context.Context, orders []*domain.InternalOrder) error
}

type OrdersUseCase struct {
	repo OrderRepository
}

func NewOrdersUseCase(repo OrderRepository) *OrdersUseCase {
	return &OrdersUseCase{repo: repo}
}

func (uc *OrdersUseCase) GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error) {
	return uc.repo.GetAllOrders(ctx)
}

func (uc *OrdersUseCase) UpdateOrders(ctx context.Context, orders []*domain.InternalOrder) error {
	err := uc.repo.UpdateOrders(ctx, orders)
	if err != nil {
		return err
	}

	return nil
}

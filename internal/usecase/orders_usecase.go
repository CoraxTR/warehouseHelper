package usecase

import (
	"context"
	"errors"
	"fmt"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/repository/msapiclient"
)

type OrderRepository interface {
	GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error)
	UpdateOrders(ctx context.Context, orders []*domain.InternalOrder) error
}

type OrdersUseCase struct {
	repo      OrderRepository
	msClient  MoySkladClient
	converter *msapiclient.MoySkladConverter
}

type MoySkladClient interface {
	GetOrderByHREF(ctx context.Context, href string) (*msapiclient.MSOrder, error)
}

func NewOrdersUseCase(repo OrderRepository, msClient MoySkladClient, converter *msapiclient.MoySkladConverter) *OrdersUseCase {
	return &OrdersUseCase{
		repo:      repo,
		msClient:  msClient,
		converter: converter,
	}
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

func (uc *OrdersUseCase) UpdateOrderFromMS(ctx context.Context, href string) error {
	msOrder, err := uc.msClient.GetOrderByHREF(ctx, href)
	if err != nil {
		return fmt.Errorf("failed to fetch order from MS: %w", err)
	}

	domainOrder := uc.converter.ToDomain(msOrder)
	if domainOrder == nil {
		return errors.New("converter returned nil")
	}

	domainOrder.Validate()

	if err := uc.repo.UpdateOrders(ctx, []*domain.InternalOrder{domainOrder}); err != nil {
		return fmt.Errorf("failed to update order in DB: %w", err)
	}

	return nil
}

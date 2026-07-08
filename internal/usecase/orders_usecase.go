package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/repository/msapiclient"
	"warehouseHelper/internal/repository/pdfpreloader"
)

type OrderRepository interface {
	GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error)
	UpdateOrders(ctx context.Context, orders []*domain.InternalOrder) error
	DeleteOrder(ctx context.Context, href string) error
	GetOrdersByHREFs(ctx context.Context, hrefs []string) ([]*domain.InternalOrder, error)
}

type OrdersUseCase struct {
	repo      OrderRepository
	msClient  MoySkladClient
	converter *msapiclient.MSConverter
	pdf       *pdfpreloader.PDFPreloader
}

type MoySkladClient interface {
	GetOrderByHREF(ctx context.Context, href string) (*msapiclient.MSOrder, error)
}

func NewOrdersUseCase(repo OrderRepository, msClient MoySkladClient, converter *msapiclient.MSConverter,
	pdf *pdfpreloader.PDFPreloader) *OrdersUseCase {
	return &OrdersUseCase{
		repo:      repo,
		msClient:  msClient,
		converter: converter,
		pdf:       pdf,
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
	err := uc.DeletePreloadedPDF(href)
	if err != nil {
		return err
	}

	msOrder, err := uc.msClient.GetOrderByHREF(ctx, href)
	if err != nil {
		return fmt.Errorf("failed to fetch order from MS: %w", err)
	}

	domainOrder := uc.converter.ToDomain(msOrder)
	if domainOrder == nil {
		return errors.New("converter returned nil")
	}

	domainOrder.Validate()

	err = uc.repo.UpdateOrders(ctx, []*domain.InternalOrder{domainOrder})
	if err != nil {
		return fmt.Errorf("failed to update order in DB: %w", err)
	}

	uc.pdf.StartPreloading([]*domain.InternalOrder{domainOrder})

	return nil
}

func (uc *OrdersUseCase) DeleteOrder(ctx context.Context, href string) error {
	return uc.repo.DeleteOrder(ctx, href)
}

func (uc *OrdersUseCase) DeletePreloadedPDF(href string) error {
	safeName := filepath.Base(strings.TrimSuffix(href, "/"))
	filePath := filepath.Join("..", "temp", safeName+".pdf")

	err := os.Remove(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}

	return nil
}

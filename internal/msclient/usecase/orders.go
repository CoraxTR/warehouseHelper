package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/msclient/pdfpreloader"
	"warehouseHelper/internal/tempdir"
)

type OrderRepository interface {
	GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error)
	UpdateOrders(ctx context.Context, orders []*domain.InternalOrder) error
	DeleteOrder(ctx context.Context, href string) error
	GetOrdersByHREFs(ctx context.Context, hrefs []string) ([]*domain.InternalOrder, error)
	GetOrderByName(ctx context.Context, name string) (*domain.InternalOrder, error)
	GetOrderByRefGoNumber(ctx context.Context, refgoNumber string) (*domain.InternalOrder, error)
}

type OrdersUseCase struct {
	repo      OrderRepository
	msClient  MoySkladClient
	converter *client.MSConverter
	pdf       *pdfpreloader.PDFPreloader
}

type MoySkladClient interface {
	GetOrderByHREF(ctx context.Context, href string) (*client.MSOrder, error)
}

func NewOrdersUseCase(repo OrderRepository, msClient MoySkladClient, converter *client.MSConverter,
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

// GetOrderByName возвращает заказ по номеру в МойСклад (name).
// Если заказ не найден, возвращает (nil, nil).
func (uc *OrdersUseCase) GetOrderByName(ctx context.Context, name string) (*domain.InternalOrder, error) {
	return uc.repo.GetOrderByName(ctx, name)
}

// GetOrderByRefGoNumber возвращает заказ по номеру в РефГо (refgo_number).
// Если заказ не найден, возвращает (nil, nil).
func (uc *OrdersUseCase) GetOrderByRefGoNumber(ctx context.Context, refgoNumber string) (*domain.InternalOrder, error) {
	return uc.repo.GetOrderByRefGoNumber(ctx, refgoNumber)
}

func (uc *OrdersUseCase) DeletePreloadedPDF(href string) error {
	safeName := filepath.Base(strings.TrimSuffix(href, "/"))
	filePath := filepath.Join(tempdir.Dir, safeName+".pdf")

	err := os.Remove(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}

	return nil
}

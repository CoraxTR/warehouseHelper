package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

// maxPriceKop — верхняя граница цены (10 млрд ₽ в копейках): защита от
// случайного лишнего нуля, реальные цены на порядки ниже.
const maxPriceKop = 1_000_000_000_000

// CatalogReader — названия товаров (реализует *gucase.GoodsUseCase: таблица
// products — каталог). Поставщики читают её только через этот шов.
type CatalogReader interface {
	ProductTitles(ctx context.Context, ids []string) (map[string]string, error)
}

// SetCatalog подключает каталог: без него цены отдаются без названий товаров.
func (uc *MSSuppliersUseCase) SetCatalog(c CatalogReader) {
	uc.catalog = c
}

// SupplierPrices — цены товаров поставщика для страницы поставщика: пары
// (товар, цена) с названиями товаров из каталога, порядок — по названию.
func (uc *MSSuppliersUseCase) SupplierPrices(ctx context.Context, supplierID string) ([]domain.SupplierProductPrice, error) {
	done := metrics.Track(trackPkg, "SupplierPrices")
	defer done()

	supplierID = strings.TrimSpace(supplierID)
	if supplierID == "" {
		return nil, fmt.Errorf("не выбран поставщик: %w", domain.ErrSupplierNotFound)
	}

	prices, err := uc.repo.ListSupplierPrices(ctx, supplierID)
	if err != nil {
		return nil, err
	}
	if len(prices) == 0 {
		return prices, nil
	}

	if uc.catalog != nil {
		ids := make([]string, 0, len(prices))
		for _, p := range prices {
			ids = append(ids, p.ProductID)
		}
		titles, err := uc.catalog.ProductTitles(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("названия товаров: %w", err)
		}
		for i := range prices {
			prices[i].ProductName = titles[prices[i].ProductID]
		}
	}

	slices.SortStableFunc(prices, func(a, b domain.SupplierProductPrice) int {
		return strings.Compare(a.ProductName, b.ProductName)
	})

	return prices, nil
}

// ProductPrices — цены товара по поставщикам (мини-окно на странице «Сроки»).
// Поставщики без цены сюда не попадают: список заказываемых поставщиков
// страница берёт у приёмки (по внешним кодам), а не из цен.
func (uc *MSSuppliersUseCase) ProductPrices(ctx context.Context, productID string) ([]domain.SupplierProductPrice, error) {
	done := metrics.Track(trackPkg, "ProductPrices")
	defer done()

	productID = strings.TrimSpace(productID)
	if productID == "" {
		return nil, fmt.Errorf("не выбран товар: %w", domain.ErrProductNotFound)
	}

	return uc.repo.ListProductPrices(ctx, productID)
}

// SetSupplierPrice — записать цену пары (товар, поставщик). price <= 0 —
// удаление цены: строка существует, только когда цена известна.
func (uc *MSSuppliersUseCase) SetSupplierPrice(ctx context.Context, supplierID, productID string, price int64) error {
	done := metrics.Track(trackPkg, "SetSupplierPrice")
	defer done()

	supplierID = strings.TrimSpace(supplierID)
	productID = strings.TrimSpace(productID)
	if supplierID == "" || productID == "" {
		return errors.New("цена пары: не выбраны поставщик и товар")
	}
	if price > maxPriceKop {
		return fmt.Errorf("цена %d копеек вне разумного диапазона", price)
	}

	if price <= 0 {
		return uc.repo.DeleteSupplierPrice(ctx, supplierID, productID)
	}

	return uc.repo.UpsertSupplierPrice(ctx, supplierID, productID, price)
}

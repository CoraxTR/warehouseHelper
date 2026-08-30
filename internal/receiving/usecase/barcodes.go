// Пакет usecase — сценарии модуля приёмки: ввод связок «внешний код →
// товар» для поставщика с автоматическими тегами вики.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/receiving"
)

// BarcodeRepository — контракт хранилища связок «внешний код → товар»,
// реализуется postgres-репозиторием.
type BarcodeRepository interface {
	LoadSupplierBarcodes(ctx context.Context, supplierID string) ([]receiving.BarcodeRef, error)
	GetSupplierBarcode(ctx context.Context, supplierID, externalCode string) (*receiving.BarcodeRef, error)
	SaveSupplierBarcode(ctx context.Context, supplierID, externalCode, productID string) error
	DeleteSupplierBarcode(ctx context.Context, supplierID, externalCode string) error
	// CountSupplierProductCodes — сколько кодов этого товара осталось у поставщика.
	CountSupplierProductCodes(ctx context.Context, supplierID, productID string) (int, error)
}

// SupplierReader — чтение поставщика (имя для тега), реализуется
// postgres-репозиторием (метод GetSupplier модуля mssuppliers).
type SupplierReader interface {
	GetSupplier(ctx context.Context, id string) (*domain.Supplier, error)
}

// CatalogReader — чтение товара (имя, средний вес), реализуется
// postgres-репозиторием (метод GetProduct модуля каталога).
type CatalogReader interface {
	GetProduct(ctx context.Context, id string) (*domain.Product, error)
}

// WikiBarcodeRef — вики: гарантия страницы товара и теги «поставщик ⇄ товар».
// Реализуется *wucase.WikiUseCase (вики — отдельный модуль, его таблицы
// receiving не трогает).
type WikiBarcodeRef interface {
	EnsureProductPage(ctx context.Context, productID, name, averageWeight string) error
	AddTagToPage(ctx context.Context, title, tag string) error
	RemoveTagFromPage(ctx context.Context, title, tag string) error
}

// BarcodeEditor — ввод связок «внешний код → товар» для поставщика.
// Добавление: UPSERT связки + теги вики (у товара — имя поставщика,
// у поставщика — имя товара). Удаление: снос тегов, только если это был
// последний код этого товара у поставщика.
type BarcodeEditor struct {
	repo      BarcodeRepository
	suppliers SupplierReader
	catalog   CatalogReader
	wiki      WikiBarcodeRef
}

// NewBarcodeEditor создаёт сценарий с переданными хранилищами.
func NewBarcodeEditor(repo BarcodeRepository, suppliers SupplierReader, catalog CatalogReader, wiki WikiBarcodeRef) *BarcodeEditor {
	return &BarcodeEditor{repo: repo, suppliers: suppliers, catalog: catalog, wiki: wiki}
}

// List возвращает связки поставщика (для виджета на карточке поставщика).
func (uc *BarcodeEditor) List(ctx context.Context, supplierID string) ([]receiving.BarcodeRef, error) {
	supplierID = strings.TrimSpace(supplierID)
	if supplierID == "" {
		return nil, errors.New("поставщик не выбран")
	}

	return uc.repo.LoadSupplierBarcodes(ctx, supplierID)
}

// Add добавляет связку «внешний код → товар»: UPSERT + теги вики.
// Идемпотентно: повторное добавление того же кода обновляет товар.
func (uc *BarcodeEditor) Add(ctx context.Context, supplierID, externalCode, productID string) error {
	supplierID = strings.TrimSpace(supplierID)
	externalCode = strings.TrimSpace(externalCode)
	productID = strings.TrimSpace(productID)

	if supplierID == "" {
		return errors.New("поставщик не выбран")
	}
	if externalCode == "" {
		return errors.New("внешний код не может быть пустым")
	}
	if len(externalCode) > 64 {
		return fmt.Errorf("внешний код слишком длинный (максимум 64 символа)")
	}
	if productID == "" {
		return errors.New("товар не выбран")
	}

	// Имена для тегов; заодно проверка, что поставщик и товар существуют.
	supplier, err := uc.suppliers.GetSupplier(ctx, supplierID)
	if err != nil {
		if errors.Is(err, domain.ErrSupplierNotFound) {
			return err
		}
		return fmt.Errorf("получить поставщика %s: %w", supplierID, err)
	}
	product, err := uc.catalog.GetProduct(ctx, productID)
	if err != nil {
		if errors.Is(err, domain.ErrProductNotFound) {
			return err
		}
		return fmt.Errorf("получить товар %s: %w", productID, err)
	}

	// Страница товара в вики (создаётся при отсутствии), затем связка,
	// затем теги. Все операции идемпотентны — повтор после ошибки безопасен.
	if err := uc.wiki.EnsureProductPage(ctx, product.ID, product.Name, avgWeightString(product.AverageWeight)); err != nil {
		return fmt.Errorf("гарантировать страницу вики товара %s: %w", product.ID, err)
	}
	if err := uc.repo.SaveSupplierBarcode(ctx, supplierID, externalCode, productID); err != nil {
		return fmt.Errorf("сохранить связку %q: %w", externalCode, err)
	}
	if err := uc.wiki.AddTagToPage(ctx, product.Name, supplier.Name); err != nil {
		return fmt.Errorf("добавить тег поставщика товару %q: %w", product.Name, err)
	}
	if err := uc.wiki.AddTagToPage(ctx, supplier.Name, product.Name); err != nil {
		return fmt.Errorf("добавить тег товара поставщику %q: %w", supplier.Name, err)
	}

	return nil
}

// Remove удаляет связку; если это последний код этого товара у поставщика —
// снимает теги с обеих страниц.
func (uc *BarcodeEditor) Remove(ctx context.Context, supplierID, externalCode string) error {
	supplierID = strings.TrimSpace(supplierID)
	externalCode = strings.TrimSpace(externalCode)

	if supplierID == "" {
		return errors.New("поставщик не выбран")
	}
	if externalCode == "" {
		return errors.New("внешний код не может быть пустым")
	}

	b, err := uc.repo.GetSupplierBarcode(ctx, supplierID, externalCode)
	if err != nil {
		return fmt.Errorf("получить связку %q: %w", externalCode, err)
	}
	if b == nil {
		return fmt.Errorf("связка %q у поставщика не найдена", externalCode)
	}

	if err := uc.repo.DeleteSupplierBarcode(ctx, supplierID, externalCode); err != nil {
		return fmt.Errorf("удалить связку %q: %w", externalCode, err)
	}

	count, err := uc.repo.CountSupplierProductCodes(ctx, supplierID, b.ProductID)
	if err != nil {
		return fmt.Errorf("посчитать коды товара %s: %w", b.ProductID, err)
	}
	if count > 0 {
		return nil // у товара остались другие коды этого поставщика — теги актуальны
	}

	supplier, err := uc.suppliers.GetSupplier(ctx, supplierID)
	if err != nil {
		return err
	}
	product, err := uc.catalog.GetProduct(ctx, b.ProductID)
	if err != nil {
		return err
	}

	if err := uc.wiki.RemoveTagFromPage(ctx, product.Name, supplier.Name); err != nil {
		return fmt.Errorf("снять тег поставщика с товара %q: %w", product.Name, err)
	}
	if err := uc.wiki.RemoveTagFromPage(ctx, supplier.Name, product.Name); err != nil {
		return fmt.Errorf("снять тег товара с поставщика %q: %w", supplier.Name, err)
	}

	return nil
}

// avgWeightString форматирует средний вес каталога (кг, float) в строку вики.
func avgWeightString(v *float64) string {
	if v == nil {
		return ""
	}

	return strconv.FormatFloat(*v, 'f', -1, 64)
}

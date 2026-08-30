package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/receiving"
)

// LoadSupplierBarcodes возвращает все связки «внешний код → товар» поставщика
// с именами товаров (для виджета на карточке поставщика).
func (pg *PGClient) LoadSupplierBarcodes(ctx context.Context, supplierID string) ([]receiving.BarcodeRef, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT psb.external_code, psb.product_id, p.name
        FROM product_supplier_barcodes psb
        JOIN products p ON p.id = psb.product_id
        WHERE psb.supplier_id = $1
        ORDER BY psb.external_code
    `, supplierID)
	if err != nil {
		return nil, fmt.Errorf("load supplier barcodes %s: %w", supplierID, err)
	}
	defer rows.Close()

	out := make([]receiving.BarcodeRef, 0)
	for rows.Next() {
		var b receiving.BarcodeRef

		if err := rows.Scan(&b.ExternalCode, &b.ProductID, &b.ProductName); err != nil {
			return nil, err
		}

		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// GetSupplierBarcode возвращает одну связку. Если нет — (nil, nil).
//
//nolint:nilnil // контракт репозитория: (nil, nil) = связка не найдена
func (pg *PGClient) GetSupplierBarcode(ctx context.Context, supplierID, externalCode string) (*receiving.BarcodeRef, error) {
	row := pg.Pool.QueryRow(ctx, `
        SELECT psb.external_code, psb.product_id, p.name
        FROM product_supplier_barcodes psb
        JOIN products p ON p.id = psb.product_id
        WHERE psb.supplier_id = $1 AND psb.external_code = $2
    `, supplierID, externalCode)

	var b receiving.BarcodeRef
	if err := row.Scan(&b.ExternalCode, &b.ProductID, &b.ProductName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	return &b, nil
}

// SaveSupplierBarcode сохраняет связку (UPSERT: повторное добавление того же
// кода обновляет товар).
func (pg *PGClient) SaveSupplierBarcode(ctx context.Context, supplierID, externalCode, productID string) error {
	if _, err := pg.Pool.Exec(ctx, `
        INSERT INTO product_supplier_barcodes (supplier_id, external_code, product_id)
        VALUES ($1, $2, $3)
        ON CONFLICT (supplier_id, external_code) DO UPDATE SET product_id = EXCLUDED.product_id
    `, supplierID, externalCode, productID); err != nil {
		return fmt.Errorf("upsert supplier barcode %q: %w", externalCode, err)
	}

	return nil
}

// DeleteSupplierBarcode удаляет связку.
func (pg *PGClient) DeleteSupplierBarcode(ctx context.Context, supplierID, externalCode string) error {
	if _, err := pg.Pool.Exec(ctx, `
        DELETE FROM product_supplier_barcodes
        WHERE supplier_id = $1 AND external_code = $2
    `, supplierID, externalCode); err != nil {
		return fmt.Errorf("delete supplier barcode %q: %w", externalCode, err)
	}

	return nil
}

// CountSupplierProductCodes возвращает, сколько кодов этого товара осталось
// у поставщика (для решения о сносе тегов вики).
func (pg *PGClient) CountSupplierProductCodes(ctx context.Context, supplierID, productID string) (int, error) {
	var n int

	if err := pg.Pool.QueryRow(ctx, `
        SELECT count(*)
        FROM product_supplier_barcodes
        WHERE supplier_id = $1 AND product_id = $2
    `, supplierID, productID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count supplier product codes %s/%s: %w", supplierID, productID, err)
	}

	return n, nil
}

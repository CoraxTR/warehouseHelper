package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/receiving"
)

// LoadSupplierBarcodes возвращает все связки «внешний код → товар» поставщика
// с данными товаров (виджет поставщика и кеш приёмки).
func (pg *PGClient) LoadSupplierBarcodes(ctx context.Context, supplierID string) ([]receiving.BarcodeRef, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT psb.external_code, psb.product_id, p.name, p.internal_code, p.uom
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
		var (
			b   receiving.BarcodeRef
			uom string
		)
		if err := rows.Scan(&b.ExternalCode, &b.ProductID, &b.ProductName, &b.InternalCode, &uom); err != nil {
			return nil, err
		}
		b.Weighted = weightedUOM(uom)

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
        SELECT psb.external_code, psb.product_id, p.name, p.internal_code, p.uom
        FROM product_supplier_barcodes psb
        JOIN products p ON p.id = psb.product_id
        WHERE psb.supplier_id = $1 AND psb.external_code = $2
    `, supplierID, externalCode)

	var (
		b   receiving.BarcodeRef
		uom string
	)
	if err := row.Scan(&b.ExternalCode, &b.ProductID, &b.ProductName, &b.InternalCode, &uom); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}
	b.Weighted = weightedUOM(uom)

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

// weightedUOM — весовой ли товар по единице измерения (тип учёта из uom).
func weightedUOM(uom string) bool {
	switch strings.ToLower(strings.TrimSpace(uom)) {
	case "кг", "г", "т":
		return true
	default:
		return false
	}
}

// LoadCatalogProductsByCodes возвращает товары каталога по внутренним кодам
// (для резолва внутренних штрих-кодов 29/33; имя с префиксом Catalog, чтобы
// не конфликтовать со stock.LoadProductsByCodes — другой тип результата).
func (pg *PGClient) LoadCatalogProductsByCodes(ctx context.Context, codes []string) (map[string]receiving.ProductRef, error) {
	out := make(map[string]receiving.ProductRef, len(codes))
	if len(codes) == 0 {
		return out, nil
	}

	rows, err := pg.Pool.Query(ctx, `
        SELECT id, internal_code, name, uom
        FROM products
        WHERE internal_code = ANY($1)
    `, codes)
	if err != nil {
		return nil, fmt.Errorf("load products by codes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			ref receiving.ProductRef
			uom string
		)
		if err := rows.Scan(&ref.ProductID, &ref.InternalCode, &ref.Name, &uom); err != nil {
			return nil, err
		}
		ref.Weighted = weightedUOM(uom)
		out[ref.InternalCode] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// LoadCatalogAllRefs возвращает все товары каталога с внутренними кодами
// (для кеша страницы приёмки).
func (pg *PGClient) LoadCatalogAllRefs(ctx context.Context) ([]receiving.ProductRef, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT id, internal_code, name, uom
        FROM products
        WHERE internal_code <> ''
        ORDER BY name
    `)
	if err != nil {
		return nil, fmt.Errorf("load catalog refs: %w", err)
	}
	defer rows.Close()

	out := make([]receiving.ProductRef, 0)
	for rows.Next() {
		var (
			ref receiving.ProductRef
			uom string
		)
		if err := rows.Scan(&ref.ProductID, &ref.InternalCode, &ref.Name, &uom); err != nil {
			return nil, err
		}
		ref.Weighted = weightedUOM(uom)
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// InsertReceivedWeights записывает веса принятых единиц (граммы).
func (pg *PGClient) InsertReceivedWeights(ctx context.Context, rows []receiving.WeightRow) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("received weights begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, r := range rows {
		if _, err := tx.Exec(ctx, `
            INSERT INTO product_received_weights (product_id, weight)
            VALUES ($1, $2)
        `, r.ProductID, r.WeightG); err != nil {
			return fmt.Errorf("insert received weight %s: %w", r.ProductID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("received weights commit: %w", err)
	}

	return nil
}

// TrimReceivedWeights оставляет последние keep весов товара (FIFO по id).
func (pg *PGClient) TrimReceivedWeights(ctx context.Context, productID string, keep int) error {
	if _, err := pg.Pool.Exec(ctx, `
        DELETE FROM product_received_weights
        WHERE product_id = $1 AND id NOT IN (
            SELECT id FROM product_received_weights
            WHERE product_id = $1
            ORDER BY id DESC
            LIMIT $2
        )
    `, productID, keep); err != nil {
		return fmt.Errorf("trim received weights %s: %w", productID, err)
	}

	return nil
}

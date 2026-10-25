package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/domain"
)

// supplierColumns — колонки suppliers в порядке SELECT/INSERT.
const supplierColumns = `id, name, decode_rules, box_decode_rules, min_order_amount`

// ListSuppliers возвращает всех поставщиков по алфавиту без учёта регистра.
func (pg *PGClient) ListSuppliers(ctx context.Context) ([]domain.Supplier, error) {
	rows, err := pg.Pool.Query(ctx, `
		SELECT `+supplierColumns+`
		FROM suppliers
		ORDER BY lower(name), name`)
	if err != nil {
		return nil, fmt.Errorf("query suppliers: %w", err)
	}
	defer rows.Close()

	suppliers := make([]domain.Supplier, 0)
	for rows.Next() {
		s, err := scanSupplier(rows)
		if err != nil {
			return nil, err
		}
		suppliers = append(suppliers, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate suppliers: %w", err)
	}

	return suppliers, nil
}

// GetSupplier возвращает поставщика по id; если нет — (nil, nil).
func (pg *PGClient) GetSupplier(ctx context.Context, id string) (*domain.Supplier, error) {
	row := pg.Pool.QueryRow(ctx, `
		SELECT `+supplierColumns+`
		FROM suppliers
		WHERE id = $1`, id)

	s, err := scanSupplier(row)
	if errors.Is(err, pgx.ErrNoRows) {
		//nolint:nilnil // контракт: поставщик не найден
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get supplier %s: %w", id, err)
	}

	return s, nil
}

// SaveSupplier создаёт или обновляет поставщика (upsert по id — первичному ключу).
func (pg *PGClient) SaveSupplier(ctx context.Context, s *domain.Supplier) error {
	_, err := pg.Pool.Exec(ctx, `
		INSERT INTO suppliers (`+supplierColumns+`)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			decode_rules = EXCLUDED.decode_rules,
			box_decode_rules = EXCLUDED.box_decode_rules,
			min_order_amount = EXCLUDED.min_order_amount`,
		s.ID, s.Name, s.DecodeRules, s.BoxDecodeRules, s.MinOrderAmount,
	)
	if err != nil {
		return fmt.Errorf("save supplier %s: %w", s.ID, err)
	}

	return nil
}

// DeleteSupplier удаляет поставщика по id. Каскады (barcodes/prices/order
// schedules — CASCADE, wiki — SET NULL) выполняет БД. Если поставщика нет —
// domain.ErrSupplierNotFound.
func (pg *PGClient) DeleteSupplier(ctx context.Context, id string) error {
	tag, err := pg.Pool.Exec(ctx, `DELETE FROM suppliers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete supplier %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrSupplierNotFound
	}

	return nil
}

// rowScanner — общий интерфейс QueryRow/Query для scanSupplier.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSupplier(row rowScanner) (*domain.Supplier, error) {
	var s domain.Supplier
	err := row.Scan(
		&s.ID, &s.Name, &s.DecodeRules, &s.BoxDecodeRules, &s.MinOrderAmount,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSupplierPrices — цены товаров поставщика (только пары с известной ценой,
// порядок — по id товара). Названия заполняет usecase: products — каталог.
func (pg *PGClient) ListSupplierPrices(ctx context.Context, supplierID string) ([]domain.SupplierProductPrice, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT supplier_id, product_id, current_price
        FROM product_supplier_prices
        WHERE supplier_id = $1
        ORDER BY product_id`, supplierID)
	if err != nil {
		return nil, fmt.Errorf("list supplier prices %s: %w", supplierID, err)
	}
	defer rows.Close()

	out := make([]domain.SupplierProductPrice, 0, 16)
	for rows.Next() {
		var p domain.SupplierProductPrice
		if err := rows.Scan(&p.SupplierID, &p.ProductID, &p.Price); err != nil {
			return nil, err
		}
		out = append(out, p)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// ListProductPrices — цены товара по всем поставщикам (мини-окно «Сроков»).
func (pg *PGClient) ListProductPrices(ctx context.Context, productID string) ([]domain.SupplierProductPrice, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT p.supplier_id, s.name, p.product_id, p.current_price
        FROM product_supplier_prices p
        JOIN suppliers s ON s.id = p.supplier_id
        WHERE p.product_id = $1
        ORDER BY s.name`, productID)
	if err != nil {
		return nil, fmt.Errorf("list product prices %s: %w", productID, err)
	}
	defer rows.Close()

	out := make([]domain.SupplierProductPrice, 0, 8)
	for rows.Next() {
		var p domain.SupplierProductPrice
		if err := rows.Scan(&p.SupplierID, &p.SupplierName, &p.ProductID, &p.Price); err != nil {
			return nil, err
		}
		out = append(out, p)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// UpsertSupplierPrice — записать цену пары (товар, поставщик), копейки.
func (pg *PGClient) UpsertSupplierPrice(ctx context.Context, supplierID, productID string, price int64) error {
	_, err := pg.Pool.Exec(ctx, `
        INSERT INTO product_supplier_prices (supplier_id, product_id, current_price)
        VALUES ($1, $2, $3)
        ON CONFLICT (supplier_id, product_id) DO UPDATE
        SET current_price = EXCLUDED.current_price`,
		supplierID, productID, price,
	)
	if err != nil {
		return fmt.Errorf("upsert supplier price %s/%s: %w", supplierID, productID, err)
	}

	return nil
}

// DeleteSupplierPrice — убрать цену пары: строка живёт, только когда цена
// известна, поэтому «нет цены» = нет строки.
func (pg *PGClient) DeleteSupplierPrice(ctx context.Context, supplierID, productID string) error {
	_, err := pg.Pool.Exec(ctx, `
        DELETE FROM product_supplier_prices
        WHERE supplier_id = $1 AND product_id = $2`,
		supplierID, productID,
	)
	if err != nil {
		return fmt.Errorf("delete supplier price %s/%s: %w", supplierID, productID, err)
	}

	return nil
}

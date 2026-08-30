package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/domain"
)

// supplierColumns — колонки suppliers в порядке SELECT/INSERT (без id/name уточнений).
const supplierColumns = `id, name, decode_rules, box_decode_rules,
	order_days, delivery_days, delay_days, min_order_amount, order_cutoff_time,
	special_order_days, special_delivery_days, special_delay_days`

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
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			decode_rules = EXCLUDED.decode_rules,
			box_decode_rules = EXCLUDED.box_decode_rules,
			order_days = EXCLUDED.order_days,
			delivery_days = EXCLUDED.delivery_days,
			delay_days = EXCLUDED.delay_days,
			min_order_amount = EXCLUDED.min_order_amount,
			order_cutoff_time = EXCLUDED.order_cutoff_time,
			special_order_days = EXCLUDED.special_order_days,
			special_delivery_days = EXCLUDED.special_delivery_days,
			special_delay_days = EXCLUDED.special_delay_days`,
		s.ID, s.Name, s.DecodeRules, s.BoxDecodeRules,
		s.OrderDays, s.DeliveryDays, s.DelayDays, s.MinOrderAmount, s.OrderCutoffTime,
		s.SpecialOrderDays, s.SpecialDeliveryDays, s.SpecialDelayDays,
	)
	if err != nil {
		return fmt.Errorf("save supplier %s: %w", s.ID, err)
	}

	return nil
}

// DeleteSupplier удаляет поставщика по id. Каскады (barcodes/prices — CASCADE,
// wiki — SET NULL) выполняет БД. Если поставщика нет — domain.ErrSupplierNotFound.
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
		&s.ID, &s.Name, &s.DecodeRules, &s.BoxDecodeRules,
		&s.OrderDays, &s.DeliveryDays, &s.DelayDays, &s.MinOrderAmount, &s.OrderCutoffTime,
		&s.SpecialOrderDays, &s.SpecialDeliveryDays, &s.SpecialDelayDays,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

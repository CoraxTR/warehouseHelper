package postgres

import (
	"context"
	"fmt"

	"warehouseHelper/internal/domain"
)

// orderScheduleColumns — колонки product_order_schedules в порядке SELECT/scan
// (s — алиас таблицы в запросах). Порядок обязан совпадать с scanOrderSchedule.
const orderScheduleColumns = `s.id, s.product_id, s.supplier_id, s.name,
	s.order_days, s.delivery_days, s.delay_days, s.order_cutoff_time`

// ListProductOrderSchedules возвращает варианты заказа товара у поставщиков:
// по алфавиту поставщика, затем по названию варианта. Название поставщика
// читается JOIN'ом — это read-model для страницы карточки позиции.
func (pg *PGClient) ListProductOrderSchedules(ctx context.Context, productID string) ([]domain.OrderSchedule, error) {
	rows, err := pg.Pool.Query(ctx, `
		SELECT `+orderScheduleColumns+`, sup.name
		FROM product_order_schedules s
		JOIN suppliers sup ON sup.id = s.supplier_id
		WHERE s.product_id = $1
		ORDER BY lower(sup.name), sup.name, lower(s.name), s.name`, productID)
	if err != nil {
		return nil, fmt.Errorf("query order schedules of product %s: %w", productID, err)
	}
	defer rows.Close()

	list := make([]domain.OrderSchedule, 0)
	for rows.Next() {
		item, err := scanOrderSchedule(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order schedules of product %s: %w", productID, err)
	}

	return list, nil
}

// ReplaceProductOrderSchedules заменяет ВСЕ варианты заказа товара переданным
// набором в одной транзакции: страница карточки сохраняет список целиком
// (удалённая в форме строка исчезает из БД). Пустой список — товар без
// вариантов заказа.
func (pg *PGClient) ReplaceProductOrderSchedules(ctx context.Context, productID string, list []domain.OrderSchedule) error {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("replace order schedules begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM product_order_schedules WHERE product_id = $1`, productID); err != nil {
		return fmt.Errorf("replace order schedules delete %s: %w", productID, err)
	}

	for _, item := range list {
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_order_schedules (
				product_id, supplier_id, name, order_days, delivery_days, delay_days, order_cutoff_time
			) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			productID, item.SupplierID, item.Name,
			nonNilDays(item.OrderDays), nonNilDays(item.DeliveryDays),
			item.DelayDays, item.OrderCutoffTime,
		); err != nil {
			return fmt.Errorf("replace order schedules insert %s/%s: %w", productID, item.SupplierID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("replace order schedules commit: %w", err)
	}

	return nil
}

// nonNilDays отдаёт пустой массив вместо nil: колонки дней NOT NULL, а pgx
// отправляет nil как SQL NULL.
func nonNilDays(days []int16) []int16 {
	if days == nil {
		return []int16{}
	}

	return days
}

// scanOrderSchedule читает строку выборки вариантов заказа (общий scan-хелпер
// и для QueryRow, и для rows.Next).
func scanOrderSchedule(row rowScanner) (*domain.OrderSchedule, error) {
	var s domain.OrderSchedule
	err := row.Scan(
		&s.ID, &s.ProductID, &s.SupplierID, &s.Name,
		&s.OrderDays, &s.DeliveryDays, &s.DelayDays, &s.OrderCutoffTime,
		&s.SupplierName,
	)
	if err != nil {
		return nil, err
	}

	return &s, nil
}

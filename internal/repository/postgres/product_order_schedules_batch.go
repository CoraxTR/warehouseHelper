package postgres

import (
	"context"
	"fmt"

	"warehouseHelper/internal/domain"
)

// scheduleReadBatch — размер пачки id при чтении вариантов заказа списком
// товаров: один запрос на пачку, а не запрос на товар (срок поставки товара
// считается сразу по всему списку — например, для всего окна скидок).
const scheduleReadBatch = 500

// ListOrderSchedulesByProducts — варианты заказа перечисленных товаров
// (без JOIN поставщика: для срока поставки нужны только дни). Пачки по
// scheduleReadBatch; порядок — товар, затем id варианта.
func (pg *PGClient) ListOrderSchedulesByProducts(ctx context.Context, productIDs []string) ([]domain.OrderSchedule, error) {
	out := make([]domain.OrderSchedule, 0, len(productIDs))
	for _, batch := range idBatches(productIDs, scheduleReadBatch) {
		rows, err := pg.Pool.Query(ctx, `
            SELECT `+orderScheduleColumns+`
            FROM product_order_schedules s
            WHERE s.product_id = ANY($1::text[])
            ORDER BY s.product_id, s.id`, batch)
		if err != nil {
			return nil, fmt.Errorf("query order schedules of %d products: %w", len(batch), err)
		}

		for rows.Next() {
			item, err := scanOrderSchedule(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, *item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate order schedules: %w", err)
		}
		rows.Close()
	}

	return out, nil
}

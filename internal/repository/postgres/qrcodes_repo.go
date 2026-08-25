package postgres

import (
	"context"
	"fmt"
	"time"

	"warehouseHelper/internal/domain"
)

// UpsertOrderWithPhotos создаёт заказ по номеру (или находит существующий)
// и добавляет к нему фотографии в одной транзакции. Возвращает id заказа.
func (pg *PGClient) UpsertOrderWithPhotos(ctx context.Context, orderNumber string, photos []domain.QRPhoto) (int64, error) {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin qrcode tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var orderID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO qrcode_orders (order_number) VALUES ($1)
		 ON CONFLICT (order_number) DO UPDATE SET order_number = EXCLUDED.order_number
		 RETURNING id`,
		orderNumber,
	).Scan(&orderID)
	if err != nil {
		return 0, fmt.Errorf("upsert qrcode order %q: %w", orderNumber, err)
	}

	for _, ph := range photos {
		if _, err := tx.Exec(ctx,
			`INSERT INTO qrcode_photos (id, order_id, ext) VALUES ($1, $2, $3)`,
			ph.ID, orderID, ph.Ext,
		); err != nil {
			return 0, fmt.Errorf("insert qrcode photo %s: %w", ph.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit qrcode order %q: %w", orderNumber, err)
	}

	return orderID, nil
}

// GetOrdersWithPhotos возвращает все заказы с фотографиями (порядок не
// гарантирован — сортировку по номеру делает usecase).
func (pg *PGClient) GetOrdersWithPhotos(ctx context.Context) ([]domain.QROrder, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT o.id, o.order_number, p.id, p.ext, p.created_at
		 FROM qrcode_orders o
		 LEFT JOIN qrcode_photos p ON p.order_id = o.id
		 ORDER BY o.id`)
	if err != nil {
		return nil, fmt.Errorf("query qrcode orders: %w", err)
	}
	defer rows.Close()

	orders := make([]domain.QROrder, 0)
	indexByID := make(map[int64]int)

	for rows.Next() {
		var (
			orderID   int64
			orderNum  string
			photoID   *string
			ext       *string
			createdAt *time.Time
		)
		if err := rows.Scan(&orderID, &orderNum, &photoID, &ext, &createdAt); err != nil {
			return nil, fmt.Errorf("scan qrcode order: %w", err)
		}

		idx, ok := indexByID[orderID]
		if !ok {
			orders = append(orders, domain.QROrder{
				ID:          orderID,
				OrderNumber: orderNum,
				Photos:      make([]domain.QRPhoto, 0),
			})
			idx = len(orders) - 1
			indexByID[orderID] = idx
		}

		if photoID != nil {
			orders[idx].Photos = append(orders[idx].Photos, domain.QRPhoto{
				ID:        *photoID,
				Ext:       *ext,
				CreatedAt: *createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate qrcode orders: %w", err)
	}

	return orders, nil
}

// DeletePhotosByIDs удаляет фотографии по id (например, после очистки файлов).
func (pg *PGClient) DeletePhotosByIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	if _, err := pg.Pool.Exec(ctx, `DELETE FROM qrcode_photos WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("delete qrcode photos: %w", err)
	}

	return nil
}

// DeleteEmptyOrders удаляет заказы, у которых не осталось фотографий.
func (pg *PGClient) DeleteEmptyOrders(ctx context.Context) error {
	if _, err := pg.Pool.Exec(ctx,
		`DELETE FROM qrcode_orders o
		 WHERE NOT EXISTS (SELECT 1 FROM qrcode_photos p WHERE p.order_id = o.id)`); err != nil {
		return fmt.Errorf("delete empty qrcode orders: %w", err)
	}

	return nil
}

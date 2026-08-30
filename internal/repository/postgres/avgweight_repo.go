package postgres

import (
	"context"
	"fmt"

	"warehouseHelper/internal/avgweight"
)

// InsertReceivedWeights пишет веса принятых единиц (граммы) одной
// транзакцией: ошибка в любой строке откатывает всю партию.
func (pg *PGClient) InsertReceivedWeights(ctx context.Context, rows []avgweight.WeightRow) error {
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

// AverageWeightGrams — средний вес товара по оставшимся строкам, граммы
// (AVG по FIFO-окну, которое оставил TrimReceivedWeights).
func (pg *PGClient) AverageWeightGrams(ctx context.Context, productID string) (float64, error) {
	var avg float64
	if err := pg.Pool.QueryRow(ctx, `
        SELECT COALESCE(AVG(weight), 0)
        FROM product_received_weights
        WHERE product_id = $1
    `, productID).Scan(&avg); err != nil {
		return 0, fmt.Errorf("average weight %s: %w", productID, err)
	}

	return avg, nil
}

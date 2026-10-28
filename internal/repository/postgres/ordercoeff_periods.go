package postgres

import (
	"context"
	"fmt"
	"time"

	"warehouseHelper/internal/ordercoeff"
)

// PeriodsBatch — строки событий периода по списку товаров и интервалам:
// коэффициент и счётчики (скидка, закончился, заморозка, недоступность).
// Нужен календарю продаж: цвет ячейки = факт события, подсказка = счётчики.
func (pg *PGClient) PeriodsBatch(ctx context.Context, productIDs []string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[string]map[time.Time]ordercoeff.PeriodCoeff, error) {
	out := make(map[string]map[time.Time]ordercoeff.PeriodCoeff, len(productIDs))
	if len(intervals) == 0 || len(productIDs) == 0 {
		return out, nil
	}

	starts := make([]string, len(intervals))
	for i, s := range intervals {
		starts[i] = s.Format(time.DateOnly)
	}

	for _, batch := range idBatches(productIDs, coeffReadBatch) {
		rows, err := pg.Pool.Query(ctx, `
            SELECT product_id, period_start, coeff, sold_out, discount, frozen, unavailable
            FROM product_period_coeff
            WHERE product_id = ANY($1::text[]) AND period_type = $2 AND period_start = ANY($3::date[])`,
			batch, periodType, starts,
		)
		if err != nil {
			return nil, fmt.Errorf("periods batch: %w", err)
		}

		for rows.Next() {
			var p ordercoeff.PeriodCoeff
			if err := rows.Scan(&p.ProductID, &p.PeriodStart, &p.Coeff,
				&p.SoldOut, &p.Discount, &p.Frozen, &p.Unavailable); err != nil {
				rows.Close()
				return nil, err
			}
			if out[p.ProductID] == nil {
				out[p.ProductID] = make(map[time.Time]ordercoeff.PeriodCoeff, len(intervals))
			}
			out[p.ProductID][p.PeriodStart] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate periods batch: %w", err)
		}
	}

	return out, nil
}

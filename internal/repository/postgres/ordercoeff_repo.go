package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"warehouseHelper/internal/ordercoeff"

	"github.com/jackc/pgx/v5"
)

// ApplyCoeffEvent атомарно применяет событие к периоду товара: одна транзакция
// (строка текущего периода под FOR UPDATE + перенос/обнуление предыдущей).
// Возвращает applied=false, если откату нечего отменять (записи нет).
func (pg *PGClient) ApplyCoeffEvent(ctx context.Context, productID string, periodType ordercoeff.PeriodType, at time.Time, ev ordercoeff.EventType) (bool, error) {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	periodStart := ordercoeff.PeriodStart(periodType, at)

	cur, err := pg.coeffPeriodForUpdate(ctx, tx, productID, periodType, periodStart)
	if err != nil {
		return false, err
	}

	var prev *ordercoeff.PeriodCoeff
	if cur == nil {
		prevStart := ordercoeff.PrevPeriodStart(periodType, periodStart)
		prev, err = pg.coeffPeriodForUpdate(ctx, tx, productID, periodType, prevStart)
		if err != nil {
			return false, err
		}
	}

	newCur, newPrev, applied := ordercoeff.ApplyEvent(cur, prev, ev)
	if !applied {
		return false, nil // откату нечего отменять — ничего не пишем
	}

	if cur == nil {
		_, err = tx.Exec(ctx, `
            INSERT INTO product_period_coeff (product_id, period_type, period_start, coeff, sold_out, discount, frozen, unavailable)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			productID, periodType, periodStart, newCur.Coeff, newCur.SoldOut, newCur.Discount, newCur.Frozen, newCur.Unavailable,
		)
		if err != nil {
			return false, fmt.Errorf("insert coeff: %w", err)
		}
	} else {
		_, err = tx.Exec(ctx, `
            UPDATE product_period_coeff
            SET coeff = $4, sold_out = $5, discount = $6, frozen = $7, unavailable = $8
            WHERE product_id = $1 AND period_type = $2 AND period_start = $3`,
			productID, periodType, periodStart, newCur.Coeff, newCur.SoldOut, newCur.Discount, newCur.Frozen, newCur.Unavailable,
		)
		if err != nil {
			return false, fmt.Errorf("update coeff: %w", err)
		}
	}

	if newPrev != nil {
		_, err = tx.Exec(ctx, `
            UPDATE product_period_coeff
            SET coeff = $4
            WHERE product_id = $1 AND period_type = $2 AND period_start = $3`,
			productID, periodType, newPrev.PeriodStart, newPrev.Coeff,
		)
		if err != nil {
			return false, fmt.Errorf("zero prev coeff: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}

	return true, nil
}

// coeffPeriodForUpdate читает строку периода под блокировкой; nil, если строки нет.
func (pg *PGClient) coeffPeriodForUpdate(ctx context.Context, tx pgx.Tx, productID string, periodType ordercoeff.PeriodType, periodStart time.Time) (*ordercoeff.PeriodCoeff, error) {
	var p ordercoeff.PeriodCoeff
	err := tx.QueryRow(ctx, `
        SELECT coeff, sold_out, discount, frozen, unavailable
        FROM product_period_coeff
        WHERE product_id = $1 AND period_type = $2 AND period_start = $3
        FOR UPDATE`,
		productID, periodType, periodStart,
	).Scan(&p.Coeff, &p.SoldOut, &p.Discount, &p.Frozen, &p.Unavailable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select coeff period: %w", err)
	}
	p.ProductID = productID
	p.PeriodType = periodType
	p.PeriodStart = periodStart
	return &p, nil
}

// Coefficients — значения coeff строк календаря товара для заданных интервалов
// (нет строки — нет значения в map). Интервалы сравниваются как DATE
// (без часовых поясов: строки 'YYYY-MM-DD' кастуются в date[]).
func (pg *PGClient) Coefficients(ctx context.Context, productID string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[time.Time]int16, error) {
	if len(intervals) == 0 {
		return map[time.Time]int16{}, nil
	}

	starts := make([]string, len(intervals))
	for i, s := range intervals {
		starts[i] = s.Format(time.DateOnly)
	}

	rows, err := pg.Pool.Query(ctx, `
        SELECT period_start, coeff
        FROM product_period_coeff
        WHERE product_id = $1 AND period_type = $2 AND period_start = ANY($3::date[])`,
		productID, periodType, starts,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[time.Time]int16, len(intervals))
	for rows.Next() {
		var start time.Time
		var coeff int16
		if err := rows.Scan(&start, &coeff); err != nil {
			return nil, err
		}
		out[start] = coeff
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

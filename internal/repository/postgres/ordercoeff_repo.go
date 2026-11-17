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

// AdjustSoldOut — правка счётчика «закончился» периода at на delta (+1/−1) с
// полным пересчётом каскада coeff от этого периода вперёд.
// applied=false: править нечего (delta<0 при sold_out=0 или строки периода нет).
//
// Одна транзакция: строка периода (и предыдущая — для переноса) под FOR UPDATE,
// затем все строки периода и позже под FOR UPDATE и пересчёт RecalcChain.
// Строки period_start < at не читаются и не меняются: их coeff уже обнулён
// переносом, накопленное живёт на самом свежем конце цепочки.
func (pg *PGClient) AdjustSoldOut(ctx context.Context, productID string, periodType ordercoeff.PeriodType, at time.Time, delta int16) (bool, error) {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	periodStart := ordercoeff.PeriodStart(periodType, at)

	seed, applied, err := pg.adjustPeriodCounter(ctx, tx, productID, periodType, periodStart, delta)
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}

	if err := pg.recalcChainCoeffs(ctx, tx, productID, periodType, periodStart, seed); err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}

	return true, nil
}

// adjustPeriodCounter — правка строки периода на delta: существующая строка
// (счётчик ±1) или новая (первое событие периода, с переносом coeff предыдущей).
// Возвращает seed — coeff периода перед periodStart — для пересчёта цепочки;
// applied=false — править нечего.
func (pg *PGClient) adjustPeriodCounter(ctx context.Context, tx pgx.Tx, productID string, periodType ordercoeff.PeriodType, periodStart time.Time, delta int16) (seed int16, applied bool, err error) {
	cur, err := pg.coeffPeriodForUpdate(ctx, tx, productID, periodType, periodStart)
	if err != nil {
		return 0, false, err
	}

	if cur == nil {
		if delta < 0 {
			return 0, false, nil // строки периода нет — снимать нечего
		}

		return pg.insertSoldOutPeriod(ctx, tx, productID, periodType, periodStart)
	}

	if delta < 0 && cur.SoldOut == 0 {
		return 0, false, nil // живого «закончился» в периоде нет
	}

	seed, err = pg.prevPeriodCoeff(ctx, tx, productID, periodType, periodStart)
	if err != nil {
		return 0, false, err
	}
	// coeff здесь не трогаем: его перепишет пересчёт каскада.
	if _, err := tx.Exec(ctx, `
            UPDATE product_period_coeff
            SET sold_out = sold_out + $4
            WHERE product_id = $1 AND period_type = $2 AND period_start = $3`,
		productID, periodType, periodStart, delta,
	); err != nil {
		return 0, false, fmt.Errorf("update sold_out: %w", err)
	}

	return seed, true, nil
}

// insertSoldOutPeriod — первое событие периода: строка создаётся, coeff
// предыдущего периода переносится на неё и обнуляется у предыдущей
// (инвариант transfer). seed читается ДО обнуления — значение переносится,
// а не теряется.
func (pg *PGClient) insertSoldOutPeriod(ctx context.Context, tx pgx.Tx, productID string, periodType ordercoeff.PeriodType, periodStart time.Time) (seed int16, applied bool, err error) {
	prev, err := pg.coeffPeriodForUpdate(ctx, tx, productID, periodType, ordercoeff.PrevPeriodStart(periodType, periodStart))
	if err != nil {
		return 0, false, err
	}

	if prev != nil {
		seed = prev.Coeff
	}

	if _, err := tx.Exec(ctx, `
            INSERT INTO product_period_coeff (product_id, period_type, period_start, coeff, sold_out, discount, frozen, unavailable)
            VALUES ($1, $2, $3, 0, 1, 0, 0, 0)`,
		productID, periodType, periodStart,
	); err != nil {
		return 0, false, fmt.Errorf("insert coeff: %w", err)
	}

	if prev == nil {
		return seed, true, nil
	}

	if _, err := tx.Exec(ctx, `
            UPDATE product_period_coeff
            SET coeff = 0
            WHERE product_id = $1 AND period_type = $2 AND period_start = $3`,
		productID, periodType, prev.PeriodStart,
	); err != nil {
		return 0, false, fmt.Errorf("zero prev coeff: %w", err)
	}

	return seed, true, nil
}

// prevPeriodCoeff — coeff периода, предшествующего periodStart (0, если строки
// о нём нет): значение переносится в строку нового периода.
func (pg *PGClient) prevPeriodCoeff(ctx context.Context, tx pgx.Tx, productID string, periodType ordercoeff.PeriodType, periodStart time.Time) (int16, error) {
	prev, err := pg.coeffPeriodForUpdate(ctx, tx, productID, periodType, ordercoeff.PrevPeriodStart(periodType, periodStart))
	if err != nil {
		return 0, err
	}
	if prev == nil {
		return 0, nil
	}

	return prev.Coeff, nil
}

// recalcChainCoeffs — полный каскад: пересчёт coeff строки periodStart и всех
// последующих периодов товара и запись результата.
func (pg *PGClient) recalcChainCoeffs(ctx context.Context, tx pgx.Tx, productID string, periodType ordercoeff.PeriodType, periodStart time.Time, seed int16) error {
	rows, err := pg.coeffChainForUpdate(ctx, tx, productID, periodType, periodStart)
	if err != nil {
		return err
	}

	ordercoeff.RecalcChain(rows, seed)

	for _, row := range rows {
		if _, err := tx.Exec(ctx, `
            UPDATE product_period_coeff
            SET coeff = $4
            WHERE product_id = $1 AND period_type = $2 AND period_start = $3`,
			productID, periodType, row.PeriodStart, row.Coeff,
		); err != nil {
			return fmt.Errorf("update chain coeff: %w", err)
		}
	}

	return nil
}

// coeffChainForUpdate читает строки календаря периода from и позже (все поля
// PeriodCoeff) под блокировкой, в порядке возрастания периода.
func (pg *PGClient) coeffChainForUpdate(ctx context.Context, tx pgx.Tx, productID string, periodType ordercoeff.PeriodType, from time.Time) ([]ordercoeff.PeriodCoeff, error) {
	rows, err := tx.Query(ctx, `
        SELECT coeff, sold_out, discount, frozen, unavailable, period_start
        FROM product_period_coeff
        WHERE product_id = $1 AND period_type = $2 AND period_start >= $3
        ORDER BY period_start
        FOR UPDATE`,
		productID, periodType, from,
	)
	if err != nil {
		return nil, fmt.Errorf("select coeff chain: %w", err)
	}
	defer rows.Close()

	out := make([]ordercoeff.PeriodCoeff, 0, 8)
	for rows.Next() {
		p := ordercoeff.PeriodCoeff{ProductID: productID, PeriodType: periodType}
		if err := rows.Scan(&p.Coeff, &p.SoldOut, &p.Discount, &p.Frozen, &p.Unavailable, &p.PeriodStart); err != nil {
			return nil, fmt.Errorf("select coeff chain: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("select coeff chain: %w", err)
	}

	return out, nil
}

// coeffPeriodForUpdate читает строку периода под блокировкой; nil, если строки нет.
//
//nolint:nilnil // контракт: (nil, nil) = периода нет, переносить нечего
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

// coeffReadBatch — размер пачки id при пакетном чтении коэффициентов
// (календарь продаж: один запрос на пачку товаров вместо запроса на товар).
const coeffReadBatch = 500

// CoefficientsBatch — коэффициенты по списку товаров и интервалам:
// map[productID]map[periodStart]coeff. Нет строки — нет значения.
func (pg *PGClient) CoefficientsBatch(ctx context.Context, productIDs []string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[string]map[time.Time]int16, error) {
	out := make(map[string]map[time.Time]int16, len(productIDs))
	if len(intervals) == 0 || len(productIDs) == 0 {
		return out, nil
	}

	starts := make([]string, len(intervals))
	for i, s := range intervals {
		starts[i] = s.Format(time.DateOnly)
	}

	for _, batch := range idBatches(productIDs, coeffReadBatch) {
		rows, err := pg.Pool.Query(ctx, `
            SELECT product_id, period_start, coeff
            FROM product_period_coeff
            WHERE product_id = ANY($1::text[]) AND period_type = $2 AND period_start = ANY($3::date[])`,
			batch, periodType, starts,
		)
		if err != nil {
			return nil, fmt.Errorf("coefficients batch: %w", err)
		}

		for rows.Next() {
			var productID string
			var start time.Time
			var coeff int16
			if err := rows.Scan(&productID, &start, &coeff); err != nil {
				rows.Close()
				return nil, err
			}
			if out[productID] == nil {
				out[productID] = make(map[time.Time]int16, len(intervals))
			}
			out[productID][start] = coeff
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	return out, nil
}

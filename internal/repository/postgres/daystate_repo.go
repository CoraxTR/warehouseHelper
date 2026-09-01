package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"warehouseHelper/internal/daystate"

	"github.com/jackc/pgx/v5"
)

// EnsureDay создаёт строку дня, если её нет (ON CONFLICT DO NOTHING);
// snapshot-значения используются только при вставке — существующая строка
// (событие, календарь, снапшот) не перезаписывается.
func (pg *PGClient) EnsureDay(ctx context.Context, d daystate.DayState) error {
	if _, err := pg.Pool.Exec(ctx, `
        INSERT INTO product_day_state (product_id, date, in_stock, discount_start, discount, orderable)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (product_id, date) DO NOTHING`,
		d.ProductID, d.Date, d.InStock, d.DiscountStart, d.Discount, d.Orderable,
	); err != nil {
		return fmt.Errorf("ensure day %s %s: %w", d.ProductID, d.Date.Format(time.DateOnly), err)
	}
	return nil
}

// GetDay читает строку дня; строки нет — daystate.ErrDayNotFound.
func (pg *PGClient) GetDay(ctx context.Context, productID string, date time.Time) (*daystate.DayState, error) {
	var d daystate.DayState
	var increases []int16
	err := pg.Pool.QueryRow(ctx, `
        SELECT product_id, date, in_stock, discount_start, discount, discount_increases, orderable, sold_out_today
        FROM product_day_state
        WHERE product_id = $1 AND date = $2`,
		productID, date,
	).Scan(&d.ProductID, &d.Date, &d.InStock, &d.DiscountStart, &d.Discount, &increases, &d.Orderable, &d.SoldOutToday)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, daystate.ErrDayNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select day %s %s: %w", productID, date.Format(time.DateOnly), err)
	}
	d.DiscountIncreases = increases
	return &d, nil
}

// UpdateDay обновляет пересчитываемые поля строки дня (in_stock, discount,
// discount_increases, sold_out_today); строки нет — daystate.ErrDayNotFound.
func (pg *PGClient) UpdateDay(ctx context.Context, d daystate.DayState) error {
	tag, err := pg.Pool.Exec(ctx, `
        UPDATE product_day_state
        SET in_stock = $3, discount = $4, discount_increases = $5, sold_out_today = $6
        WHERE product_id = $1 AND date = $2`,
		d.ProductID, d.Date, d.InStock, d.Discount, d.DiscountIncreases, d.SoldOutToday,
	)
	if err != nil {
		return fmt.Errorf("update day %s %s: %w", d.ProductID, d.Date.Format(time.DateOnly), err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s %s", daystate.ErrDayNotFound, d.ProductID, d.Date.Format(time.DateOnly))
	}
	return nil
}

// SetOrderable обновляет доступность товара для набора дат одной транзакцией:
// строки создаются при необходимости (in_stock/discount остаются NULL),
// orderable перезаписывается (включая строки, созданные событиями).
func (pg *PGClient) SetOrderable(ctx context.Context, productID string, dates []time.Time, orderable bool) error {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set orderable begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, d := range dates {
		if _, err := tx.Exec(ctx, `
            INSERT INTO product_day_state (product_id, date, orderable)
            VALUES ($1, $2, $3)
            ON CONFLICT (product_id, date) DO UPDATE SET orderable = EXCLUDED.orderable`,
			productID, d, orderable,
		); err != nil {
			return fmt.Errorf("set orderable %s %s: %w", productID, d.Format(time.DateOnly), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set orderable commit: %w", err)
	}
	return nil
}

// SnapshotDone — есть ли строки за дату (маркер утреннего снапшота:
// снапшот сделан, если за день уже что-то создано — событиями или им самим).
func (pg *PGClient) SnapshotDone(ctx context.Context, date time.Time) (bool, error) {
	var ok bool
	if err := pg.Pool.QueryRow(ctx, `
        SELECT EXISTS (SELECT 1 FROM product_day_state WHERE date = $1 LIMIT 1)`,
		date,
	).Scan(&ok); err != nil {
		return false, fmt.Errorf("snapshot done %s: %w", date.Format(time.DateOnly), err)
	}
	return ok, nil
}

// SnapshotInsert делает утренний снимок за дату из product_stock одним
// запросом: по товару со строками в стоке — (в наличии, скидки). Строки,
// созданные событиями/календарём, не перезаписываются: при конфликте
// дополняются только NULL-поля (COALESCE) — снимок не трогает
// discount_increases, sold_out_today и orderable.
func (pg *PGClient) SnapshotInsert(ctx context.Context, date time.Time) error {
	if _, err := pg.Pool.Exec(ctx, `
        INSERT INTO product_day_state (product_id, date, in_stock, discount_start, discount)
        SELECT product_id, $1::date,
               BOOL_OR(qty > 0),
               MAX(COALESCE(discount_general_manual, discount_general)),
               MAX(COALESCE(discount_general_manual, discount_general))
        FROM product_stock
        GROUP BY product_id
        ON CONFLICT (product_id, date) DO UPDATE
          SET in_stock = COALESCE(product_day_state.in_stock, EXCLUDED.in_stock),
              discount_start = COALESCE(product_day_state.discount_start, EXCLUDED.discount_start),
              discount = COALESCE(product_day_state.discount, EXCLUDED.discount)`,
		date,
	); err != nil {
		return fmt.Errorf("snapshot insert %s: %w", date.Format(time.DateOnly), err)
	}
	return nil
}

// LotsSnapshot читает лоты товара из product_stock: количество и эффективную
// скидку канала general (COALESCE(manual, plain)) — срез для пересчёта дня.
func (pg *PGClient) LotsSnapshot(ctx context.Context, productID string) ([]daystate.LotState, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT qty, COALESCE(discount_general_manual, discount_general)
        FROM product_stock
        WHERE product_id = $1`,
		productID,
	)
	if err != nil {
		return nil, fmt.Errorf("lots snapshot %s: %w", productID, err)
	}
	defer rows.Close()

	lots := make([]daystate.LotState, 0, 8)
	for rows.Next() {
		var l daystate.LotState
		if err := rows.Scan(&l.Qty, &l.EffectiveGeneral); err != nil {
			return nil, fmt.Errorf("lots snapshot %s: %w", productID, err)
		}
		lots = append(lots, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lots snapshot %s: %w", productID, err)
	}
	return lots, nil
}

// ClearSoldOut сбрасывает маркер «закончилась» строки дня; строки нет —
// не ошибка (нечего сбрасывать, откат — идемпотентное действие).
func (pg *PGClient) ClearSoldOut(ctx context.Context, productID string, date time.Time) error {
	if _, err := pg.Pool.Exec(ctx, `
        UPDATE product_day_state SET sold_out_today = false
        WHERE product_id = $1 AND date = $2`,
		productID, date,
	); err != nil {
		return fmt.Errorf("clear sold out %s %s: %w", productID, date.Format(time.DateOnly), err)
	}
	return nil
}

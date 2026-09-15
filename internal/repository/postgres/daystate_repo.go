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

// LastKnownInStock читает последнее известное состояние наличия товара до даты
// before (строго раньше): ближайшая по дате строка дня с заполненным in_stock.
// Строки с in_stock NULL (календарь «Доступность») пропускаются, строки
// будущих дат не рассматриваются. Истории нет — nil, nil: у товара в полном
// отсутствии строк в таблице нет вовсе (лот, списанный до нуля, удаляется,
// снапшот дня такого товара не видит). Устаревшая последняя строка (обнуление
// не наблюдалось контуром) даёт «в наличии» для фактически отсутствующего
// товара — переход «не было → появилось» тогда не детектируется.
//
//nolint:nilnil // контракт репозитория: (nil, nil) = истории нет
func (pg *PGClient) LastKnownInStock(ctx context.Context, productID string, before time.Time) (*bool, error) {
	var inStock bool
	err := pg.Pool.QueryRow(ctx, `
        SELECT in_stock
        FROM product_day_state
        WHERE product_id = $1 AND date < $2 AND in_stock IS NOT NULL
        ORDER BY date DESC
        LIMIT 1`,
		productID, before,
	).Scan(&inStock)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last known in stock %s %s: %w", productID, before.Format(time.DateOnly), err)
	}

	return &inStock, nil
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
// Effective скидка (решение владельца, сентябрь 2026):
// COALESCE(manual, NULLIF(plain,0)) — ручная перекрывает «просто» как есть,
// включая заданный 0 («скидка 0 %», лестница по паре заморожена); ноль в
// plain-колонке — legacy-«скидки нет» и лестницу не перекрывает.
//
// Запрет менеджера (решение владельца, 15.09.2026): ручная 0 блокирует все
// сроки ДАЛЬШЕ того, на который поставлена, — пары товара с годностью не раньше
// ban_from (самый близкий срок среди пар с ручной 0) в скидку дня дают 0.
func (pg *PGClient) SnapshotInsert(ctx context.Context, date time.Time) error {
	if _, err := pg.Pool.Exec(ctx, `
        WITH lots AS (
            SELECT product_id, qty, best_before,
                   COALESCE(discount_general_manual, NULLIF(discount_general, 0)) AS eff,
                   MIN(best_before) FILTER (
                       WHERE discount_general_manual = 0 OR discount_telegram_manual = 0
                   ) OVER (PARTITION BY product_id) AS ban_from
            FROM product_stock
        )
        INSERT INTO product_day_state (product_id, date, in_stock, discount_start, discount)
        SELECT product_id, $1::date,
               BOOL_OR(qty > 0),
               MAX(CASE WHEN ban_from IS NOT NULL AND best_before >= ban_from THEN 0 ELSE eff END),
               MAX(CASE WHEN ban_from IS NOT NULL AND best_before >= ban_from THEN 0 ELSE eff END)
        FROM lots
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
// скидку канала general — COALESCE(manual, NULLIF(plain,0)): заданная ручная
// (в том числе 0 — «скидка 0 %», пара заморожена) перекрывает «просто» как
// есть, а ноль в plain-колонке значит «скидки нет» (решение владельца,
// сентябрь 2026 — было «0 = NULL» для обеих колонок, 14.09.2026).
func (pg *PGClient) LotsSnapshot(ctx context.Context, productID string) ([]daystate.LotState, error) {
	rows, err := pg.Pool.Query(ctx, `
        WITH ban AS (
            SELECT MIN(best_before) AS ban_from
            FROM product_stock
            WHERE product_id = $1
              AND (discount_general_manual = 0 OR discount_telegram_manual = 0)
        )
        SELECT ps.qty,
               CASE WHEN b.ban_from IS NOT NULL AND ps.best_before >= b.ban_from THEN 0
                    ELSE COALESCE(ps.discount_general_manual, NULLIF(ps.discount_general, 0)) END
        FROM product_stock ps
        CROSS JOIN ban b
        WHERE ps.product_id = $1`,
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

// ListByRange читает строки за период [from, to]: map product_id → map date →
// строка. Период — календарный месяц (календарь доступности, отчёт по наличию).
func (pg *PGClient) ListByRange(ctx context.Context, from, to time.Time) (map[string]map[time.Time]daystate.DayState, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT product_id, date, in_stock, discount_start, discount, discount_increases, orderable, sold_out_today
        FROM product_day_state
        WHERE date >= $1 AND date <= $2
        ORDER BY product_id, date`,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("list days %s..%s: %w", from.Format(time.DateOnly), to.Format(time.DateOnly), err)
	}
	defer rows.Close()

	out := map[string]map[time.Time]daystate.DayState{}
	for rows.Next() {
		var d daystate.DayState
		var increases []int16
		if err := rows.Scan(&d.ProductID, &d.Date, &d.InStock, &d.DiscountStart, &d.Discount, &increases, &d.Orderable, &d.SoldOutToday); err != nil {
			return nil, fmt.Errorf("list days scan: %w", err)
		}
		d.DiscountIncreases = increases
		byDate := out[d.ProductID]
		if byDate == nil {
			byDate = map[time.Time]daystate.DayState{}
			out[d.ProductID] = byDate
		}
		byDate[d.Date] = d
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list days: %w", err)
	}
	return out, nil
}

package postgres

import (
	"context"
	"fmt"
	"time"

	"warehouseHelper/internal/averagesales"
)

// turnoverReadBatch — размер пачки id при чтении окна оборотов списком товаров:
// один запрос на пачку (ANY($1)) вместо запроса на товар.
const turnoverReadBatch = 500

// UpsertMonthlyTurnover пишет месячные обороты батчем (upsert по PK).
// Одной транзакцией: ошибка любой строки откатывает всю партию.
func (pg *PGClient) UpsertMonthlyTurnover(ctx context.Context, rows []averagesales.TurnoverRow) error {
	return pg.upsertTurnover(ctx, "product_monthly_turnover", "month_start", rows)
}

// UpsertWeeklyTurnover пишет недельные обороты батчем.
func (pg *PGClient) UpsertWeeklyTurnover(ctx context.Context, rows []averagesales.TurnoverRow) error {
	return pg.upsertTurnover(ctx, "product_weekly_turnover", "week_start", rows)
}

// upsertTurnover — общий батч-апсёрт оборотов (таблица и колонка периода свои).
func (pg *PGClient) upsertTurnover(ctx context.Context, table, periodColumn string, rows []averagesales.TurnoverRow) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	stmt := fmt.Sprintf(`
        INSERT INTO %s (product_id, %s, qty)
        VALUES ($1, $2, $3)
        ON CONFLICT (product_id, %s) DO UPDATE SET qty = EXCLUDED.qty`,
		table, periodColumn, periodColumn,
	)

	for _, r := range rows {
		if _, err := tx.Exec(ctx, stmt, r.ProductID, r.PeriodStart, r.Qty); err != nil {
			return fmt.Errorf("upsert %s: %w", table, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// LastMonthlyTurnover — последние n строк месячного оборота товара
// (по убыванию period_start; самые свежие — первые).
func (pg *PGClient) LastMonthlyTurnover(ctx context.Context, productID string, n int) ([]averagesales.TurnoverRow, error) {
	return pg.lastTurnover(ctx, "product_monthly_turnover", "month_start", productID, n)
}

// LastWeeklyTurnover — последние n строк недельного оборота товара.
func (pg *PGClient) LastWeeklyTurnover(ctx context.Context, productID string, n int) ([]averagesales.TurnoverRow, error) {
	return pg.lastTurnover(ctx, "product_weekly_turnover", "week_start", productID, n)
}

// lastTurnover — общее чтение окна оборотов товара.
func (pg *PGClient) lastTurnover(ctx context.Context, table, periodColumn, productID string, n int) ([]averagesales.TurnoverRow, error) {
	rows, err := pg.Pool.Query(ctx, fmt.Sprintf(`
        SELECT product_id, %s, qty
        FROM %s
        WHERE product_id = $1
        ORDER BY %s DESC
        LIMIT $2`, periodColumn, table, periodColumn),
		productID, n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]averagesales.TurnoverRow, 0, n)
	for rows.Next() {
		var r averagesales.TurnoverRow
		if err := rows.Scan(&r.ProductID, &r.PeriodStart, &r.Qty); err != nil {
			return nil, err
		}
		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// turnoverWindow — общее чтение окна оборотов товаров: id бьются пачками по
// turnoverReadBatch, каждый запрос берёт пачку через ANY($1). since включается
// в выборку и сравнивается как DATE (без часовых поясов: строка 'YYYY-MM-DD'
// кастуется в date). Порядок строк — товар по возрастанию, периоды по убыванию.
func (pg *PGClient) turnoverWindow(ctx context.Context, table, periodColumn string, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error) {
	out := make([]averagesales.TurnoverRow, 0, len(productIDs))
	for _, batch := range idBatches(productIDs, turnoverReadBatch) {
		rows, err := pg.turnoverWindowBatch(ctx, table, periodColumn, batch, since)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}

	return out, nil
}

// turnoverWindowBatch — один запрос окна по пачке товаров.
func (pg *PGClient) turnoverWindowBatch(ctx context.Context, table, periodColumn string, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error) {
	rows, err := pg.Pool.Query(ctx, fmt.Sprintf(`
        SELECT product_id, %s, qty
        FROM %s
        WHERE product_id = ANY($1::text[]) AND %s >= $2::date
        ORDER BY product_id, %s DESC`, periodColumn, table, periodColumn, periodColumn),
		productIDs, since.Format(time.DateOnly),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]averagesales.TurnoverRow, 0, len(productIDs))
	for rows.Next() {
		var r averagesales.TurnoverRow
		if err := rows.Scan(&r.ProductID, &r.PeriodStart, &r.Qty); err != nil {
			return nil, err
		}
		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// MonthlyTurnoverWindowByProducts — строки месячного оборота перечисленных
// товаров за периоды не раньше since, пачками по turnoverReadBatch id (1000
// товаров = 2 запроса). Порядок — как у lastTurnover: периоды по убыванию.
func (pg *PGClient) MonthlyTurnoverWindowByProducts(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error) {
	return pg.turnoverWindow(ctx, "product_monthly_turnover", "month_start", productIDs, since)
}

// WeeklyTurnoverWindowByProducts — то же для недельного ряда.
func (pg *PGClient) WeeklyTurnoverWindowByProducts(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error) {
	return pg.turnoverWindow(ctx, "product_weekly_turnover", "week_start", productIDs, since)
}

// idBatches — id пачками не больше size (порядок сохраняется): пагинация
// чтения списка товаров батчем, а не товаром за запрос.
func idBatches(ids []string, size int) [][]string {
	if size <= 0 {
		size = 1
	}

	out := make([][]string, 0, (len(ids)+size-1)/size)
	for len(ids) > 0 {
		end := min(size, len(ids))
		out = append(out, ids[:end])
		ids = ids[end:]
	}

	return out
}

// ProductsMissingMonthlyTurnover — id товаров, у которых в окне последних
// завершённых месяцев (starts, YYYY-MM-DD) есть дыры: нет строки хотя бы за
// один период окна (стартовая дозаливка; порядок не важен).
func (pg *PGClient) ProductsMissingMonthlyTurnover(ctx context.Context, starts []string) ([]string, error) {
	return pg.productsMissingTurnover(ctx, "product_monthly_turnover", "month_start", starts)
}

// ProductsMissingWeeklyTurnover — то же для недельного окна.
func (pg *PGClient) ProductsMissingWeeklyTurnover(ctx context.Context, starts []string) ([]string, error) {
	return pg.productsMissingTurnover(ctx, "product_weekly_turnover", "week_start", starts)
}

// productsMissingTurnover — общий запрос: товары каталога без строки хотя бы за
// один период окна. starts — даты начал периодов; сравниваются как DATE
// (без часовых поясов: строки 'YYYY-MM-DD' кастуются в date[]).
func (pg *PGClient) productsMissingTurnover(ctx context.Context, table, periodColumn string, starts []string) ([]string, error) {
	rows, err := pg.Pool.Query(ctx, fmt.Sprintf(`
        SELECT DISTINCT p.id
        FROM products p
        WHERE NOT EXISTS (
            SELECT 1 FROM %s t
            WHERE t.product_id = p.id AND t.%s = ANY($1::date[])
        )`, table, periodColumn), starts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return ids, nil
}

// HasMonthlyTurnover — есть ли у товара хоть одна строка месячного оборота
// (идемпотентность бэкфилла: уже заполненные товары не перезаполняем).
func (pg *PGClient) HasMonthlyTurnover(ctx context.Context, productID string) (bool, error) {
	return pg.hasTurnover(ctx, "product_monthly_turnover", productID)
}

// HasWeeklyTurnover — есть ли у товара хоть одна строка недельного оборота.
func (pg *PGClient) HasWeeklyTurnover(ctx context.Context, productID string) (bool, error) {
	return pg.hasTurnover(ctx, "product_weekly_turnover", productID)
}

func (pg *PGClient) hasTurnover(ctx context.Context, table, productID string) (bool, error) {
	var exists bool
	err := pg.Pool.QueryRow(ctx, fmt.Sprintf(`
        SELECT EXISTS (
            SELECT 1 FROM %s WHERE product_id = $1
        )`, table), productID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

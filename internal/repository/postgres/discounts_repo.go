package postgres

import (
	"context"
	"fmt"
	"time"

	"warehouseHelper/internal/discounts"

	"github.com/jackc/pgx/v5"
)

// discountInputColumns — колонки снапшота входа расчёта; порядок обязан
// совпадать с порядком Scan в scanDiscountInput.
const discountInputColumns = `
    s.product_id, p.name, p.group_name, p.short_list, p.shelf_life, p.track_weekly,
    s.best_before, s.qty, COALESCE(w.qty, m.qty)`

// discountInputQuery — снапшот целиком: лоты стока с товарными признаками и
// оборотом последнего ЗАВЕРШЁННОГО периода. Ряды оборотов содержат и текущий
// незакрытый период (неделю/месяц), поэтому периоды ограничены строго раньше
// начала текущей недели (Postgres: неделя с понедельника) и текущего месяца;
// дата сравнивается с датой (date_trunc … ::date), не со временем. Вчера/сегодня
// приходят параметром $1 (сегодня) — «сейчас» внутри SQL не берём, чтобы выбор
// периода был тестируемым.
// Период выбирается по признаку товара, а не по наличию данных: недельному
// товару месячный ряд не подставляется (и наоборот), период оборота — часть
// товарного признака (DailyRate: v = оборот / 7 или 30).
const discountInputQuery = `
    SELECT ` + discountInputColumns + `
    FROM product_stock s
    JOIN products p ON p.id = s.product_id
    LEFT JOIN LATERAL (
        SELECT w.qty
        FROM product_weekly_turnover w
        WHERE p.track_weekly
          AND w.product_id = s.product_id
          AND w.week_start < date_trunc('week', $1::date)::date
        ORDER BY w.week_start DESC
        LIMIT 1
    ) w ON true
    LEFT JOIN LATERAL (
        SELECT m.qty
        FROM product_monthly_turnover m
        WHERE NOT p.track_weekly
          AND m.product_id = s.product_id
          AND m.month_start < date_trunc('month', $1::date)::date
        ORDER BY m.month_start DESC
        LIMIT 1
    ) m ON true
    ORDER BY s.product_id, s.best_before`

// Дни периода оборота — Input.PeriodDays: недельный ряд 7 дней, месячный 30.
const (
	weeklyPeriodDays  = 7
	monthlyPeriodDays = 30
)

// LoadDiscountInput читает вход матчинга формул: все лоты product_stock с
// товарными признаками из products и оборотом последнего завершённого периода
// (недельного для track_weekly, месячного для остальных).
//
// today — локальная дата склада, задающая границу незакрытого периода; она
// параметр, а не «сейчас», чтобы расчёт был тестируемым.
//
// Оборот — в штуках (весовые пересчитаны по average_weight в модуле оборота),
// конвертации здесь нет. Отрицательный оборот (возвраты задним числом) отдаём
// как есть — чинит не репозиторий. Нет данных о завершённом периоде — Turnover
// nil и PeriodDays 0 (нет данных о скорости продаж).
// Фильтров по количеству нет: обнулённые лоты сток удаляет сам (DELETE в
// ReplaceStockLots), а что делать с нулём остатка, решает расчёт.
func (pg *PGClient) LoadDiscountInput(ctx context.Context, today time.Time) ([]discounts.Input, error) {
	rows, err := pg.Pool.Query(ctx, discountInputQuery, today)
	if err != nil {
		return nil, fmt.Errorf("load discount input %s: %w", today.Format(time.DateOnly), err)
	}
	defer rows.Close()

	inputs := make([]discounts.Input, 0, 256)
	for rows.Next() {
		in, err := scanDiscountInput(rows)
		if err != nil {
			return nil, fmt.Errorf("load discount input scan: %w", err)
		}
		inputs = append(inputs, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load discount input %s: %w", today.Format(time.DateOnly), err)
	}
	return inputs, nil
}

// scanDiscountInput сканирует строку снапшота в discounts.Input (порядок
// discountInputColumns). Оборот есть ⇔ колонка оборота не NULL; дни периода
// задаёт track_weekly (7 или 30), а не наличие данных.
func scanDiscountInput(row pgx.Row) (discounts.Input, error) {
	var in discounts.Input
	if err := row.Scan(
		&in.ProductID, &in.Name, &in.GroupName, &in.ShortList, &in.ShelfLife, &in.TrackWeekly,
		&in.BestBefore, &in.Qty, &in.Turnover,
	); err != nil {
		return discounts.Input{}, fmt.Errorf("scan discount input: %w", err)
	}

	if in.Turnover != nil {
		in.PeriodDays = monthlyPeriodDays
		if in.TrackWeekly {
			in.PeriodDays = weeklyPeriodDays
		}
	}
	return in, nil
}

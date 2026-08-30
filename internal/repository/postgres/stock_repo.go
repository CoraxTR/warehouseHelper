package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/stock"
)

// stockProductColumns — колонки JOIN products + product_stock в порядке SELECT.
const stockProductColumns = `
    p.id, p.internal_code, p.name, p.group_name, p.short_list,
    ps.best_before, ps.qty, ps.produced_on,
    ps.discount_general, ps.discount_telegram,
    ps.discount_general_manual, ps.discount_telegram_manual`

// LoadAllStock возвращает все лоты остатков с данными каталога, отсортированные
// по (group_name, name, best_before). Используется для прогрева кэша модуля
// «Сроки» при старте — один запрос на всё.
func (pg *PGClient) LoadAllStock(ctx context.Context) ([]stock.Product, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+stockProductColumns+`
        FROM product_stock ps
        JOIN products p ON p.id = ps.product_id
        ORDER BY lower(p.group_name), lower(p.name), ps.best_before`)
	if err != nil {
		return nil, fmt.Errorf("load all stock: %w", err)
	}
	defer rows.Close()

	var (
		products []stock.Product
		byID     = map[string]int{}
	)
	for rows.Next() {
		var (
			pID, internalCode, name, groupName string
			shortList                          bool
			bestBefore                         time.Time
			qty                                int64
			producedOn                         *time.Time
			general, telegram                  *int16
			generalManual, telegramManual      *int16
		)
		if err := rows.Scan(
			&pID, &internalCode, &name, &groupName, &shortList,
			&bestBefore, &qty, &producedOn,
			&general, &telegram, &generalManual, &telegramManual,
		); err != nil {
			return nil, fmt.Errorf("scan stock row: %w", err)
		}

		i, ok := byID[pID]
		if !ok {
			i = len(products)
			byID[pID] = i
			products = append(products, stock.Product{
				ID:           pID,
				InternalCode: internalCode,
				Name:         name,
				GroupName:    groupName,
				ShortList:    shortList,
			})
		}
		products[i].Lots = append(products[i].Lots, stock.Lot{
			BestBefore:     bestBefore,
			Qty:            qty,
			ProducedOn:     producedOn,
			General:        general,
			Telegram:       telegram,
			GeneralManual:  generalManual,
			TelegramManual: telegramManual,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load all stock rows: %w", err)
	}

	return products, nil
}

// SetManualDiscount обновляет ручные скидки лота по PK (product_id, best_before).
// Строки нет — stock.ErrLotNotFound.
func (pg *PGClient) SetManualDiscount(ctx context.Context, productID string, bestBefore time.Time, generalManual, telegramManual *int16) error {
	tag, err := pg.Pool.Exec(ctx, `
        UPDATE product_stock
        SET discount_general_manual = $3, discount_telegram_manual = $4
        WHERE product_id = $1 AND best_before = $2`,
		productID, bestBefore, generalManual, telegramManual,
	)
	if err != nil {
		return fmt.Errorf("set manual discount (%s, %s): %w", productID, bestBefore.Format(time.DateOnly), err)
	}
	if tag.RowsAffected() == 0 {
		return stock.ErrLotNotFound
	}

	return nil
}

// catalogProductColumns — колонки товара каталога для сканов «Обновить сроки».
const catalogProductColumns = `id, internal_code, name, group_name, short_list`

// LoadProductsByCodes возвращает товары каталога по internal_code (включая
// товары без остатков) — карта code → товар. Используется валидацией сканов
// страницы «Обновить сроки».
func (pg *PGClient) LoadProductsByCodes(ctx context.Context, codes []string) (map[string]stock.Product, error) {
	if len(codes) == 0 {
		return map[string]stock.Product{}, nil
	}
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+catalogProductColumns+`
        FROM products
        WHERE internal_code = ANY($1)`, codes)
	if err != nil {
		return nil, fmt.Errorf("load products by codes: %w", err)
	}
	defer rows.Close()

	out := make(map[string]stock.Product, len(codes))
	for rows.Next() {
		var p stock.Product
		if err := rows.Scan(&p.ID, &p.InternalCode, &p.Name, &p.GroupName, &p.ShortList); err != nil {
			return nil, fmt.Errorf("scan product by code: %w", err)
		}
		out[p.InternalCode] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load products by codes rows: %w", err)
	}

	return out, nil
}

// LoadProductByID возвращает товар каталога по id; строки нет — stock.ErrProductNotFound.
func (pg *PGClient) LoadProductByID(ctx context.Context, productID string) (stock.Product, error) {
	var p stock.Product
	err := pg.Pool.QueryRow(ctx, `
        SELECT `+catalogProductColumns+`
        FROM products
        WHERE id = $1`, productID,
	).Scan(&p.ID, &p.InternalCode, &p.Name, &p.GroupName, &p.ShortList)
	if errors.Is(err, pgx.ErrNoRows) {
		return stock.Product{}, fmt.Errorf("%w: %s", stock.ErrProductNotFound, productID)
	}
	if err != nil {
		return stock.Product{}, fmt.Errorf("load product by id: %w", err)
	}

	return p, nil
}

// LoadGroupNameByCode возвращает название первой группы с кодом
// internal_code[1:4]; пустая строка — группы с таким кодом нет.
func (pg *PGClient) LoadGroupNameByCode(ctx context.Context, groupCode string) (string, error) {
	var name string
	err := pg.Pool.QueryRow(ctx, `
        SELECT group_name
        FROM products
        WHERE group_name <> '' AND substr(internal_code, 2, 3) = $1
        LIMIT 1`, groupCode,
	).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load group name by code: %w", err)
	}

	return name, nil
}

// ReplaceStockLots применяет замену остатков товаров в одной транзакции:
// лоты из Upserts апсертятся (qty = целевое значение, produced_on =
// COALESCE(существующего, нового), ручные скидки — целевые, «просто»-скидки
// не трогаются), лоты из Deletes удаляются.
func (pg *PGClient) ReplaceStockLots(ctx context.Context, writes []stock.ProductWrite) error {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("replace stock begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после Commit — no-op

	for _, w := range writes {
		for _, bb := range w.Deletes {
			if _, err := tx.Exec(ctx, `
                DELETE FROM product_stock
                WHERE product_id = $1 AND best_before = $2`,
				w.ProductID, bb,
			); err != nil {
				return fmt.Errorf("replace stock delete (%s, %s): %w", w.ProductID, bb.Format(time.DateOnly), err)
			}
		}
		for _, u := range w.Upserts {
			if _, err := tx.Exec(ctx, `
                INSERT INTO product_stock
                    (product_id, best_before, qty, produced_on, discount_general_manual, discount_telegram_manual)
                VALUES ($1, $2, $3, $4, $5, $6)
                ON CONFLICT (product_id, best_before) DO UPDATE SET
                    qty = EXCLUDED.qty,
                    produced_on = COALESCE(product_stock.produced_on, EXCLUDED.produced_on),
                    discount_general_manual = EXCLUDED.discount_general_manual,
                    discount_telegram_manual = EXCLUDED.discount_telegram_manual`,
				w.ProductID, u.BestBefore, u.Qty, u.ProducedOn, u.GeneralManual, u.TelegramManual,
			); err != nil {
				return fmt.Errorf("replace stock upsert (%s, %s): %w", w.ProductID, u.BestBefore.Format(time.DateOnly), err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("replace stock commit: %w", err)
	}

	return nil
}

// AcceptStockLots добавляет принятые модулем приёмки партии в одной
// транзакции: существующий срок увеличивается (qty +=), новый — новая
// строка; produced_on — COALESCE (известная дата не затирается).
func (pg *PGClient) AcceptStockLots(ctx context.Context, lots []stock.LotIn) error {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("accept stock begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после Commit — no-op

	for _, l := range lots {
		if _, err := tx.Exec(ctx, `
            INSERT INTO product_stock (product_id, best_before, qty, produced_on)
            VALUES ($1, $2, $3, $4)
            ON CONFLICT (product_id, best_before) DO UPDATE SET
                qty = product_stock.qty + EXCLUDED.qty,
                produced_on = COALESCE(product_stock.produced_on, EXCLUDED.produced_on)`,
			l.ProductID, l.BestBefore, l.Qty, l.ProducedOn,
		); err != nil {
			return fmt.Errorf("accept stock upsert (%s, %s): %w", l.ProductID, l.BestBefore.Format(time.DateOnly), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("accept stock commit: %w", err)
	}

	return nil
}

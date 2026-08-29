package postgres

import (
	"context"
	"fmt"
	"time"

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

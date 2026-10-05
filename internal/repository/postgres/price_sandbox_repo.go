package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/domain"
)

// priceSandboxColumns — колонки product_price_sandbox в порядке SELECT/INSERT;
// порядок обязан совпадать с порядком Scan в scanPriceSandbox (иначе рассинхрон
// молчит — как у productColumns).
const priceSandboxColumns = `product_id, sale_price, buy_price, effective_vat, vat_incoming`

// scanPriceSandbox сканирует строку в domain.PriceSandbox (порядок
// priceSandboxColumns). Все четыре значения nullable → читаем через
// *int64/*int16: pgx не кладёт NULL в неуказатель, а NULL здесь значит
// «поле песочницы не заполнено».
func scanPriceSandbox(row pgx.Row) (domain.PriceSandbox, error) {
	var (
		s                   domain.PriceSandbox
		salePrice, buyPrice *int64
		effectiveVAT        *int16
		vatIncoming         *int16
	)
	if err := row.Scan(
		&s.ProductID, &salePrice, &buyPrice, &effectiveVAT, &vatIncoming,
	); err != nil {
		return domain.PriceSandbox{}, err
	}
	s.SalePrice = salePrice
	s.BuyPrice = buyPrice
	s.EffectiveVat = effectiveVAT
	s.VATIncoming = vatIncoming

	return s, nil
}

// LoadPriceSandboxes — снапшоты песочницы страницы «Проверка цен» по всем
// товарам сразу (ключ — product_id). Пустая таблица → пустая карта, не ошибка.
func (pg *PGClient) LoadPriceSandboxes(ctx context.Context) (map[string]domain.PriceSandbox, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+priceSandboxColumns+`
        FROM product_price_sandbox`)
	if err != nil {
		return nil, fmt.Errorf("load price sandboxes: %w", err)
	}
	defer rows.Close()

	boxes := make(map[string]domain.PriceSandbox)
	for rows.Next() {
		s, err := scanPriceSandbox(rows)
		if err != nil {
			return nil, err
		}
		boxes[s.ProductID] = s
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return boxes, nil
}

// UpsertPriceSandbox сохраняет снапшот песочницы товара (перезапись по
// product_id): страница пишет «фантазийные» значения целиком, поэтому прежние
// значения колонок заменяются как есть, включая NULL.
func (pg *PGClient) UpsertPriceSandbox(ctx context.Context, s domain.PriceSandbox) error {
	if _, err := pg.Pool.Exec(ctx, `
        INSERT INTO product_price_sandbox (`+priceSandboxColumns+`)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (product_id) DO UPDATE SET
            sale_price    = EXCLUDED.sale_price,
            buy_price     = EXCLUDED.buy_price,
            effective_vat = EXCLUDED.effective_vat,
            vat_incoming  = EXCLUDED.vat_incoming
    `, s.ProductID, s.SalePrice, s.BuyPrice, s.EffectiveVat, s.VATIncoming); err != nil {
		return fmt.Errorf("upsert price sandbox %s: %w", s.ProductID, err)
	}

	return nil
}

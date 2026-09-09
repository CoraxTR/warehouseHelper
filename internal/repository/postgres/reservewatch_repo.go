package postgres

import (
	"context"
	"fmt"

	"warehouseHelper/internal/reservewatch"
)

// Модуль «Контроль резервов заказов» (internal/reservewatch): методы на общем
// PGClient с префиксом ReserveWatch — таблица reserve_notices (активные
// уведомления о проблемах резерва) и каталог-шов (чтение products).
// Строка живёт, пока проблема активна; вставляется только после успешной
// отправки сообщения в чат склада.

// ListActiveReserveNotices — активные уведомления (все строки таблицы:
// закрытие — удаление, «done»-статуса нет).
func (pg *PGClient) ListActiveReserveNotices(ctx context.Context) ([]reservewatch.Notice, error) {
	rows, err := pg.Pool.Query(ctx, `SELECT order_id, kind, message_id FROM reserve_notices`)
	if err != nil {
		return nil, fmt.Errorf("reservewatch list notices: %w", err)
	}
	defer rows.Close()

	notices := make([]reservewatch.Notice, 0)
	for rows.Next() {
		var (
			n    reservewatch.Notice
			kind string
		)
		if err := rows.Scan(&n.OrderID, &kind, &n.MessageID); err != nil {
			return nil, fmt.Errorf("reservewatch list notices scan: %w", err)
		}
		n.Kind = reservewatch.Kind(kind)
		notices = append(notices, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reservewatch list notices: %w", err)
	}
	return notices, nil
}

// InsertReserveNotice — запись активного уведомления (дедуп по PK
// order_id + kind: повторно не шлём, пока проблема не исчезла и не вернулась).
// ok=false — проблема уже отслеживается.
func (pg *PGClient) InsertReserveNotice(ctx context.Context, n reservewatch.Notice) (bool, error) {
	tag, err := pg.Pool.Exec(ctx,
		`INSERT INTO reserve_notices (order_id, kind, message_id) VALUES ($1, $2, $3)
		 ON CONFLICT (order_id, kind) DO NOTHING`,
		n.OrderID, string(n.Kind), n.MessageID,
	)
	if err != nil {
		return false, fmt.Errorf("reservewatch insert notice: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteReserveNotice — проблема исчезла: убрать запись. Строки нет —
// reservewatch.ErrNoActiveProblem (повторный тик после рестарта).
func (pg *PGClient) DeleteReserveNotice(ctx context.Context, orderID string, kind reservewatch.Kind) error {
	tag, err := pg.Pool.Exec(ctx,
		`DELETE FROM reserve_notices WHERE order_id = $1 AND kind = $2`,
		orderID, string(kind),
	)
	if err != nil {
		return fmt.Errorf("reservewatch delete notice: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("reservewatch delete notice %s/%s: %w", orderID, kind, reservewatch.ErrNoActiveProblem)
	}
	return nil
}

// ReserveWatchProductsByMSIDs — каталог-шов «Контроля резервов»: тип учёта
// и код склада товаров по uuid МС (чтение products — владелец записи каталог).
// Товаров нет в каталоге — их просто нет в мапе (в проверку не идут:
// internal_code неизвестен).
func (pg *PGClient) ReserveWatchProductsByMSIDs(ctx context.Context, ids []string) (map[string]reservewatch.CatalogProduct, error) {
	out := make(map[string]reservewatch.CatalogProduct, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	rows, err := pg.Pool.Query(ctx,
		`SELECT id, internal_code, uom FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("reservewatch catalog products: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cp   reservewatch.CatalogProduct
			code *string // NULL — товар без кода (в проверку не идёт)
			uom  string
		)
		if err := rows.Scan(&cp.ProductID, &code, &uom); err != nil {
			return nil, fmt.Errorf("reservewatch catalog products scan: %w", err)
		}
		if code != nil {
			cp.InternalCode = *code
		}
		cp.Weighted = weightedUOM(uom) // весовой: кг/г/т (комментарий products_schema)
		out[cp.ProductID] = cp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reservewatch catalog products: %w", err)
	}
	return out, nil
}

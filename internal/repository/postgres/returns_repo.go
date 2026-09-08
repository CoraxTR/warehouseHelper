package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/returns"
)

// Модуль «Возврат в продажу»: методы на общем PGClient (имена без префикса —
// это шов модуля: интерфейсы usecase возврата объявлены на стороне потребителя).
// return_events — события аудита МС (снимков диффа нет, данные перечитываются
// из МС по id); return_cursor — курсор поллера (единственная строка id=1).

// returnEventColumns — колонки return_events в порядке SELECT/Scan.
const returnEventColumns = `id, kind, order_id, order_name, moment, status, manual_close, chat_id, message_id`

// scanReturnEvent сканирует строку return_events в returns.ReturnEvent.
func scanReturnEvent(row pgx.Row) (*returns.ReturnEvent, error) {
	var (
		ev     returns.ReturnEvent
		kind   string
		status string
	)
	if err := row.Scan(
		&ev.ID, &kind, &ev.OrderID, &ev.OrderName, &ev.Moment, &status, &ev.Manual,
		&ev.ChatID, &ev.MessageID,
	); err != nil {
		return nil, err
	}
	ev.Kind = returns.EventKind(kind)
	ev.Status = returns.EventStatus(status)
	return &ev, nil
}

// GetCursor — курсор поллера. ok=false — строки ещё нет (первый
// запуск: модуль ставит now и прошлое не сканирует).
func (pg *PGClient) GetCursor(ctx context.Context) (t time.Time, ok bool, err error) {
	err = pg.Pool.QueryRow(ctx, `SELECT last_moment FROM return_cursor WHERE id = 1`).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("returns get cursor: %w", err)
	}
	return t, true, nil
}

// SetCursor — сохранить курсор (upsert единственной строки id=1).
func (pg *PGClient) SetCursor(ctx context.Context, t time.Time) error {
	if _, err := pg.Pool.Exec(ctx,
		`INSERT INTO return_cursor (id, last_moment) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET last_moment = EXCLUDED.last_moment, updated_at = now()`,
		t,
	); err != nil {
		return fmt.Errorf("returns set cursor: %w", err)
	}
	return nil
}

// InsertEvent — вставить событие, если его ещё нет (дедуп по PK id:
// события с моментом на границе окна/после рестарта повторно не шлём).
// ok=false — событие уже отслеживается.
func (pg *PGClient) InsertEvent(ctx context.Context, ev *returns.ReturnEvent) (bool, error) {
	tag, err := pg.Pool.Exec(ctx,
		`INSERT INTO return_events (id, kind, order_id, order_name, moment)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (id) DO NOTHING`,
		ev.ID, string(ev.Kind), ev.OrderID, ev.OrderName, ev.Moment,
	)
	if err != nil {
		return false, fmt.Errorf("returns insert event %s: %w", ev.ID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkSent — сообщение отправлено в чат склада (статус new → sent).
func (pg *PGClient) MarkSent(ctx context.Context, id string, chatID, messageID int64) error {
	tag, err := pg.Pool.Exec(ctx,
		`UPDATE return_events SET status = 'sent', chat_id = $2, message_id = $3
		 WHERE id = $1 AND status = 'new'`,
		id, chatID, messageID,
	)
	if err != nil {
		return fmt.Errorf("returns mark sent %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("returns mark sent %s: %w", id, returns.ErrEventNotFound)
	}
	return nil
}

// MarkDone — возврат принят stock (manual=false) или закрыт вручную
// (manual=true): статус done; сообщение из чата после этого удаляет модуль.
func (pg *PGClient) MarkDone(ctx context.Context, id string, manual bool) error {
	tag, err := pg.Pool.Exec(ctx,
		`UPDATE return_events
		 SET status = 'done', manual_close = $2, processed_at = now()
		 WHERE id = $1 AND status <> 'done'`,
		id, manual,
	)
	if err != nil {
		return fmt.Errorf("returns mark done %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("returns mark done %s: %w", id, returns.ErrEventNotFound)
	}
	return nil
}

// GetEvent — событие по id (для страницы возврата и удаления сообщения).
func (pg *PGClient) GetEvent(ctx context.Context, id string) (*returns.ReturnEvent, error) {
	ev, err := scanReturnEvent(pg.Pool.QueryRow(ctx,
		`SELECT `+returnEventColumns+` FROM return_events WHERE id = $1`, id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("returns get event %s: %w", id, returns.ErrEventNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("returns get event %s: %w", id, err)
	}
	return ev, nil
}

// ListActive — активные события (new/sent) для повторной отправки
// после рестарта и списка на странице «Возврат в продажу», свежие сверху.
func (pg *PGClient) ListActive(ctx context.Context) ([]returns.ReturnEvent, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT `+returnEventColumns+` FROM return_events
		 WHERE status <> 'done' ORDER BY moment DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("returns list active: %w", err)
	}
	defer rows.Close()

	var events []returns.ReturnEvent
	for rows.Next() {
		ev, err := scanReturnEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("returns list active scan: %w", err)
		}
		events = append(events, *ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("returns list active: %w", err)
	}
	return events, nil
}

// ProductsByMSIDs — каталог-шов «Возврата в продажу»: тип учёта и код склада
// товаров по uuid МС (чтение products — владелец записи каталог). Товаров нет
// в каталоге — их просто нет в мапе (в возврат не идут: internal_code неизвестен).
func (pg *PGClient) ProductsByMSIDs(ctx context.Context, ids []string) (map[string]returns.CatalogProduct, error) {
	out := make(map[string]returns.CatalogProduct, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	rows, err := pg.Pool.Query(ctx,
		`SELECT id, internal_code, uom FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("returns catalog products: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cp   returns.CatalogProduct
			code *string // NULL — товар без кода (в возврат не идёт)
			uom  string
		)
		if err := rows.Scan(&cp.ProductID, &code, &uom); err != nil {
			return nil, fmt.Errorf("returns catalog products scan: %w", err)
		}
		if code != nil {
			cp.InternalCode = *code
		}
		switch strings.TrimSpace(uom) { // весовой: кг/г/т (комментарий products_schema)
		case "кг", "г", "т":
			cp.Weighted = true
		}
		out[cp.ProductID] = cp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("returns catalog products: %w", err)
	}
	return out, nil
}

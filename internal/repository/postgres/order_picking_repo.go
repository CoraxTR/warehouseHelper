package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/msorders"
)

// orderPickingColumns — колонки журнала подбора в порядке SELECT; порядок обязан
// совпадать с порядком Scan в scanPickingUnit (иначе молчаливый рассинхрон).
const orderPickingColumns = `
    order_id, position_id, internal_code, product_id, product_name,
    weighted, weight, produced_on, best_before`

// orderPickingInsertSQL — запись одной единицы подбора. product_id пишется через
// NULLIF: пустая строка домена значит «товара нет в каталоге», а колонка — FK на
// products.id, и пустая строка нарушила бы ссылку. Даты — с явным ::date: без
// приведения Postgres выводит тип параметра как timestamptz (предпочтительный тип
// категории даты), и DATE-колонка сравнивается с моментом по часовому поясу сессии.
// touched_at не пишется — берётся DEFAULT CURRENT_DATE схемы.
const orderPickingInsertSQL = `
    INSERT INTO order_picking
        (order_id, position_id, internal_code, product_id, product_name,
         weighted, weight, produced_on, best_before)
    VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8::date, $9::date)`

// orderPickingDeleteByPositionsSQL — замена журнала по позициям заказа: строки
// перечисленных позиций уходят, вместо них пишутся единицы нового подбора.
const orderPickingDeleteByPositionsSQL = `
    DELETE FROM order_picking
    WHERE order_id = $1 AND position_id = ANY($2)`

// orderPickingDeleteOrderSQL — очистка всего журнала заказа (отмена/расформирование).
const orderPickingDeleteOrderSQL = `
    DELETE FROM order_picking
    WHERE order_id = $1`

// orderPickingDeleteByProductsSQL — очистка журнала по товарам заказа: позиции уже
// удалены из заказа, из диффа аудита МС известны только uuid товаров.
const orderPickingDeleteByProductsSQL = `
    DELETE FROM order_picking
    WHERE order_id = $1 AND product_id = ANY($2)`

// orderPickingRemoveUnitsSQL — возврат единиц в «Сроки»: снимаем РОВНО $5 последних
// строк, совпавших по коду склада, сроку годности и весу (весовой кусок возвращается
// тот же, штучных может вернуться несколько). Свежие строки удаляются первыми —
// ORDER BY id DESC; лимит параметром: строк с тем же набором полей может быть больше,
// чем вернулось товара.
const orderPickingRemoveUnitsSQL = `
    DELETE FROM order_picking
    WHERE id IN (
        SELECT id FROM order_picking
        WHERE order_id = $1 AND internal_code = $2
          AND best_before = $3::date AND weight = $4::numeric
        ORDER BY id DESC
        LIMIT $5)`

// orderPickingByOrderSQL — строки журнала заказа (ответ на /sroki): единицы товара
// идут подряд (внутренний код), внутри товара — по сроку годности, внутри срока —
// по порядку записи.
const orderPickingByOrderSQL = `
    SELECT ` + orderPickingColumns + `
    FROM order_picking
    WHERE order_id = $1
    ORDER BY internal_code, best_before, id`

// orderPickingCleanupSQL — ретеншен журнала: строки, не тронутые с даты olderThan
// (номер заказа повторяется каждый год, старые записи удаляются). ::date обязателен:
// иначе DATE-колонка сравнивается с моментом по часовому поясу сессии.
const orderPickingCleanupSQL = `
    DELETE FROM order_picking
    WHERE touched_at < $1::date`

// ReplaceOrderPicking заменяет журнал по позициям заказа ОДНОЙ транзакцией: строки
// перечисленных позиций удаляются, вместо них пишутся единицы подбора. Пустой
// PositionIDs — только вставка (новая позиция), пустой Units — только удаление
// (позиция очищена); оба пусты — запрос не нужен.
func (pg *PGClient) ReplaceOrderPicking(ctx context.Context, w msorders.PickingReplace) error {
	if len(w.PositionIDs) == 0 && len(w.Units) == 0 {
		return nil
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("replace order picking begin (%s): %w", w.OrderID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после Commit — no-op

	if len(w.PositionIDs) > 0 {
		if _, err := tx.Exec(ctx, orderPickingDeleteByPositionsSQL, w.OrderID, w.PositionIDs); err != nil {
			return fmt.Errorf("replace order picking delete (%s): %w", w.OrderID, err)
		}
	}

	if err := insertOrderPickingUnits(ctx, tx, w.Units); err != nil {
		return fmt.Errorf("replace order picking insert (%s): %w", w.OrderID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("replace order picking commit (%s): %w", w.OrderID, err)
	}
	return nil
}

// AppendOrderPicking дозаписывает единицы подбора (добор строки: часть единиц строки
// подобрана раньше и уже лежит в журнале). Пустой слайс — nil без запроса.
func (pg *PGClient) AppendOrderPicking(ctx context.Context, units []msorders.PickingUnit) error {
	if len(units) == 0 {
		return nil
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("append order picking begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после Commit — no-op

	if err := insertOrderPickingUnits(ctx, tx, units); err != nil {
		return fmt.Errorf("append order picking insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("append order picking commit: %w", err)
	}
	return nil
}

// RemoveOrderPickingUnits убирает вернувшиеся в «Сроки» единицы: РОВНО Count строк,
// совпавших по коду склада, сроку годности и весу. Ничего не нашлось — НЕ ошибка:
// строки могли быть записаны до появления журнала (тогда возврату просто нечего снимать).
func (pg *PGClient) RemoveOrderPickingUnits(ctx context.Context, r msorders.PickingReturn) error {
	if r.Count <= 0 {
		return nil
	}

	if _, err := pg.Pool.Exec(ctx, orderPickingRemoveUnitsSQL,
		r.OrderID, r.InternalCode, r.BestBefore, r.WeightKg, r.Count,
	); err != nil {
		return fmt.Errorf("remove order picking units (%s, %s): %w", r.OrderID, r.InternalCode, err)
	}
	return nil
}

// ClearOrderPicking чистит журнал заказа: positionIDs пусто — весь заказ
// (отмена/расформирование), иначе только перечисленные позиции (позиция удалена из
// заказа, ручное закрытие строки).
func (pg *PGClient) ClearOrderPicking(ctx context.Context, orderID string, positionIDs []string) error {
	if len(positionIDs) == 0 {
		if _, err := pg.Pool.Exec(ctx, orderPickingDeleteOrderSQL, orderID); err != nil {
			return fmt.Errorf("clear order picking (%s): %w", orderID, err)
		}
		return nil
	}

	if _, err := pg.Pool.Exec(ctx, orderPickingDeleteByPositionsSQL, orderID, positionIDs); err != nil {
		return fmt.Errorf("clear order picking positions (%s): %w", orderID, err)
	}
	return nil
}

// ClearOrderPickingProducts чистит журнал по товарам заказа (позиции удалены, известны
// только uuid товаров из диффа аудита МС). Пустой список — nil без запроса.
func (pg *PGClient) ClearOrderPickingProducts(ctx context.Context, orderID string, productIDs []string) error {
	if len(productIDs) == 0 {
		return nil
	}

	if _, err := pg.Pool.Exec(ctx, orderPickingDeleteByProductsSQL, orderID, productIDs); err != nil {
		return fmt.Errorf("clear order picking products (%s): %w", orderID, err)
	}
	return nil
}

// OrderPickingByOrder отдаёт строки журнала заказа (для ответа на /sroki): единицы
// подряд по внутреннему коду, внутри товара — по сроку годности.
func (pg *PGClient) OrderPickingByOrder(ctx context.Context, orderID string) ([]msorders.PickingUnit, error) {
	rows, err := pg.Pool.Query(ctx, orderPickingByOrderSQL, orderID)
	if err != nil {
		return nil, fmt.Errorf("order picking by order %s: %w", orderID, err)
	}
	defer rows.Close()

	units, err := collectPickingUnits(rows)
	if err != nil {
		return nil, fmt.Errorf("order picking by order %s: %w", orderID, err)
	}
	return units, nil
}

// CleanupOrderPicking удаляет строки, не тронутые с olderThan, и возвращает число
// удалённых (ретеншен журнала; пустой результат — не ошибка).
func (pg *PGClient) CleanupOrderPicking(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := pg.Pool.Exec(ctx, orderPickingCleanupSQL, olderThan)
	if err != nil {
		return 0, fmt.Errorf("cleanup order picking before %s: %w", olderThan.Format(time.DateOnly), err)
	}
	return tag.RowsAffected(), nil
}

// insertOrderPickingUnits пишет единицы подбора одним батчем (не N+1): вызов на
// строку единицы, но все — одной отправкой в рамках транзакции вызывающего.
func insertOrderPickingUnits(ctx context.Context, tx pgx.Tx, units []msorders.PickingUnit) error {
	if len(units) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, u := range units {
		batch.Queue(orderPickingInsertSQL,
			u.OrderID, u.PositionID, u.InternalCode, u.ProductID, u.ProductName,
			u.Weighted, u.WeightKg, u.ProducedOn, u.BestBefore)
	}

	results := tx.SendBatch(ctx, batch)
	for i, u := range units {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("insert picking unit %d (%s, %s): %w", i, u.OrderID, u.InternalCode, err)
		}
	}
	// Close() второй раз поверх Exec — no-op pgx: закрывает батч и возвращает
	// ошибку отправки, если её не поймали выше.
	if err := results.Close(); err != nil {
		return fmt.Errorf("insert picking units close: %w", err)
	}
	return nil
}

// scanPickingUnit разбирает строку журнала подбора (порядок orderPickingColumns).
// Один хелпер и для QueryRow, и для rows.Next. product_id — nullable-колонка
// (FK ON DELETE SET NULL): в домене пустая строка значит «товара нет в каталоге»,
// поэтому NULL читается через *string, а не прямо в string (иначе pgx падает:
// «cannot scan NULL into *string»).
func scanPickingUnit(row pgx.Row) (msorders.PickingUnit, error) {
	var (
		u         msorders.PickingUnit
		productID *string
	)
	if err := row.Scan(
		&u.OrderID, &u.PositionID, &u.InternalCode, &productID, &u.ProductName,
		&u.Weighted, &u.WeightKg, &u.ProducedOn, &u.BestBefore,
	); err != nil {
		return msorders.PickingUnit{}, fmt.Errorf("scan picking unit: %w", err)
	}
	u.ProductID = textValue(productID)
	return u, nil
}

// collectPickingUnits собирает строки журнала в срез. Пустая выборка — пустой (не nil)
// срез: потребитель итерирует результат как есть. produced_on — NULL-дата и остаётся
// nil (дата выработки не известна), этим NULL и значим.
func collectPickingUnits(rows pgx.Rows) ([]msorders.PickingUnit, error) {
	units := make([]msorders.PickingUnit, 0, 8)
	for rows.Next() {
		u, err := scanPickingUnit(rows)
		if err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("order picking rows: %w", err)
	}
	return units, nil
}

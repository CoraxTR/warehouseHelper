package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Работа с резервом отменённых заказов («Возврат в продажу», модуль returns):
// МойСклад при переводе заказа в «Отменён» резерв на позициях НЕ сбрасывает
// (проверено владельцем 08.09.2026) — расформирование заказа снимает его PUT-ом
// (reserve → 0 на всех позициях), тело PUT — эхо GET-заказа с заменённым
// positions (тот же паттерн, что у подбора msorders; служебные поля МС можно
// не чистить — проверено). Строки позиций берутся ОТДЕЛЬНЫМ запросом к
// .../positions: в корне заказа МС отдаёт только meta-ссылку на позиции, строк
// там нет (живой API 24.09.2026, заказ 19379) — попытка взять строки из эха
// молча оставляла резерв висеть. Чтение состояния/тела заказа — SubmitOther
// (общие ключи), правка — SubmitWarehouse: наши изменения customerorder в аудите
// МС помечаются складским uid, наблюдатель аудита (returns) их пропускает.
//
// Статус заказа расформирование НЕ трогает (решение владельца 24.09.2026):
// возврат товара в оборот по полностью расформированному заказу («Отменён»)
// статус не меняет — поле state из эха GET в PUT не переносится.

// FetchOrderState — id текущего статуса заказа (последний сегмент
// state.meta.href). Пустая строка — у заказа нет статуса (не ошибка).
// Лёгкий фетч: только поля верхнего уровня (FetchOrderByID).
func (msac *MSAPIClient) FetchOrderState(parentctx context.Context, orderID string) (string, error) {
	_, raw, err := msac.FetchOrderByID(parentctx, orderID)
	if err != nil {
		return "", fmt.Errorf("FetchOrderState failed: %w", err)
	}
	return orderStateID(raw), nil
}

// orderStateID достаёт id статуса из сырого тела заказа (state.meta.href);
// пустая строка — статуса нет.
func orderStateID(raw json.RawMessage) string {
	var body struct {
		State *struct {
			Meta struct {
				HREF string `json:"href"`
			} `json:"meta"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.State == nil {
		return ""
	}
	href := body.State.Meta.HREF
	if i := strings.LastIndex(href, "/"); i >= 0 {
		return href[i+1:]
	}
	return href
}

// ClearOrderReserves — снять резерв со всех позиций заказа: reserve → 0.
// Тело PUT — полное эхо GET-заказа, где positions (строки как пришли из
// .../positions) заменены на массив с обнулённым reserve, а поле state НЕ
// переносится: статус заказа эта операция не меняет (решение владельца
// 24.09.2026). Идемпотентно (повторный вызов после успешного PUT — no-op:
// резерв уже 0, запрос не уходит). Если резерва на позициях нет вовсе — PUT
// не отправляется (менять нечего).
func (msac *MSAPIClient) ClearOrderReserves(parentctx context.Context, orderID string) error {
	order, raw, err := msac.FetchOrderByID(parentctx, orderID)
	if err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}

	// Позиции — отдельным запросом по meta-ссылке заказа: корень заказа строк
	// не содержит (МС отдаёт там только meta), и попытка взять их из эха молча
	// оставляла резерв висеть (прод-баг 24.09.2026).
	if order.MSPositions.Meta.HREF == "" {
		return fmt.Errorf("ClearOrderReserves failed: в заказе %s нет ссылки на позиции", orderID)
	}

	_, rowsRaw, err := msac.FetchOrderPositionsByHREF(parentctx, order)
	if err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}
	// PUT заменяет раздел positions целиком: неполный ответ .../positions
	// (обрезка пагинации, пустая выдача при непустом заказе) молча стёр бы
	// позиции — поэтому состав сверяется с обещанным размером корня.
	if size := order.MSPositions.Meta.Size; size > 0 && len(rowsRaw) != size {
		return fmt.Errorf("ClearOrderReserves failed: позиций %d, строк %d (заказ %s)", size, len(rowsRaw), orderID)
	}
	if len(rowsRaw) == 0 {
		return nil // заказ без позиций — резерва быть не может
	}

	rows, changed, err := zeroRowsReserve(rowsRaw)
	if err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}
	if !changed {
		return nil // резерв уже снят — PUT не нужен
	}

	body, err := orderPutBody(raw, rows)
	if err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}

	if err := msac.UpdateCustomerOrder(parentctx, orderID, body); err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}
	return nil
}

// zeroRowsReserve обнуляет reserve в сырых строках позиций (формат PUT: строки
// как пришли из .../positions). changed=false, если ни одного reserve > 0 —
// тогда PUT не нужен.
func zeroRowsReserve(rowsRaw []json.RawMessage) (rows []any, changed bool, err error) {
	rows = make([]any, 0, len(rowsRaw))

	for i, rawRow := range rowsRaw {
		var row map[string]any
		if err := json.Unmarshal(rawRow, &row); err != nil {
			return nil, false, fmt.Errorf("unmarshal position row %d: %w", i, err)
		}
		if reserve, ok := row["reserve"].(float64); ok && reserve > 0 {
			row["reserve"] = float64(0)
			changed = true
		}
		rows = append(rows, row)
	}

	return rows, changed, nil
}

// orderPutBody собирает тело PUT для правки заказа: эхо GET корня заказа с
// заменённым positions. Статус в PUT НЕ переносим (решение владельца
// 24.09.2026): тело — эхо GET, и без этого PUT заново утверждал бы статус из
// эха, затирая изменения, сделанные после чтения (в частности расформирование
// заказа целиком — «Отменен» — и возврат заказа в работу). Статус меняют только
// шаги подбора.
func orderPutBody(orderRaw json.RawMessage, rows []any) (json.RawMessage, error) {
	var body map[string]any
	if err := json.Unmarshal(orderRaw, &body); err != nil {
		return nil, fmt.Errorf("unmarshal order raw: %w", err)
	}

	delete(body, "state")
	body["positions"] = rows

	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal order body: %w", err)
	}
	return out, nil
}

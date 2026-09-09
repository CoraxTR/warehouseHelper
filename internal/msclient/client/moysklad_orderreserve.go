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
// не чистить — проверено). Чтение состояния/тела заказа — SubmitOther (общие
// ключи), правка — SubmitWarehouse: наши изменения customerorder в аудите МС
// помечаются складским uid, наблюдатель аудита (returns) их пропускает.

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
// Тело PUT — полное эхо GET-заказа, где positions (строки как пришли из МС)
// заменены на массив с обнулённым reserve; идемпотентно (повторный вызов
// после успешного PUT — no-op: резерв уже 0, запрос не уходит). Если резерва
// на позициях нет вовсе — PUT не отправляется (менять нечего).
func (msac *MSAPIClient) ClearOrderReserves(parentctx context.Context, orderID string) error {
	_, raw, err := msac.FetchOrderByID(parentctx, orderID)
	if err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}

	body, changed, err := zeroOrderReserves(raw)
	if err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}
	if !changed {
		return nil // резерв уже снят (или позиций нет) — PUT не нужен
	}

	if err := msac.UpdateCustomerOrder(parentctx, orderID, body); err != nil {
		return fmt.Errorf("ClearOrderReserves failed: %w", err)
	}
	return nil
}

// zeroOrderReserves обнуляет reserve у строк positions сырого тела заказа.
// В GET positions приходит объектом {rows:[...]}, в PUT уходит массивом строк —
// возвращаемое тело уже в PUT-формате (как в подборе msorders). changed=false,
// если менять нечего (нет positions / ни одного резерва > 0) — PUT не нужен.
func zeroOrderReserves(raw json.RawMessage) (json.RawMessage, bool, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, fmt.Errorf("unmarshal order raw: %w", err)
	}

	pos, ok := body["positions"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	rows, ok := pos["rows"].([]any)
	if !ok || len(rows) == 0 {
		return nil, false, nil
	}

	changed := false
	for _, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		reserve, ok := row["reserve"].(float64)
		if ok && reserve > 0 {
			row["reserve"] = float64(0)
			changed = true
		}
	}
	if !changed {
		return nil, false, nil
	}

	body["positions"] = rows
	out, err := json.Marshal(body)
	if err != nil {
		return nil, false, fmt.Errorf("marshal order body: %w", err)
	}
	return out, true, nil
}

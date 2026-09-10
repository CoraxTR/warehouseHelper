// Выборка заказов покупателя по плановой дате доставки (раздел «Заказы» МС):
// печать бланков пачкой за день. Разбор строк лёгкий — атрибуты не парсим.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ordersByDatePageLimit — размер страницы выборки заказов по дате.
const ordersByDatePageLimit = 1000

// FetchOrdersByDeliveryDate — заказы покупателя с плановой датой доставки в
// указанный день: filter=deliveryPlannedMoment>=<начало дня>;<...<=<конец дня>.
// День трактуется в TZ учётки (auditLoc, МСК) — как его показывает МС.
//
// Разбор строк лёгкий (attributes не разбираются): выборка широкая, типы
// атрибутов в базе любые, а unmarshalMSFetchOrdersResponse падает на незнакомых
// (см. moysklad_umarshaller.go). expand=state — ради имени статуса в строке;
// МС вправе expand проигнорировать (как с agent при filter=name), тогда
// State.Name пустой и имя дотягивается картой FetchOrderStates по State.ID.
// Рейт-лимит — воркерпул (SubmitOther), напрямую к МС не ходим.
func (msac *MSAPIClient) FetchOrdersByDeliveryDate(parentctx context.Context, day time.Time) ([]MSOrder, error) {
	filter := deliveryDateFilter(day)

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 60*time.Second)
		defer cancel()

		var orders []MSOrder

		for offset := 0; ; offset += ordersByDatePageLimit {
			// Отмена контекста между страницами: не тратим запрос, если родитель уже отменил.
			if err := parentctx.Err(); err != nil {
				return nil, err
			}

			endpoint, err := msac.ordersByDateEndpoint(filter, offset)
			if err != nil {
				return nil, err
			}

			body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
			if err != nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return nil, err
				}
			}

			err = resp.Body.Close()
			if err != nil {
				slog.Error(fmt.Sprintf("failed to close response body: %v", err))
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, msAPIError(resp.Status, body)
			}

			var list struct {
				Meta MSMeta    `json:"meta"`
				Rows []MSOrder `json:"rows"`
			}
			if err := json.Unmarshal(body, &list); err != nil {
				return nil, fmt.Errorf("failed to unmarshal orders by delivery date: %w", err)
			}

			for i := range list.Rows {
				list.Rows[i].StateID = hrefID(list.Rows[i].State.Meta.HREF)
			}

			orders = append(orders, list.Rows...)

			if offset+ordersByDatePageLimit >= list.Meta.Size {
				break
			}
		}

		return orders, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchOrdersByDeliveryDate failed: %w", res.Err)
		}

		orders, ok := res.Value.([]MSOrder)
		if !ok {
			return nil, errors.New("FetchOrdersByDeliveryDate failed: unexpected value type")
		}

		return orders, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// FetchOrderStates — справочник статусов заказа: id статуса → имя
// (GET /entity/customerorder/metadata/states). Запасной источник имени статуса
// для списков, где МС проигнорировал expand=state (один запрос на страницу).
func (msac *MSAPIClient) FetchOrderStates(parentctx context.Context) (map[string]string, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 30*time.Second)
		defer cancel()

		endpoint, err := msac.statesEndpoint()
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, err
			}
		}

		err = resp.Body.Close()
		if err != nil {
			slog.Error(fmt.Sprintf("failed to close response body: %v", err))
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, msAPIError(resp.Status, body)
		}

		var list struct {
			Rows []MSState `json:"rows"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("failed to unmarshal order states: %w", err)
		}

		states := make(map[string]string, len(list.Rows))
		for i := range list.Rows {
			id := hrefID(list.Rows[i].Meta.HREF)
			if id == "" {
				continue
			}

			states[id] = list.Rows[i].Name
		}

		return states, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchOrderStates failed: %w", res.Err)
		}

		states, ok := res.Value.(map[string]string)
		if !ok {
			return nil, errors.New("FetchOrderStates failed: unexpected value type")
		}

		return states, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// deliveryDateFilter — фильтр МС «плановая дата доставки в пределах дня»
// (день — в TZ учётки, auditLoc). Конец дня — 23:59:59.999 (формат моментов МС).
func deliveryDateFilter(day time.Time) string {
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, auditLoc)
	to := from.AddDate(0, 0, 1).Add(-time.Millisecond)

	return "deliveryPlannedMoment>=" + from.Format(auditMomentLayout) +
		";deliveryPlannedMoment<=" + to.Format(auditMomentLayout)
}

// ordersByDateEndpoint — URL выборки заказов по дате: filter + expand=state +
// пагинация. Query-строка добавляется после entityEndpoint: path.Join не
// переваривает '?'.
func (msac *MSAPIClient) ordersByDateEndpoint(filter string, offset int) (string, error) {
	endpoint, err := msac.entityEndpoint("customerorder")
	if err != nil {
		return "", err
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to parse orders by date endpoint: %w", err)
	}

	q := u.Query()
	q.Set("filter", filter)
	q.Set("expand", "state")
	q.Set("limit", strconv.Itoa(ordersByDatePageLimit))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// statesEndpoint — URL справочника статусов заказа (metadata/states).
func (msac *MSAPIClient) statesEndpoint() (string, error) {
	endpoint, err := msac.entityEndpoint("customerorder", "metadata", "states")
	if err != nil {
		return "", err
	}

	return endpoint, nil
}

// hrefID — последний сегмент href МС (id сущности): "…/states/<uuid>" → "<uuid>".
// Пустая строка — href пуст или обрывается слэшем.
func hrefID(href string) string {
	href, _, _ = strings.Cut(href, "?")

	i := strings.LastIndex(href, "/")

	return strings.TrimSpace(href[i+1:])
}

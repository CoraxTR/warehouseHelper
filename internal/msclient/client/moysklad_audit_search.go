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
	"time"
)

// Поиск ручных удалений позиций в аудите МойСклада (страница «Поиск
// удалений», /ms/audit-search). Методы — только чтение: листинг окна
// «заказы/update» и резолв товара по id из нашей базы в канонический id
// remap (id каталога — legacy uuidHref, в аудите ссылки на remap-id).
//
// Аудит закрыт для части общих ключей, поэтому листинг и раскрытие идут
// строго под ключами склада (SubmitWarehouse) — как у вотчера аудита;
// рейт-лимит целиком на воркерпуле, своих лимитеров здесь нет.

// auditWindowDefaultLimit — размер страницы окна поиска удалений: листинг
// читается пачками по 100 контекстов (у FetchAuditPage лимит 25 зашит —
// его поведение не трогаем, вотчер аудита живёт на нём).
const auditWindowDefaultLimit = 100

// FetchAuditPageWindow — страница листинга аудита в окне дат:
// audit?filter=entityType=customerorder;eventType=update;
// moment>=<from МСК>;moment<=<to МСК>&limit=<limit>&offset=<offset>.
// Возвращает строки страницы и общий size (для пагинации offset += len(rows)).
// from/to — инстанты; в фильтр попадают в TZ учётки (auditLoc), как и у
// FetchAuditPage. limit <= 0 — auditWindowDefaultLimit.
func (msac *MSAPIClient) FetchAuditPageWindow(parentctx context.Context, from, to time.Time, offset, limit int) ([]AuditRow, int, error) {
	if limit <= 0 {
		limit = auditWindowDefaultLimit
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.auditEndpoint("audit")
		if err != nil {
			return nil, err
		}

		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		filter := "entityType=customerorder;eventType=update" +
			";moment>=" + from.In(auditLoc).Format(auditMomentLayout) +
			";moment<=" + to.In(auditLoc).Format(auditMomentLayout)
		q.Set("filter", filter)
		q.Set("limit", strconv.Itoa(limit))
		q.Set("offset", strconv.Itoa(offset))
		u.RawQuery = q.Encode()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, u.String(), apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Error(fmt.Sprintf("FetchAuditPageWindow timed out: %v", ctx.Err()))
			default:
				slog.Error(fmt.Sprintf("FetchAuditPageWindow failed: %v", err))
			}
			return nil, err
		}
		defer func() {
			err = resp.Body.Close()
			if err != nil {
				slog.Error(fmt.Sprintf("failed to close response body: %v", err))
			}
		}()

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, msAPIError(resp.Status, body)
		}

		var list msAuditListResponse
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, err
		}

		return &list, nil
	}

	resultCh := msac.workerpool.SubmitWarehouse(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return nil, 0, fmt.Errorf("FetchAuditPageWindow failed: %w", res.Err)
		}

		list, ok := res.Value.(*msAuditListResponse)
		if !ok {
			return nil, 0, errors.New("FetchAuditPageWindow failed: unexpected value type")
		}

		return list.Rows, list.Meta.Size, nil
	case <-parentctx.Done():
		return nil, 0, parentctx.Err()
	}
}

// GetProduct — канонический id и имя товара по id из нашей базы
// (GET /entity/product/<id>). Нужен поиску удалений: products.id — legacy
// uuidHref, а аудит ссылается на remap-id, и МойСклад отдаёт в response.ID
// уже канонический id (23c7c8f1-… → 23c7d1ba-…).
func (msac *MSAPIClient) GetProduct(parentctx context.Context, id string) (canonicalID, name string, err error) {
	product, err := msac.FetchProductByID(parentctx, id)
	if err != nil {
		return "", "", fmt.Errorf("GetProduct %s: %w", id, err)
	}

	if product.ID == "" {
		return "", "", fmt.Errorf("GetProduct %s: пустой id в ответе МойСклада", id)
	}

	return product.ID, product.Name, nil
}

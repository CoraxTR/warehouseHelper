package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Аудит МойСклад: журнал действий. Глобальный лист живёт в корне remap
// (НЕ под entity/): https://api.moysklad.ru/api/remap/1.2/audit.
// Потребитель — модуль returns (наблюдатель «возврат в продажу»).

// auditPageLimit — размер страницы журнала аудита (проверено на живом API:
// лимит в ответе 25).
const auditPageLimit = 25

// Моменты аудита приходят строками "2006-01-02 15:04:05.000" в часовом поясе
// учётки (проверено: МСК, событие 23:11:52 при 20:44 UTC). В БД и коде —
// UTC; при формировании фильтра момент конвертируется обратно в МСК.
const auditMomentLayout = "2006-01-02 15:04:05.000"

// auditLoc — TZ учётки для моментов аудита. Europe/Moscow; если базы tzdata
// нет (Windows-прод без tzdata) — фиксированное смещение +3 (МСК).
var auditLoc = func() *time.Location {
	if loc, err := time.LoadLocation("Europe/Moscow"); err == nil {
		return loc
	}
	return time.FixedZone("MSK", 3*60*60)
}()

// ParseAuditMoment разбирает момент события аудита (TZ учётки) в UTC.
func ParseAuditMoment(s string) (time.Time, error) {
	t, err := time.ParseInLocation(auditMomentLayout, s, auditLoc)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// AuditRow — строка глобального листа audit?filter=... (diff'ов и имени в
// листе НЕТ — детали раскрываются FetchAuditDetail).
type AuditRow struct {
	ID         string  `json:"id"`         // uuid события аудита (audit/<id>)
	Moment     string  `json:"moment"`     // "2006-01-02 15:04:05.000", TZ учётки
	EntityType string  `json:"entityType"` // customerorder / product / ...
	EventType  string  `json:"eventType"`  // update / print / ...
	Source     *string `json:"source"`     // remap-1.2 (API) / app (веб) / null
	UID        string  `json:"uid"`        // пользователь учётки (все события — sklad@steakhome)
}

type msAuditListResponse struct {
	Meta struct {
		Size int `json:"size"`
	} `json:"meta"`
	Rows []AuditRow `json:"rows"`
}

// AuditEventRow — строка раскрытия GET audit/<id>/events: полный diff
// изменения + имя сущности и ссылка на неё.
type AuditEventRow struct {
	AdditionalInfo string `json:"additionalInfo"`
	Audit          struct {
		Meta MSMeta `json:"meta"`
	} `json:"audit"`
	Diff   AuditDiff `json:"diff"`
	Entity struct {
		Meta MSMeta `json:"meta"`
	} `json:"entity"`
	EntityType string  `json:"entityType"`
	EventType  string  `json:"eventType"`
	Moment     string  `json:"moment"`
	Name       string  `json:"name"` // номер заказа и т.п.
	Source     *string `json:"source"`
	UID        string  `json:"uid"`
}

// AuditDiff — изменённые поля события. В JSON приходят только изменившиеся
// ключи (state / positions / sum / moment / ...), остальные игнорируем.
type AuditDiff struct {
	State     *AuditStateChange     `json:"state"`
	Positions []AuditPositionChange `json:"positions"`
}

// AuditStateChange — смена статуса заказа: oldValue/newValue — сам статус.
type AuditStateChange struct {
	OldValue *AuditState `json:"oldValue"`
	NewValue *AuditState `json:"newValue"`
}

// AuditState — статус заказа (states/...).
type AuditState struct {
	Meta MSMeta `json:"meta"`
	Name string `json:"name"`
}

// AuditPositionChange — изменение позиции заказа. Удаление позиции: только
// OldValue (NewValue == nil); изменение: оба.
type AuditPositionChange struct {
	OldValue *AuditPosition `json:"oldValue"`
	NewValue *AuditPosition `json:"newValue"`
}

// AuditPosition — снимок позиции заказа в диффе. Quantity/reserve — в кг
// (весовые) или единицах (штучные, uom "шт"); сравнение — в граммах/штуках
// на стороне потребителя (не float == по кг).
type AuditPosition struct {
	Assortment struct {
		Meta MSMeta `json:"meta"`
		Name string `json:"name"`
	} `json:"assortment"`
	Quantity float64 `json:"quantity"`
	Reserve  float64 `json:"reserve"`
	Uom      *string `json:"uom"` // "кг"/"шт" или null
}

type msAuditDetailResponse struct {
	Meta struct {
		Size int `json:"size"`
	} `json:"meta"`
	Rows []AuditEventRow `json:"rows"`
}

// auditEndpoint — URL в корне remap (вне entity/): audit, audit/<id>/events.
// URLstart заканчивается на /entity/ — сегмент убирается (как profitReportEndpoint).
func (msac *MSAPIClient) auditEndpoint(parts ...string) (string, error) {
	base, err := url.Parse(msac.msConfig.URLstart)
	if err != nil {
		return "", fmt.Errorf("failed to parse MS API base URL: %w", err)
	}

	root := strings.Replace(base.Path, "/entity/", "", 1)
	base.Path = path.Join(append([]string{root}, parts...)...)

	return base.String(), nil
}

// FetchAuditPage — страница глобального журнала аудита:
// audit?filter=eventType=update;moment>=<since МСК>&limit=25&offset=<offset>.
// Возвращает строки страницы и общий size (для пагинации offset += 25).
// Момент в фильтре — секунды (без миллисекунд): события той же секунды,
// что и курсор, повторно попадут в окно и будут отсеяны дедупом по id
// (PK return_events) — пропуска окна не возникает.
// Рейт-лимит — воркерпул (SubmitOther), напрямую к МС не ходим.
func (msac *MSAPIClient) FetchAuditPage(parentctx context.Context, since time.Time, offset int) ([]AuditRow, int, error) {
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
		q.Set("filter", "eventType=update;moment>="+since.In(auditLoc).Format("2006-01-02 15:04:05"))
		q.Set("limit", strconv.Itoa(auditPageLimit))
		q.Set("offset", strconv.Itoa(offset))
		u.RawQuery = q.Encode()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, u.String(), apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Error(fmt.Sprintf("FetchAuditPage timed out: %v", ctx.Err()))
			default:
				slog.Error(fmt.Sprintf("FetchAuditPage failed: %v", err))
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

	resultCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return nil, 0, fmt.Errorf("FetchAuditPage failed: %w", res.Err)
		}

		list, ok := res.Value.(*msAuditListResponse)
		if !ok {
			return nil, 0, errors.New("FetchAuditPage failed: unexpected value type")
		}

		return list.Rows, list.Meta.Size, nil
	case <-parentctx.Done():
		return nil, 0, parentctx.Err()
	}
}

// FetchAuditDetail — раскрытие события: GET audit/<id>/events → строки с
// полным diff (удаления позиций, смена статуса) и именем сущности.
// Пагинация на случай size > 25 (на практике событие — 1–2 строки).
func (msac *MSAPIClient) FetchAuditDetail(parentctx context.Context, auditID string) ([]AuditEventRow, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		var rows []AuditEventRow

		for offset := 0; ; offset += auditPageLimit {
			endpoint, err := msac.auditEndpoint("audit", auditID, "events")
			if err != nil {
				return nil, err
			}

			u, err := url.Parse(endpoint)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("limit", strconv.Itoa(auditPageLimit))
			q.Set("offset", strconv.Itoa(offset))
			u.RawQuery = q.Encode()

			body, resp, err := msac.httpRequest(ctx, http.MethodGet, u.String(), apiKey, http.NoBody)
			if err != nil {
				select {
				case <-ctx.Done():
					slog.Error(fmt.Sprintf("FetchAuditDetail timed out: %v", ctx.Err()))
				default:
					slog.Error(fmt.Sprintf("FetchAuditDetail failed: %v", err))
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

			var detail msAuditDetailResponse
			if err := json.Unmarshal(body, &detail); err != nil {
				return nil, err
			}

			rows = append(rows, detail.Rows...)
			if len(rows) >= detail.Meta.Size {
				break
			}
		}

		return rows, nil
	}

	resultCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchAuditDetail failed: %w", res.Err)
		}

		rows, ok := res.Value.([]AuditEventRow)
		if !ok {
			return nil, errors.New("FetchAuditDetail failed: unexpected value type")
		}

		return rows, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// FetchOrderPositions — позиции заказа с expand=assortment (имя товара).
// Лёгкий фетч для состава возврата отменённого заказа (в отличие от
// тяжёлого GetOrderByHREF с enrichOrder).
func (msac *MSAPIClient) FetchOrderPositions(parentctx context.Context, orderID string) ([]MSPosition, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("customerorder", orderID, "positions")
		if err != nil {
			return nil, err
		}

		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("expand", "assortment")
		u.RawQuery = q.Encode()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, u.String(), apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Error(fmt.Sprintf("FetchOrderPositions timed out: %v", ctx.Err()))
			default:
				slog.Error(fmt.Sprintf("FetchOrderPositions failed: %v", err))
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

		fetch, err := unmarshalPositionRows(body)
		if err != nil {
			return nil, err
		}

		return fetch.positions, nil
	}

	resultCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchOrderPositions failed: %w", res.Err)
		}

		positions, ok := res.Value.([]MSPosition)
		if !ok {
			return nil, errors.New("FetchOrderPositions failed: unexpected value type")
		}

		return positions, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

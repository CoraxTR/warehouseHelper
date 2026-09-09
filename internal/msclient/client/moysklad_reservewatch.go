package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// reserveWatchPageLimit — размер страницы листа заказов (пагинация offset).
const reserveWatchPageLimit = 1000

// FetchReserveWatchOrders — лист заказов модуля reservewatch: заказы в статусах
// stateIDs (OR — повторением поля state=<href>) с плановой отгрузкой не раньше
// начала суток windowDays календарных дней назад (TZ учётки = МСК).
// Фильтр по статусам принимает ТОЛЬКО meta-href (uuid и имена в {} → 1014,
// проверено на живом API 09.09.2026); OR по одному полю — повторение поля
// через ';' (|| молча обрезает выборку — 09.09.2026).
// Лёгкий фетч: из строк берутся id/name (positions в листе — meta-ссылки,
// expand=positions МС на листе не раскрывает — проверено 09.09.2026).
// Пагинация: offset += 1000 до конца (meta.size), паттерн FetchProductFolders.
func (msac *MSAPIClient) FetchReserveWatchOrders(parentctx context.Context, windowDays int, stateIDs []string) ([]MSOrder, error) {
	if len(stateIDs) == 0 {
		return nil, errors.New("FetchReserveWatchOrders: пуст список статусов")
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("customerorder")
		if err != nil {
			return nil, err
		}

		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		q := u.Query()

		today := time.Now().In(auditLoc)
		y, m, d := today.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, auditLoc).AddDate(0, 0, -windowDays)

		filter := strings.Builder{}
		filter.WriteString("deliveryPlannedMoment>=")
		filter.WriteString(start.Format(auditMomentLayout))
		for _, id := range stateIDs {
			filter.WriteString(";state=")
			filter.WriteString(msac.refHref("customerorder/metadata/states", id))
		}
		q.Set("filter", filter.String())
		q.Set("limit", strconv.Itoa(reserveWatchPageLimit))

		orders := make([]MSOrder, 0)
		for page := 0; ; page++ {
			q.Set("offset", strconv.Itoa(page*reserveWatchPageLimit))
			u.RawQuery = q.Encode()

			body, resp, err := msac.httpRequest(ctx, http.MethodGet, u.String(), apiKey, http.NoBody)
			if err != nil {
				select {
				case <-ctx.Done():
					slog.Error(fmt.Sprintf("FetchReserveWatchOrders timed out: %v", ctx.Err()))
				default:
					slog.Error(fmt.Sprintf("FetchReserveWatchOrders failed: %v", err))
				}
				return nil, err
			}

			func() {
				err = resp.Body.Close()
				if err != nil {
					slog.Error(fmt.Sprintf("failed to close response body: %v", err))
				}
			}()

			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				return nil, msAPIError(resp.Status, body)
			}

			pageResp, err := unmarshalMSFetchOrdersResponse(body)
			if err != nil {
				return nil, err
			}

			orders = append(orders, pageResp.Rows...)
			if len(pageResp.Rows) == 0 || page*reserveWatchPageLimit+len(pageResp.Rows) >= pageResp.Meta.Size {
				break
			}
		}

		return orders, nil
	}

	resultCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchReserveWatchOrders failed: %w", res.Err)
		}

		orders, ok := res.Value.([]MSOrder)
		if !ok {
			return nil, errors.New("FetchReserveWatchOrders failed: unexpected value type")
		}

		return orders, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

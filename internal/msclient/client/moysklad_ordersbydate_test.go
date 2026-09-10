package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// customerOrderTestPath — путь списка заказов; тесты пакета используют
// константу вместо литерала (goconst).
const (
	customerOrderTestPath = "/entity/customerorder"
	// stateNameCancelled — имя статуса «Отменён» в ответах МС.
	stateNameCancelled = "Отменен"
)

// stateHref собирает href статуса заказа — как его отдаёт МС.

func stateHref(id string) string {
	return "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/" + id
}

// TestFetchOrdersByDeliveryDate — выборка заказов дня: фильтр «плановая дата
// доставки в пределах суток» (полночь → 23:59:59.999 в TZ учётки), expand=state,
// пагинация по offset до meta.size. Разбор лёгкий: строка с атрибутом типа,
// которого не знает unmarshalMSOrderAttributes (double), не должна ронять выдачу.
func TestFetchOrdersByDeliveryDate(t *testing.T) {
	var (
		hits      int
		gotFilter string
		gotExpand string
	)

	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}

		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		if r.URL.Path != customerOrderTestPath {
			t.Errorf("path = %s, want /entity/customerorder", r.URL.Path)
		}

		hits++
		gotFilter = r.URL.Query().Get("filter")
		gotExpand = r.URL.Query().Get("expand")

		offset := 0
		_, _ = fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)

		rows := make([]map[string]any, 0, ordersByDatePageLimit)
		for i := offset; i < 1500 && len(rows) < ordersByDatePageLimit; i++ {
			rows = append(rows, map[string]any{"id": fmt.Sprintf("o-%d", i), "name": fmt.Sprintf("%d", i)})
		}

		if offset == 0 {
			rows[0] = map[string]any{
				"id":                    "o-order-1",
				"name":                  "19191",
				"deliveryPlannedMoment": "2026-09-10 10:00:00.000",
				"state": map[string]any{
					"meta": map[string]any{"href": stateHref("st-1")},
					"name": stateNameCancelled,
				},
				"attributes": []map[string]any{
					{"name": "Кол-во мест", "type": "double", "value": 1.5},
				},
			}
		}

		w.Header().Set("Content-Type", "application/json")

		if err := json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]any{"size": 1500},
			"rows": rows,
		}); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	// 15:30 UTC = 18:30 МСК: день в TZ учётки — 10 сентября.
	orders, err := msac.FetchOrdersByDeliveryDate(context.Background(),
		time.Date(2026, time.September, 10, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchOrdersByDeliveryDate() error: %v", err)
	}

	if hits != 2 {
		t.Errorf("HTTP hits = %d, want 2 (offset 0 и 1000)", hits)
	}

	wantFilter := "deliveryPlannedMoment>=2026-09-10 00:00:00.000;" +
		"deliveryPlannedMoment<=2026-09-10 23:59:59.999"
	if gotFilter != wantFilter {
		t.Errorf("filter = %q, want %q", gotFilter, wantFilter)
	}
	if gotExpand != "state" {
		t.Errorf("expand = %q, want state", gotExpand)
	}

	if len(orders) != 1500 {
		t.Fatalf("orders = %d, want 1500 (полная пагинация)", len(orders))
	}

	first := orders[0]
	if first.DeliveryPlannedMoment != "2026-09-10 10:00:00.000" {
		t.Errorf("deliveryPlannedMoment = %q", first.DeliveryPlannedMoment)
	}
	if first.State.Name != stateNameCancelled {
		t.Errorf("state.name = %q, want Отменен", first.State.Name)
	}
	if first.StateID != "st-1" {
		t.Errorf("state id = %q, want st-1", first.StateID)
	}
	// Лёгкий разбор: до AttributesMap дело не доходит (строгий разборщик упал бы
	// на типе double и уронил всю выдачу).
	if first.AttributesMap != nil {
		t.Errorf("атрибуты разбирались: AttributesMap = %v", first.AttributesMap)
	}
	if last := orders[len(orders)-1]; last.ID != "o-1499" {
		t.Errorf("край пагинации: %s, want o-1499", last.ID)
	}
}

// TestFetchOrdersByDeliveryDate_Error — не-2xx от МС превращается в
// типизированную ошибку (её разбирает retry-цикл печати бланков).
func TestFetchOrdersByDeliveryDate_Error(t *testing.T) {
	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}

		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"error":"Слишком много запросов"}]}`))
	})

	_, err := msac.FetchOrdersByDeliveryDate(context.Background(), time.Now())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	apiErr := &MSAPIError{}
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *MSAPIError", err)
	}
	if apiErr.Code != http.StatusTooManyRequests || apiErr.Permanent() {
		t.Errorf("code = %d permanent = %v, want 429 и временную ошибку", apiErr.Code, apiErr.Permanent())
	}
}

// TestFetchOrderStates — справочник статусов: id (последний сегмент href) → имя.
// Строки без id пропускаются.
func TestFetchOrderStates(t *testing.T) {
	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}

		if r.URL.Path != "/entity/customerorder/metadata/states" {
			t.Errorf("path = %s, want /entity/customerorder/metadata/states", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")

		if err := json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{
				{"name": stateNameCancelled, "meta": map[string]any{"href": stateHref("st-1")}},
				{"name": "Подготовка", "meta": map[string]any{"href": stateHref("st-2") + "?expand=x"}},
				{"name": "Без ссылки", "meta": map[string]any{"href": ""}},
			},
		}); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	states, err := msac.FetchOrderStates(context.Background())
	if err != nil {
		t.Fatalf("FetchOrderStates() error: %v", err)
	}

	if len(states) != 2 {
		t.Fatalf("states = %v, want 2 записи", states)
	}
	if states["st-1"] != stateNameCancelled {
		t.Errorf("st-1 = %q, want Отменен", states["st-1"])
	}
	// href с query-хвостом (МС иногда отдаёт expand-суффикс) — id берётся верно.
	if states["st-2"] != "Подготовка" {
		t.Errorf("st-2 = %q, want Подготовка", states["st-2"])
	}
}

// TestMSAPIError_Permanent — 4xx (кроме 408/429) повторять бессмысленно;
// 5xx, 408 и 429 — временные.
func TestMSAPIError_Permanent(t *testing.T) {
	tests := []struct {
		name string
		code int
		want bool
	}{
		{name: "400 запрос отвергнут", code: http.StatusBadRequest, want: true},
		{name: "403 доступ запрещён", code: http.StatusForbidden, want: true},
		{name: "404 нет заказа", code: http.StatusNotFound, want: true},
		{name: "415 WAF-бан", code: http.StatusUnsupportedMediaType, want: true},
		{name: "408 таймаут", code: http.StatusRequestTimeout, want: false},
		{name: "429 лимит запросов", code: http.StatusTooManyRequests, want: false},
		{name: "500 сбой МС", code: http.StatusInternalServerError, want: false},
		{name: "код не разобран", code: 0, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := &MSAPIError{Status: "статус", Code: tc.code}
			if got := err.Permanent(); got != tc.want {
				t.Errorf("Permanent() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMSAPIError_Error — текст ошибки: errors[] МС, иначе тело ответа.
func TestMSAPIError_Error(t *testing.T) {
	withErrors := msAPIError("400 Bad Request", []byte(`{"errors":[{"error":"Некорректный заказ"}]}`))
	if got := withErrors.Error(); got != "API returned 400 Bad Request: Некорректный заказ" {
		t.Errorf("Error() = %q", got)
	}

	withBody := msAPIError("415 Unsupported Media Type", []byte("<html>ban</html>"))
	if got := withBody.Error(); got != "API returned 415 Unsupported Media Type: <html>ban</html>" {
		t.Errorf("Error() = %q", got)
	}
}

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestFetchReserveWatchOrders — лист заказов окна (модуль reservewatch):
// фильтр несёт deliveryPlannedMoment>= (начало суток 7 дней назад по TZ
// учётки) и state=<href> ПОВТОРОМ поля через ';' для каждого статуса
// (проверено на живом API 09.09: '||' МС молча режет выборку), ходит общими
// ключами (SubmitOther) и пагинирует offset-ами до meta.size.
func TestFetchReserveWatchOrders(t *testing.T) {
	stateIDs := []string{"h1", "h2", "h3", "h4", "h5"}

	var (
		hits       int
		gotMethod  string
		gotFilters []string
	)

	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		hits++
		gotMethod = r.Method
		gotFilters = append(gotFilters, r.URL.Query().Get("filter"))

		if r.URL.Path != customerOrderTestPath {
			t.Errorf("path = %s, want /entity/customerorder", r.URL.Path)
		}

		offset := 0
		_, _ = fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
		rows := make([]map[string]string, 0, 1000)
		for i := offset; i < 1500 && len(rows) < 1000; i++ {
			rows = append(rows, map[string]string{"id": fmt.Sprintf("o-%d", i), "name": fmt.Sprintf("%d", i)})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]any{"size": 1500},
			"rows": rows,
		})
	})

	orders, err := msac.FetchReserveWatchOrders(context.Background(), 7, stateIDs)
	if err != nil {
		t.Fatalf("FetchReserveWatchOrders() error: %v", err)
	}
	if len(orders) != 1500 {
		t.Fatalf("orders = %d, want 1500 (полная пагинация)", len(orders))
	}
	if hits != 2 {
		t.Errorf("HTTP hits = %d, want 2 (offset 0 и 1000)", hits)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s", gotMethod)
	}
	if orders[0].ID != "o-0" || orders[1499].ID != "o-1499" {
		t.Errorf("края пагинации: %s .. %s", orders[0].ID, orders[len(orders)-1].ID)
	}

	filter := gotFilters[0]
	if !strings.Contains(filter, "deliveryPlannedMoment>=20") {
		t.Errorf("фильтр без deliveryPlannedMoment: %q", filter)
	}
	if !strings.Contains(filter, " 00:00:00.000;") {
		t.Errorf("начало окна не полночь с миллисекундами: %q", filter)
	}
	for _, id := range stateIDs {
		want := "state=" + msac.refHref("customerorder/metadata/states", id)
		if strings.Count(filter, want) != 1 {
			t.Errorf("фильтр без статуса %s (href %s): %q", id, want, filter)
		}
	}
	// Повторение поля через ';' (OR), а не '||' — '||' молча режет выборку.
	if strings.Count(filter, ";state=") != 5 {
		t.Errorf("state в фильтре не повторены через ';': %q", filter)
	}
	if strings.Contains(filter, "||") {
		t.Errorf("в фильтре не должно быть '||': %q", filter)
	}
}

// TestFetchReserveWatchOrdersEmptyStates — без статусов метод не ходит в МС.
func TestFetchReserveWatchOrdersEmptyStates(t *testing.T) {
	var hits int
	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		hits++
		w.WriteHeader(http.StatusOK)
	})

	if _, err := msac.FetchReserveWatchOrders(context.Background(), 7, nil); err == nil {
		t.Fatal("ожидалась ошибка без статусов")
	}
	if hits != 0 {
		t.Errorf("HTTP hits = %d, want 0", hits)
	}
}

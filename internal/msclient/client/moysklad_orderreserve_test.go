package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/msclient/workerpool"
)

const (
	orderIDTest = "053b3dfc-926b-11f1-0a80-135d00113455"
	cancelledID = "8737d8a5-c0b9-11e3-ac8e-002590a28eca"
	// whAuthHeader / othAuthHeader — Authorization тестовых ключей пула
	// (складской / общий); goconst: не дублировать литералы по пакету.
	whAuthHeader  = "Bearer key-wh"
	othAuthHeader = "Bearer key-oth"
	// orderRawTest — сырое тело GET заказа: state (отменён) + 3 позиции,
	// две из которых в резерве.
	orderRawTest = `{"id":"` + orderIDTest + `","name":"19191","state":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/` + cancelledID + `"}},` +
		`"positions":{"rows":[` +
		`{"id":"pos-1","quantity":0.657,"reserve":0.657,"price":1000.0,"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/a02a"}}},` +
		`{"id":"pos-2","quantity":2,"reserve":0,"price":500.0,"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/d00d"}}},` +
		`{"id":"pos-3","quantity":0.5,"reserve":0.5,"price":2000.0,"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/b00b"}}}` +
		`]}}`
)

// newReserveTestClient поднимает httptest-сервер и клиент на нём (воркерпул
// валидирует ключ отдельным запросом к организации — на него отвечает orgOK).
func newReserveTestClient(t *testing.T, handler http.HandlerFunc) *MSAPIClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	msCfg := &config.MSConfig{
		URLstart:         server.URL + "/entity/",
		AuthHeader:       "Bearer",
		Refs:             &config.MSRefs{OrgID: "org-test"},
		WarehouseAPIKEYS: []config.MSWorker{{Name: "wh-worker", APIKey: "key-wh"}},
		OthersAPIKEYS:    []config.MSWorker{{Name: "oth-worker", APIKey: "key-oth"}},
		TimeSpan:         time.Second,
		RequestCap:       1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	t.Cleanup(pool.Stop)

	return &MSAPIClient{workerpool: pool, msConfig: msCfg}
}

// orgOK отвечает на проверку ключей пулом (GET организации) и сообщает,
// обработан ли запрос.
func orgOK(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == orgTestPath {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"org-test"}`))
		return true
	}
	return false
}

// TestClearOrderReserves — GET заказа (общие ключи) → PUT (складские ключи)
// с обнулённым reserve у всех позиций; прочие поля строк и тела не тронуты.
func TestClearOrderReserves(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	var putBody []byte

	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(orderRawTest))
		case http.MethodPut:
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			gotMethod = r.Method
			putBody = make([]byte, r.ContentLength)
			_, _ = r.Body.Read(putBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	if err := msac.ClearOrderReserves(context.Background(), orderIDTest); err != nil {
		t.Fatalf("ClearOrderReserves() error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("ожидался PUT, got %s", gotMethod)
	}
	if gotPath != "/entity/customerorder/"+orderIDTest {
		t.Errorf("path = %s, want customerorder endpoint", gotPath)
	}
	if gotAuth != whAuthHeader {
		t.Errorf("Authorization = %q, want складской ключ (SubmitWarehouse)", gotAuth)
	}

	var body struct {
		Name      string           `json:"name"`
		State     map[string]any   `json:"state"`
		Positions []map[string]any `json:"positions"`
	}
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatalf("PUT body not json: %v", err)
	}
	if body.Name != "19191" {
		t.Errorf("эхо тела нарушено: name = %q", body.Name)
	}
	if len(body.Positions) != 3 {
		t.Fatalf("positions = %d строк, want 3", len(body.Positions))
	}
	for i, p := range body.Positions {
		if got, ok := p["reserve"].(float64); !ok || got != 0 {
			t.Errorf("position %d: reserve = %v, want 0", i, p["reserve"])
		}
		if p["quantity"] == nil || p["id"] == nil || p["price"] == nil {
			t.Errorf("position %d: потеряны поля строки (quantity/id/price): %v", i, p)
		}
	}
}

// TestClearOrderReserves_NoReserveSkipsPut — резерва нет: PUT не уходит, ошибки нет.
func TestClearOrderReserves_NoReserveSkipsPut(t *testing.T) {
	putCalled := false
	raw := strings.ReplaceAll(orderRawTest, `"reserve":0.657`, `"reserve":0`)
	raw = strings.ReplaceAll(raw, `"reserve":0.5`, `"reserve":0`)

	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		if r.Method == http.MethodPut {
			putCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	})

	if err := msac.ClearOrderReserves(context.Background(), orderIDTest); err != nil {
		t.Fatalf("ClearOrderReserves() error: %v", err)
	}
	if putCalled {
		t.Fatal("PUT ушёл при нулевом резерве — менять нечего")
	}
}

// TestFetchOrderState — id статуса из state.meta.href заказа.
func TestFetchOrderState(t *testing.T) {
	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(orderRawTest))
	})

	stateID, err := msac.FetchOrderState(context.Background(), orderIDTest)
	if err != nil {
		t.Fatalf("FetchOrderState() error: %v", err)
	}
	if stateID != cancelledID {
		t.Errorf("state = %q, want %q", stateID, cancelledID)
	}
}

// TestFetchOrderState_NoState — у заказа нет state: пустая строка, не ошибка.
func TestFetchOrderState_NoState(t *testing.T) {
	msac := newReserveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if orgOK(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + orderIDTest + `","name":"19191"}`))
	})

	stateID, err := msac.FetchOrderState(context.Background(), orderIDTest)
	if err != nil {
		t.Fatalf("FetchOrderState() error: %v", err)
	}
	if stateID != "" {
		t.Errorf("state = %q, want пусто", stateID)
	}
}

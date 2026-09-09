package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/msclient/workerpool"
)

// TestUpdateCustomerOrderUsesWarehouseKey — правки заказов (PUT customerorder)
// уходят строго под ключами склада (SubmitWarehouse), а не через общий пул:
// в аудите МС такие изменения должны быть помечены складским uid, чтобы
// наблюдатель аудита отличал их от ручных правок менеджера.
func TestUpdateCustomerOrderUsesWarehouseKey(t *testing.T) {
	var gotAuth, gotPath, gotMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/entity/organization/org-test" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"org-test"}`))
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
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

	msac := &MSAPIClient{workerpool: pool, msConfig: msCfg}

	err := msac.UpdateCustomerOrder(context.Background(), "053b3dfc-926b-11f1-0a80-135d00113455", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("UpdateCustomerOrder() error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/entity/customerorder/053b3dfc-926b-11f1-0a80-135d00113455" {
		t.Errorf("path = %s, want customerorder endpoint", gotPath)
	}
	if gotAuth != whAuthHeader {
		t.Errorf("Authorization = %q, want warehouse key (Bearer key-wh), got others key", gotAuth)
	}
}

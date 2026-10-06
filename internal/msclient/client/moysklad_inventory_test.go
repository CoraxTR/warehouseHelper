package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/msclient/workerpool"
)

// inventoryRequestBody — тело запроса CreateInventory для проверок в тестах.
type inventoryRequestBody struct {
	Organization struct {
		Meta struct {
			HREF      string `json:"href"`
			Type      string `json:"type"`
			MediaType string `json:"mediaType"`
		} `json:"meta"`
	} `json:"organization"`
	Store struct {
		Meta struct {
			HREF string `json:"href"`
		} `json:"meta"`
	} `json:"store"`
	Positions []struct {
		Assortment struct {
			Meta struct {
				HREF string `json:"href"`
				Type string `json:"type"`
			} `json:"meta"`
		} `json:"assortment"`
		Quantity float64 `json:"quantity"`
		Price    *int64  `json:"price"`
	} `json:"positions"`
}

// newInventoryTestClient поднимает httptest-сервер и клиент на нём (воркерпул
// валидирует ключ отдельным GET на organization — на него отвечаем пустым JSON).
// storeID пустой — воспроизводит незаданный MSAPI_STORE_ID.
func newInventoryTestClient(
	t *testing.T,
	storeID string,
	handler http.HandlerFunc,
) (*MSAPIClient, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/entity/organization/org-test" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"org-test"}`))
			return
		}
		if handler != nil {
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)

	msCfg := &config.MSConfig{
		URLstart:         server.URL + "/entity/",
		AuthHeader:       "Bearer",
		Refs:             &config.MSRefs{OrgID: "org-test", StoreID: storeID},
		WarehouseAPIKEYS: []config.MSWorker{{Name: "wh-worker", APIKey: "key-wh"}},
		OthersAPIKEYS:    []config.MSWorker{{Name: "oth-worker", APIKey: "key-oth"}},
		TimeSpan:         time.Second,
		RequestCap:       1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	t.Cleanup(pool.Stop)

	return &MSAPIClient{workerpool: pool, msConfig: msCfg}, server
}

// TestCreateInventory — успешное создание документа: POST на /entity/inventory,
// организация/склад из конфига, все позиции с quantity и price (в т.ч. нулём),
// из ответа МС возвращаются ID/Name/URL.
func TestCreateInventory(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)

	msac, server := newInventoryTestClient(t, "store-test", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"id":"doc-1","name":"Инвентаризация № 1","meta":{"uuidHref":"https://api.moysklad.ru/app/#inventory/edit?id=doc-1"}}`,
		))
	})

	doc, err := msac.CreateInventory(context.Background(), []MSInventoryPosition{
		{AssortmentID: "prod-1", Quantity: 12.345, PriceKop: 45000},
		{AssortmentID: "prod-2", Quantity: 3, PriceKop: 0},
	})
	if err != nil {
		t.Fatalf("CreateInventory() error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/entity/inventory" {
		t.Errorf("path = %s, want /entity/inventory", gotPath)
	}
	if doc.ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", doc.ID)
	}
	if doc.Name != "Инвентаризация № 1" {
		t.Errorf("Name = %q, want «Инвентаризация № 1»", doc.Name)
	}
	if doc.URL != "https://api.moysklad.ru/app/#inventory/edit?id=doc-1" {
		t.Errorf("URL = %q, want uuidHref из ответа", doc.URL)
	}

	var sent inventoryRequestBody
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("не разобрал тело запроса: %v", err)
	}

	if want := server.URL + "/entity/organization/org-test"; sent.Organization.Meta.HREF != want {
		t.Errorf("organization href = %q, want %q", sent.Organization.Meta.HREF, want)
	}
	if sent.Organization.Meta.Type != "organization" {
		t.Errorf("organization type = %q, want organization", sent.Organization.Meta.Type)
	}
	if sent.Organization.Meta.MediaType != MSApplicationJSON {
		t.Errorf("organization mediaType = %q, want %q", sent.Organization.Meta.MediaType, MSApplicationJSON)
	}
	if want := server.URL + "/entity/store/store-test"; sent.Store.Meta.HREF != want {
		t.Errorf("store href = %q, want %q", sent.Store.Meta.HREF, want)
	}

	if len(sent.Positions) != 2 {
		t.Fatalf("positions = %d, want 2", len(sent.Positions))
	}
	if want := server.URL + "/entity/product/prod-1"; sent.Positions[0].Assortment.Meta.HREF != want {
		t.Errorf("positions[0].assortment href = %q, want %q", sent.Positions[0].Assortment.Meta.HREF, want)
	}
	if sent.Positions[0].Assortment.Meta.Type != "product" {
		t.Errorf("positions[0].assortment type = %q, want product", sent.Positions[0].Assortment.Meta.Type)
	}
	if sent.Positions[0].Quantity != 12.345 {
		t.Errorf("positions[0].quantity = %v, want 12.345", sent.Positions[0].Quantity)
	}
	if sent.Positions[0].Price == nil || *sent.Positions[0].Price != 45000 {
		t.Errorf("positions[0].price = %v, want 45000", sent.Positions[0].Price)
	}
	// price передаётся всегда, в том числе нулём (не nil — ключ присутствует).
	if sent.Positions[1].Price == nil {
		t.Error("positions[1].price отсутствует, want 0")
	} else if *sent.Positions[1].Price != 0 {
		t.Errorf("positions[1].price = %d, want 0", *sent.Positions[1].Price)
	}
}

// TestCreateInventoryAPIError — не-2xx от МС: ошибка несёт текст errors[].
func TestCreateInventoryAPIError(t *testing.T) {
	msac, _ := newInventoryTestClient(t, "store-test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"errors":[{"error":"Недостаточно прав для создания инвентаризации"}]}`))
	})

	_, err := msac.CreateInventory(context.Background(), nil)
	if err == nil {
		t.Fatal("CreateInventory() ожидалась ошибка, получили nil")
	}
	if !strings.Contains(err.Error(), "Недостаточно прав для создания инвентаризации") {
		t.Errorf("ошибка не содержит текст МС: %v", err)
	}

	var apiErr *MSAPIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ошибка не *MSAPIError: %v", err)
	}
	if apiErr.Code != http.StatusPreconditionFailed {
		t.Errorf("Code = %d, want %d", apiErr.Code, http.StatusPreconditionFailed)
	}
}

// TestCreateInventoryStoreNotConfigured — пустой Refs.StoreID: ошибка до
// запроса, в МС НИЧЕГО не уходит (счётчик обращений к /entity/inventory = 0).
func TestCreateInventoryStoreNotConfigured(t *testing.T) {
	var inventoryRequests int32

	msac, _ := newInventoryTestClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/entity/inventory" {
			atomic.AddInt32(&inventoryRequests, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	_, err := msac.CreateInventory(context.Background(), []MSInventoryPosition{
		{AssortmentID: "prod-1", Quantity: 1},
	})
	if err == nil {
		t.Fatal("CreateInventory() ожидалась ошибка, получили nil")
	}
	if !strings.Contains(err.Error(), "MSAPI_STORE_ID") {
		t.Errorf("ошибка не упоминает MSAPI_STORE_ID: %v", err)
	}
	if n := atomic.LoadInt32(&inventoryRequests); n != 0 {
		t.Errorf("обращений к /entity/inventory = %d, want 0 (запрос не должен уходить)", n)
	}
}

// TestInventoryStoreConfigured — true при заданном складе, false при пустом.
func TestInventoryStoreConfigured(t *testing.T) {
	configured, _ := newInventoryTestClient(t, "store-test", nil)
	if !configured.InventoryStoreConfigured() {
		t.Error("InventoryStoreConfigured() = false при заданном StoreID, want true")
	}

	empty, _ := newInventoryTestClient(t, "", nil)
	if empty.InventoryStoreConfigured() {
		t.Error("InventoryStoreConfigured() = true при пустом StoreID, want false")
	}
}

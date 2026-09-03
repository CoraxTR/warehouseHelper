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

// TestSearchCustomerOrdersByName — живой контур клиента: httptest-сервер +
// воркерпул. Проверяем URL (filter=name=<точное имя>, expand=agent), разбор
// строк (moment/deliveryPlannedMoment/agent.name при expand).
func TestSearchCustomerOrdersByName(t *testing.T) {
	var searchRequests int
	var gotFilter, gotExpand string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Запрос валидации ключа воркерпулом (entity/organization) не считаем.
		if r.URL.Path != "/entity/customerorder" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}

		searchRequests++
		gotFilter = r.URL.Query().Get("filter")
		gotExpand = r.URL.Query().Get("expand")

		list := MSFetchOrdersResponse{}
		list.Meta.Size = 2
		list.Rows = []MSOrder{
			{
				ID:                    "00023557-7e97-11e7-7a34-5acf0020c748",
				Name:                  "03969",
				Moment:                "2017-08-11 16:13:00.000",
				DeliveryPlannedMoment: "2017-08-12 16:14:00.000",
				Agent: MSAgent{
					Meta: MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/counterparty/cp-1"},
					Name: "ООО \"ШИРИН 2\"",
				},
			},
			{
				ID:     "00023557-7e97-11e7-7a34-5acf0020c749",
				Name:   "03969",
				Moment: "2018-01-05 09:00:00.000",
				Agent:  MSAgent{Name: "ИП Тестов"},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	msCfg := &config.MSConfig{
		URLstart:      server.URL + "/entity/",
		AuthHeader:    "Bearer",
		Refs:          &config.MSRefs{OrgID: "org-test"},
		OthersAPIKEYS: []config.MSWorker{{Name: "test-worker", APIKey: "test-key"}},
		TimeSpan:      time.Second,
		RequestCap:    1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	defer pool.Stop()

	msac := &MSAPIClient{workerpool: pool, msConfig: msCfg}

	rows, err := msac.SearchCustomerOrdersByName(context.Background(), "03969")
	if err != nil {
		t.Fatalf("SearchCustomerOrdersByName() error: %v", err)
	}

	if searchRequests != 1 {
		t.Errorf("HTTP-запросов поиска = %d, want 1", searchRequests)
	}
	if gotFilter != "name=03969" {
		t.Errorf("filter = %q, want name=03969", gotFilter)
	}
	if gotExpand != "agent" {
		t.Errorf("expand = %q, want agent", gotExpand)
	}

	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].Name != "03969" || rows[0].Agent.Name != "ООО \"ШИРИН 2\"" {
		t.Errorf("строка 0 не разобрана: name=%q agent=%q", rows[0].Name, rows[0].Agent.Name)
	}
	if rows[0].Moment != "2017-08-11 16:13:00.000" || rows[0].DeliveryPlannedMoment != "2017-08-12 16:14:00.000" {
		t.Errorf("моменты строки 0 не разобраны: moment=%q delivery=%q",
			rows[0].Moment, rows[0].DeliveryPlannedMoment)
	}
	if rows[1].Agent.Name != "ИП Тестов" {
		t.Errorf("agent второй строки = %q, want ИП Тестов", rows[1].Agent.Name)
	}
}

func TestSearchCustomerOrdersByNameEmptyName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	msCfg := &config.MSConfig{
		URLstart:      server.URL + "/entity/",
		AuthHeader:    "Bearer",
		Refs:          &config.MSRefs{OrgID: "org-test"},
		OthersAPIKEYS: []config.MSWorker{{Name: "test-worker", APIKey: "test-key"}},
		TimeSpan:      time.Second,
		RequestCap:    1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	defer pool.Stop()

	msac := &MSAPIClient{workerpool: pool, msConfig: msCfg}

	if _, err := msac.SearchCustomerOrdersByName(context.Background(), "  "); err == nil {
		t.Fatal("SearchCustomerOrdersByName() error = nil для пустого имени, want ошибку")
	}
}

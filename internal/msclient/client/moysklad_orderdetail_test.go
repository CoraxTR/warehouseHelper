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

// orderBody — тело заказа как его отдаёт МС на одиночный GET customerorder/{id}
// (поля верхнего уровня + meta-ссылки agent/positions; shipmentAddressFull —
// объект полного адреса).
func orderBody() MSOrder {
	return MSOrder{
		ID:                    "053b3dfc-926b-11f1-0a80-135d00113455",
		Name:                  "05685",
		Moment:                "2026-09-11 17:02:00.000",
		Description:           "самовывоз, уточнить по оплате",
		DeliveryPlannedMoment: "2026-09-13 17:19:00.000",
		ShipmentAddress:       "село Алабушево, 14-1",
		ShipmentAddressFull: MSAddressFull{
			AddInfo: "село Алабушево, 14-1\n2) Москва, Мантулинская улица, д. 9к4",
			Comment: "2) 2п,16эт,77кв",
		},
		Agent: MSAgent{
			Meta: MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/counterparty/f932612f-926a-11f1-0a80-005c0010ca3b"},
		},
		MSPositions: MSPositions{
			Meta: MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/053b3dfc-926b-11f1-0a80-135d00113455/positions"},
		},
	}
}

// positionsBody — тело ответа позиций заказа при expand=assortment: имя и
// внутренний код товара приезжают прямо в строке.
func positionsBody() MSPositions {
	return MSPositions{
		Meta: MSMeta{Size: 2},
		Rows: []MSPosition{
			{
				ID:       "pos-1",
				Quantity: 0.367,
				Price:    279000.0,
				Reserve:  0.0,
				Assortment: MSAssortment{
					Meta: MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/product/c01a9c62-0240-11e6-7a69-93a7000bc220"},
					Code: "00220002",
					Name: "Стейк Нью-Йорк АМТ (Frigorifico). Зам.",
				},
			},
			{
				ID:       "pos-2",
				Quantity: 3.0,
				Price:    50000.0,
				Reserve:  1.5,
				Assortment: MSAssortment{
					Meta: MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/product/c01a9c62-0240-11e6-7a69-93a7000bc221"},
					Code: "21110001",
					Name: "Соус терияки",
				},
			},
		},
	}
}

// newDetailTestClient поднимает httptest-сервер и клиент на нём (воркерпул
// валидирует ключ отдельным запросом — на него отвечаем пустым JSON).
func newDetailTestClient(t *testing.T, handler http.HandlerFunc) (*MSAPIClient, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/entity/organization/org-test" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"org-test"}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	msCfg := &config.MSConfig{
		URLstart:      server.URL + "/entity/",
		AuthHeader:    "Bearer",
		Refs:          &config.MSRefs{OrgID: "org-test"},
		OthersAPIKEYS: []config.MSWorker{{Name: "test-worker", APIKey: "test-key-1"}},
		TimeSpan:      time.Second,
		RequestCap:    1000,
	}

	pool := workerpool.NewMSWorkerPool(msCfg)
	t.Cleanup(pool.Stop)

	return &MSAPIClient{workerpool: pool, msConfig: msCfg}, server
}

// TestFetchOrderByID — лёгкий фетч заказа по id: поля верхнего уровня,
// полный адрес (объект), meta-ссылки агента и позиций для хопов.
func TestFetchOrderByID(t *testing.T) {
	var gotPath string

	msac, _ := newDetailTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(orderBody()); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	order, err := msac.FetchOrderByID(context.Background(), "053b3dfc-926b-11f1-0a80-135d00113455")
	if err != nil {
		t.Fatalf("FetchOrderByID() error: %v", err)
	}

	if gotPath != "/entity/customerorder/053b3dfc-926b-11f1-0a80-135d00113455" {
		t.Errorf("path = %q, want одиночный фетч customerorder/{id}", gotPath)
	}
	if order.Name != "05685" || order.Description != "самовывоз, уточнить по оплате" {
		t.Errorf("поля шапки не разобраны: name=%q description=%q", order.Name, order.Description)
	}
	if order.DeliveryPlannedMoment != "2026-09-13 17:19:00.000" {
		t.Errorf("deliveryPlannedMoment = %q", order.DeliveryPlannedMoment)
	}
	if order.ShipmentAddressFull.AddInfo == "" || order.ShipmentAddressFull.Comment != "2) 2п,16эт,77кв" {
		t.Errorf("shipmentAddressFull не разобран: %+v", order.ShipmentAddressFull)
	}
	if order.Agent.Meta.HREF == "" || order.MSPositions.Meta.HREF == "" {
		t.Error("meta-ссылки agent/positions для хопов не приехали")
	}
}

// TestFetchOrderPositionsByHREF — позиции с expand=assortment: в URL query,
// в строках — id, reserve и вложенный товар (code/name).
func TestFetchOrderPositionsByHREF(t *testing.T) {
	var gotPath, gotExpand string

	msac, server := newDetailTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotExpand = r.URL.Query().Get("expand")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(positionsBody()); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	order := orderBody()
	// positions.meta.href в тесте — адрес httptest-сервера, не живой МС
	// (в orderBody стоит реальный href из контрольного запроса).
	order.MSPositions.Meta.HREF = server.URL + "/entity/customerorder/053b3dfc-926b-11f1-0a80-135d00113455/positions"
	positions, err := msac.FetchOrderPositionsByHREF(context.Background(), &order)
	if err != nil {
		t.Fatalf("FetchOrderPositionsByHREF() error: %v", err)
	}

	if gotPath != "/entity/customerorder/053b3dfc-926b-11f1-0a80-135d00113455/positions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotExpand != "assortment" {
		t.Errorf("expand = %q, want assortment", gotExpand)
	}
	if len(positions) != 2 {
		t.Fatalf("len(positions) = %d, want 2", len(positions))
	}

	p := positions[0]
	if p.ID != "pos-1" || p.Quantity != 0.367 || p.Price != 279000.0 || p.Reserve != 0.0 {
		t.Errorf("позиция 0 не разобрана: %+v", p)
	}
	if p.Assortment.Code != "00220002" || p.Assortment.Name != "Стейк Нью-Йорк АМТ (Frigorifico). Зам." {
		t.Errorf("assortment позиции 0 не разобран: %+v", p.Assortment)
	}

	if positions[1].Reserve != 1.5 || positions[1].Assortment.Code != "21110001" {
		t.Errorf("позиция 1 (резерв/код) не разобрана: %+v", positions[1])
	}
}

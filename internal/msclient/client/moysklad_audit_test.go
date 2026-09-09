package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/msclient/workerpool"
)

// Фикстуры — реальные ответы живого API (проба 08.09.2026): глобальный лист
// audit (2 события из ТЗ: удаление 01726 и отмена 19379) и раскрытие events
// отмены (884ff854...). Используются и как эталон структуры.

const auditListFixture = `{
  "meta": {"href": "https://api.moysklad.ru/api/remap/1.2/audit?filter=eventType%3dupdate", "type": "audit", "size": 2, "limit": 25, "offset": 0},
  "rows": [
    {"id": "884ff854-abc1-11f1-0a80-106a00019ed6", "meta": {"href": "https://api.moysklad.ru/api/remap/1.2/audit/884ff854-abc1-11f1-0a80-106a00019ed6"}, "moment": "2026-09-08 23:11:52.918", "entityType": "customerorder", "eventType": "update", "uid": "sklad@steakhome", "source": "app", "objectCount": 1, "events": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/audit/884ff854-abc1-11f1-0a80-106a00019ed6/events"}}},
    {"id": "83535fe1-abc1-11f1-0a80-0cfc000184c6", "meta": {"href": "https://api.moysklad.ru/api/remap/1.2/audit/83535fe1-abc1-11f1-0a80-0cfc000184c6"}, "moment": "2026-09-08 23:11:44.554", "entityType": "customerorder", "eventType": "update", "uid": "sklad@steakhome", "source": "app", "objectCount": 1, "events": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/audit/83535fe1-abc1-11f1-0a80-0cfc000184c6/events"}}}
  ]
}`

const auditDetailFixture = `{
  "meta": {"href": "https://api.moysklad.ru/api/remap/1.2/audit/884ff854-abc1-11f1-0a80-106a00019ed6/events", "type": "auditevent", "size": 1, "limit": 25, "offset": 0},
  "rows": [{
    "source": "app",
    "eventType": "update",
    "entityType": "customerorder",
    "uid": "sklad@steakhome",
    "moment": "2026-09-08 23:11:52.918",
    "diff": {
      "state": {
        "oldValue": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/785d7841-1ac2-11f0-0a80-071f000f6177"}, "name": "РефГо"},
        "newValue": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/8737d8a5-c0b9-11e3-ac8e-002590a28eca"}, "name": "Отменен"}
      }
    },
    "name": "19379",
    "additionalInfo": "от 2026-09-05 23:10:00 Тестовый Борис 5435.0 RUB",
    "audit": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/audit/884ff854-abc1-11f1-0a80-106a00019ed6", "type": "audit"}},
    "entity": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/0de266d9-2c42-11f0-0a80-0cd40022203a", "type": "customerorder"}}
  }]
}`

func TestParseAuditMoment(t *testing.T) {
	got, err := ParseAuditMoment("2026-09-08 23:11:52.918")
	if err != nil {
		t.Fatalf("ParseAuditMoment() error: %v", err)
	}

	want := time.Date(2026, time.September, 8, 20, 11, 52, 918000000, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ParseAuditMoment() = %v, want %v (МСК 23:11:52.918 → UTC 20:11:52.918)", got, want)
	}
}

func TestParseAuditMomentInvalid(t *testing.T) {
	if _, err := ParseAuditMoment("мусор"); err == nil {
		t.Fatal("ParseAuditMoment() error = nil, want error on garbage")
	}
}

// newAuditTestClient — как newDetailTestClient, но с warehouse-ключом: audit
// закрыт для части общих ключей, все audit-запросы идут строго под ключами
// склада (SubmitWarehouse) — воркерпулу нужен warehouse-воркер.
func newAuditTestClient(t *testing.T, handler http.HandlerFunc) *MSAPIClient {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == orgTestPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"org-test"}`))
			return
		}
		handler(w, r)
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

	return &MSAPIClient{workerpool: pool, msConfig: msCfg}
}

// orgTestPath — путь валидации ключа в тестовых серверах (goconst: литерал
// повторяется по пакету в хелперах).
const orgTestPath = "/entity/organization/org-test"

func TestFetchAuditPage(t *testing.T) {
	var gotFilter, gotLimit, gotOffset, gotAuth string

	msac := newAuditTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audit" {
			t.Errorf("path = %s, want /audit", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotFilter = r.URL.Query().Get("filter")
		gotLimit = r.URL.Query().Get("limit")
		gotOffset = r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(json.RawMessage(auditListFixture)); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	since := time.Date(2026, time.September, 8, 20, 10, 0, 0, time.UTC) // 23:10 МСК
	rows, size, err := msac.FetchAuditPage(context.Background(), since, 0)
	if err != nil {
		t.Fatalf("FetchAuditPage() error: %v", err)
	}

	if size != 2 {
		t.Errorf("size = %d, want 2", size)
	}
	// Audit закрыт для части общих ключей — запрос должен уйти под warehouse-ключом.
	if gotAuth != whAuthHeader {
		t.Errorf("Authorization = %q, want warehouse key (Bearer key-wh)", gotAuth)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}

	// Фильтр: момент в TZ учётки (МСК), с миллисекундами (край + 1мс
	// выталкивает краевое событие; МС фильтрует с точностью до мс).
	if gotFilter != "eventType=update;moment>=2026-09-08 23:10:00.000" {
		t.Errorf("filter = %q, want eventType=update;moment>=2026-09-08 23:10:00.000", gotFilter)
	}
	if gotLimit != "25" || gotOffset != "0" {
		t.Errorf("limit/offset = %s/%s, want 25/0", gotLimit, gotOffset)
	}

	if rows[0].ID != "884ff854-abc1-11f1-0a80-106a00019ed6" {
		t.Errorf("rows[0].ID = %s, want audit id отмены", rows[0].ID)
	}
	if rows[0].Source == nil || *rows[0].Source != "app" {
		t.Errorf("rows[0].Source = %v, want app (веб-правка)", rows[0].Source)
	}
	if rows[0].EntityType != "customerorder" || rows[0].EventType != "update" {
		t.Errorf("rows[0] entityType/eventType = %s/%s, want customerorder/update", rows[0].EntityType, rows[0].EventType)
	}
}

func TestFetchAuditPageURLEncoding(t *testing.T) {
	// Закодированные ; = > в filter МС принимает (проверено на живом API) —
	// Query().Encode() кодирует именно так.
	u, err := url.Parse("https://api.moysklad.ru/api/remap/1.2/audit")
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("filter", "eventType=update;moment>=2026-09-08 23:10:00")
	u.RawQuery = q.Encode()

	if !strings.Contains(u.String(), "filter=eventType%3Dupdate%3Bmoment%3E%3D2026-09-08+23%3A10%3A00") {
		t.Errorf("encoded URL = %s, want закодированные ; = >", u.String())
	}
}

func TestFetchAuditDetail(t *testing.T) {
	var gotPath string

	msac := newAuditTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(json.RawMessage(auditDetailFixture)); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	rows, err := msac.FetchAuditDetail(context.Background(), "884ff854-abc1-11f1-0a80-106a00019ed6")
	if err != nil {
		t.Fatalf("FetchAuditDetail() error: %v", err)
	}

	if gotPath != "/audit/884ff854-abc1-11f1-0a80-106a00019ed6/events" {
		t.Errorf("path = %s, want /audit/<id>/events", gotPath)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}

	row := rows[0]
	if row.Name != "19379" || row.EntityType != "customerorder" {
		t.Errorf("row name/entityType = %s/%s, want 19379/customerorder", row.Name, row.EntityType)
	}
	if row.Diff.State == nil {
		t.Fatal("row.Diff.State = nil, want state-change (отмена)")
	}
	if row.Diff.State.NewValue == nil || row.Diff.State.NewValue.Name != "Отменен" {
		t.Errorf("state.NewValue = %+v, want Отменен", row.Diff.State.NewValue)
	}
	if !strings.HasSuffix(row.Entity.Meta.HREF, "/0de266d9-2c42-11f0-0a80-0cd40022203a") {
		t.Errorf("entity href = %s, want заказ 0de266d9", row.Entity.Meta.HREF)
	}
}

func TestFetchOrderPositions(t *testing.T) {
	// Реальные позиции отменённого заказа 19379 (сопутка, reserve=0) +
	// весовая позиция с reserve==quantity (отложенный кусок).
	const positionsFixture = `{
	  "meta": {"size": 3, "limit": 1000, "offset": 0},
	  "rows": [
	    {"id": "p1", "quantity": 1.0, "reserve": 0.0, "assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/10080001-0000-0000-0000-000000000001"}, "name": "Соус Jack Daniel's Honey BBQ. 553 гр."}},
	    {"id": "p2", "quantity": 0.657, "reserve": 0.657, "assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/a02a9121-7ef5-11e5-7a40-e897001b4cc6"}, "name": "Стейк Чак ролл Праймбиф. Охл."}},
	    {"id": "p3", "quantity": 1.0, "reserve": 0.0, "assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/10080001-0000-0000-0000-000000000002"}, "name": "Доставка"}}
	  ]
	}`

	var gotPath string
	msac, _ := newDetailTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if got := r.URL.Query().Get("expand"); got != "assortment" {
			t.Errorf("expand = %q, want assortment", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(json.RawMessage(positionsFixture)); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	positions, err := msac.FetchOrderPositions(context.Background(), "0de266d9-2c42-11f0-0a80-0cd40022203a")
	if err != nil {
		t.Fatalf("FetchOrderPositions() error: %v", err)
	}

	if gotPath != "/entity/customerorder/0de266d9-2c42-11f0-0a80-0cd40022203a/positions" {
		t.Errorf("path = %s, want customerorder positions endpoint", gotPath)
	}
	if len(positions) != 3 {
		t.Fatalf("len(positions) = %d, want 3", len(positions))
	}
	if positions[1].Quantity != 0.657 || positions[1].Reserve != 0.657 {
		t.Errorf("positions[1] qty/reserve = %v/%v, want отложенный кусок 0.657/0.657", positions[1].Quantity, positions[1].Reserve)
	}
	if positions[1].Assortment.Name != "Стейк Чак ролл Праймбиф. Охл." {
		t.Errorf("positions[1] name = %q, want имя из expand=assortment", positions[1].Assortment.Name)
	}
}

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/config"
)

// Тестовые id товаров (порядок в фильтре должен сохраняться как передан).
const (
	priceID1 = "11111111-1111-1111-1111-111111111111"
	priceID2 = "22222222-2222-2222-2222-222222222222"
)

// TestProductPricesByIDsEndpoint — сборка URL батча: filter из повторяющихся
// условий "id=<uuid>" через ';' (ИЛИ по докам МС), значение urlencoded
// (id%3D...%3B...), НЕ через запятую (МС 400/1014), порядок id сохранён.
func TestProductPricesByIDsEndpoint(t *testing.T) {
	msac := &MSAPIClient{msConfig: &config.MSConfig{
		URLstart: "https://api.moysklad.ru/api/remap/1.2/entity/",
	}}

	got, err := msac.productPricesByIDsEndpoint([]string{priceID1, priceID2}, 0, productPricesPageLimit)
	if err != nil {
		t.Fatalf("productPricesByIDsEndpoint() error: %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse(%q) error: %v", got, err)
	}

	if u.Path != "/api/remap/1.2/entity/product" {
		t.Errorf("path = %q, want /api/remap/1.2/entity/product", u.Path)
	}

	// Сырой query: url.Values.Encode() кодирует '=' как %3D, ';' как %3B.
	if !strings.Contains(u.RawQuery, "id%3D"+priceID1) {
		t.Errorf("raw query %q не содержит id%%3D<uuid> (urlencoded filter)", u.RawQuery)
	}
	if strings.Contains(u.RawQuery, "%2C") || strings.Contains(u.RawQuery, ",") {
		t.Errorf("raw query %q содержит запятую — список через запятую МС отклоняет (400/1014)", u.RawQuery)
	}

	q := u.Query()
	want := "id=" + priceID1 + ";id=" + priceID2
	if q.Get("filter") != want {
		t.Errorf("filter = %q, want %q (порядок id сохранён)", q.Get("filter"), want)
	}
	if q.Get("limit") != "1000" {
		t.Errorf("limit = %q, want 1000", q.Get("limit"))
	}
	if q.Get("offset") != "0" {
		t.Errorf("offset = %q, want 0", q.Get("offset"))
	}
}

// TestProductPricesSinceEndpoint — сборка URL инкремента: filter
// "updated>=<момент>", момент — в TZ учётки (МСК), urlencoded (updated%3E%3D).
func TestProductPricesSinceEndpoint(t *testing.T) {
	msac := &MSAPIClient{msConfig: &config.MSConfig{
		URLstart: "https://api.moysklad.ru/api/remap/1.2/entity/",
	}}

	// 09:00 UTC == 12:00 МСК (UTC+3).
	since := time.Date(2026, time.October, 5, 9, 0, 0, 0, time.UTC)

	got, err := msac.productPricesSinceEndpoint(since, 0, productPricesPageLimit)
	if err != nil {
		t.Fatalf("productPricesSinceEndpoint() error: %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse(%q) error: %v", got, err)
	}

	if !strings.Contains(u.RawQuery, "updated%3E%3D") {
		t.Errorf("raw query %q не содержит updated%%3E%%3D (urlencoded >)", u.RawQuery)
	}

	if want := "updated>=2026-10-05 12:00:00"; u.Query().Get("filter") != want {
		t.Errorf("filter = %q, want %q (момент в МСК, не UTC)", u.Query().Get("filter"), want)
	}

	if u.Query().Get("limit") != "1000" || u.Query().Get("offset") != "0" {
		t.Errorf("пагинация = limit %q offset %q, want 1000/0",
			u.Query().Get("limit"), u.Query().Get("offset"))
	}
}

// TestParseMSProductPriceParentVat — useParentVat=true: МС НЕ отдаёт полей НДС,
// EffectiveVat/EffectiveVatEnabled должны остаться nil (не падать), цены — разобраться.
func TestParseMSProductPriceParentVat(t *testing.T) {
	row := json.RawMessage(`{
		"id": "` + priceID1 + `",
		"buyPrice": {"value": 315000.0, "currency": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/currency/x", "type": "currency", "mediaType": "application/json"}}},
		"salePrices": [{"value": 549000.0, "priceType": {"id": "pt-1", "name": "Цена продажи", "meta": {"href": "https://api.moysklad.ru/api/remap/1.2/context/companysettings/pricetype/pt-1", "type": "pricetype", "mediaType": "application/json"}}}],
		"useParentVat": true
	}`)

	got, err := parseMSProductPrice(row)
	if err != nil {
		t.Fatalf("parseMSProductPrice() error: %v", err)
	}

	if got.ID != priceID1 {
		t.Errorf("ID = %q, want %q", got.ID, priceID1)
	}
	if !got.UseParentVat {
		t.Error("UseParentVat = false, want true")
	}
	if got.EffectiveVat != nil {
		t.Errorf("EffectiveVat = %d, want nil (при useParentVat поля НДС в ответе нет)", *got.EffectiveVat)
	}
	if got.EffectiveVatEnabled != nil {
		t.Errorf("EffectiveVatEnabled = %v, want nil (при useParentVat поля НДС в ответе нет)", *got.EffectiveVatEnabled)
	}
	if got.BuyPrice == nil || *got.BuyPrice != 315000 {
		t.Errorf("BuyPrice = %v, want 315000", got.BuyPrice)
	}
	if got.SalePrice == nil || *got.SalePrice != 549000 {
		t.Errorf("SalePrice = %v, want 549000", got.SalePrice)
	}
}

// TestParseMSProductPriceNoPrices — строка без buyPrice/salePrices: обе цены
// nil, разбор не паникует. Заодно — НДС-поля как указатели (10/true) и округление.
func TestParseMSProductPriceNoPrices(t *testing.T) {
	row := json.RawMessage(`{
		"id": "` + priceID2 + `",
		"effectiveVat": 10,
		"effectiveVatEnabled": true,
		"useParentVat": false
	}`)

	got, err := parseMSProductPrice(row)
	if err != nil {
		t.Fatalf("parseMSProductPrice() error: %v", err)
	}

	if got.BuyPrice != nil {
		t.Errorf("BuyPrice = %v, want nil (поля нет)", *got.BuyPrice)
	}
	if got.SalePrice != nil {
		t.Errorf("SalePrice = %v, want nil (поля нет)", *got.SalePrice)
	}
	if got.EffectiveVat == nil || *got.EffectiveVat != 10 {
		t.Errorf("EffectiveVat = %v, want 10", got.EffectiveVat)
	}
	if got.EffectiveVatEnabled == nil || !*got.EffectiveVatEnabled {
		t.Errorf("EffectiveVatEnabled = %v, want true", got.EffectiveVatEnabled)
	}

	// Пустой salePrices (не отсутствие) — тоже nil, без паники.
	empty, err := parseMSProductPrice(json.RawMessage(`{"id":"x","salePrices":[]}`))
	if err != nil {
		t.Fatalf("parseMSProductPrice(empty salePrices) error: %v", err)
	}
	if empty.SalePrice != nil {
		t.Errorf("SalePrice = %v, want nil (пустой salePrices)", *empty.SalePrice)
	}
}

// TestMSProductPriceRounding — значения приходят Float, кладём в копейки с
// math.Round (0.5 — от нуля).
func TestMSProductPriceRounding(t *testing.T) {
	got, err := parseMSProductPrice(json.RawMessage(`{
		"id": "x",
		"buyPrice": {"value": 100.5},
		"salePrices": [{"value": 200.4}]
	}`))
	if err != nil {
		t.Fatalf("parseMSProductPrice() error: %v", err)
	}

	if got.BuyPrice == nil || *got.BuyPrice != 101 {
		t.Errorf("BuyPrice = %v, want 101 (round 100.5)", got.BuyPrice)
	}
	if got.SalePrice == nil || *got.SalePrice != 200 {
		t.Errorf("SalePrice = %v, want 200 (round 200.4)", got.SalePrice)
	}
}

// TestProductPriceFrom — экспортированное правило «сырой товар МС → копейки»:
// им пользуются и фоновый обновитель (через parseMSProductPrice), и выгрузка
// каталога goods. Проверяем не разбор JSON, а саму модель: округление до
// копеек, nil у отсутствующих цен, выбор первого salePrices и перенос НДС.
func TestProductPriceFrom(t *testing.T) {
	vat, vatEnabled := 22, true

	t.Run("копейки, округление и НДС", func(t *testing.T) {
		src := MSProduct{
			ID:                  priceID1,
			BuyPrice:            &MSBuyPrice{Value: 315000.5},
			SalePrices:          []MSSalePrice{{Value: 549000.4}},
			EffectiveVat:        &vat,
			EffectiveVatEnabled: &vatEnabled,
		}

		got := ProductPriceFrom(src)

		if got.ID != priceID1 {
			t.Errorf("ID = %q, want %q", got.ID, priceID1)
		}
		if got.BuyPrice == nil || *got.BuyPrice != 315001 {
			t.Errorf("BuyPrice = %v, want 315001 (round 315000.5)", got.BuyPrice)
		}
		if got.SalePrice == nil || *got.SalePrice != 549000 {
			t.Errorf("SalePrice = %v, want 549000 (round 549000.4)", got.SalePrice)
		}
		if got.EffectiveVat == nil || *got.EffectiveVat != 22 {
			t.Errorf("EffectiveVat = %v, want 22", got.EffectiveVat)
		}
		if got.EffectiveVatEnabled == nil || !*got.EffectiveVatEnabled {
			t.Errorf("EffectiveVatEnabled = %v, want true", got.EffectiveVatEnabled)
		}
	})

	t.Run("нет цен → nil, разбор не падает", func(t *testing.T) {
		got := ProductPriceFrom(MSProduct{ID: priceID2, UseParentVat: true})

		if got.BuyPrice != nil || got.SalePrice != nil {
			t.Errorf("BuyPrice/SalePrice = %v/%v, want nil (полей нет)", got.BuyPrice, got.SalePrice)
		}
		if got.EffectiveVat != nil || got.EffectiveVatEnabled != nil {
			t.Errorf("НДС = %v/%v, want nil (useParentVat — полей нет)", got.EffectiveVat, got.EffectiveVatEnabled)
		}
		if !got.UseParentVat {
			t.Error("UseParentVat потерян при переносе")
		}
	})

	t.Run("несколько salePrices → берём первый (у нас один тип цены)", func(t *testing.T) {
		got := ProductPriceFrom(MSProduct{SalePrices: []MSSalePrice{{Value: 100}, {Value: 200}}})

		if got.SalePrice == nil || *got.SalePrice != 100 {
			t.Errorf("SalePrice = %v, want 100 (первый элемент)", got.SalePrice)
		}
	})
}

// TestFetchProductPricesByIDsEmpty — пустой список id: запроса к МС нет вовсе.
func TestFetchProductPricesByIDsEmpty(t *testing.T) {
	requests := 0

	msac, _ := newDetailTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		requests++
	})

	for _, ids := range [][]string{nil, {}} {
		got, err := msac.FetchProductPricesByIDs(context.Background(), ids)
		if err != nil {
			t.Fatalf("FetchProductPricesByIDs(%v) error: %v", ids, err)
		}
		if got != nil {
			t.Errorf("FetchProductPricesByIDs(%v) = %v, want nil", ids, got)
		}
	}

	if requests != 0 {
		t.Errorf("HTTP-запросов = %d, want 0 (пустой список — без запроса)", requests)
	}
}

// TestFetchProductPricesByIDs — живой контур: реальный запрос через воркерпул,
// проверяем filter (повторяющиеся id=, без запятой) и разбор строк.
func TestFetchProductPricesByIDs(t *testing.T) {
	var gotFilter string
	requests := 0

	msac, _ := newDetailTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/entity/product" {
			t.Errorf("path = %s, want /entity/product", r.URL.Path)
			return
		}

		requests++
		gotFilter = r.URL.Query().Get("filter")

		raw := json.RawMessage(`{"meta":{"size":2,"limit":1000,"offset":0},"rows":[
			{"id":"` + priceID1 + `","buyPrice":{"value":315000.0},"salePrices":[{"value":549000.0}],"effectiveVat":22,"effectiveVatEnabled":true},
			{"id":"` + priceID2 + `","useParentVat":true}
		]}`)

		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(raw); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	})

	got, err := msac.FetchProductPricesByIDs(context.Background(), []string{priceID1, priceID2})
	if err != nil {
		t.Fatalf("FetchProductPricesByIDs() error: %v", err)
	}

	if requests != 1 {
		t.Errorf("HTTP-запросов = %d, want 1", requests)
	}

	wantFilter := "id=" + priceID1 + ";id=" + priceID2
	if gotFilter != wantFilter {
		t.Errorf("filter = %q, want %q", gotFilter, wantFilter)
	}

	if len(got) != 2 {
		t.Fatalf("len(prices) = %d, want 2", len(got))
	}
	if got[0].BuyPrice == nil || *got[0].BuyPrice != 315000 {
		t.Errorf("prices[0].BuyPrice = %v, want 315000", got[0].BuyPrice)
	}
	if got[0].SalePrice == nil || *got[0].SalePrice != 549000 {
		t.Errorf("prices[0].SalePrice = %v, want 549000", got[0].SalePrice)
	}
	if got[0].EffectiveVat == nil || *got[0].EffectiveVat != 22 {
		t.Errorf("prices[0].EffectiveVat = %v, want 22", got[0].EffectiveVat)
	}
	if !got[1].UseParentVat || got[1].EffectiveVat != nil || got[1].BuyPrice != nil {
		t.Errorf("prices[1] = %+v, want useParentVat=true, НДС/цены nil", got[1])
	}
}

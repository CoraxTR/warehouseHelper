package usecase

import (
	"encoding/json"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// Фикстуры — реальные ответы живого API (проба 08.09.2026) и примеры из ТЗ:
// отмена 19379 (state → «Отменен»), удаление позиции из 01726 (Чак ролл,
// quantity 0.657, reserve 0.0 — НЕ отложен).

const cancelledStateID = "8737d8a5-c0b9-11e3-ac8e-002590a28eca"

func rowsFixture(t *testing.T, fixture string) []client.AuditEventRow {
	t.Helper()

	var resp struct {
		Rows []client.AuditEventRow `json:"rows"`
	}
	if err := json.Unmarshal([]byte(fixture), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return resp.Rows
}

func TestParseDetailCancelled(t *testing.T) {
	// Перевод заказа 19379: state РефГо → Отменен (id 8737d8a5...), без удалений.
	const fixture = `{"rows": [{
		"source": "app", "eventType": "update", "entityType": "customerorder",
		"uid": "sklad@steakhome", "moment": "2026-09-08 23:11:52.918",
		"diff": {"state": {
			"oldValue": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/785d7841-1ac2-11f0-0a80-071f000f6177"}, "name": "РефГо"},
			"newValue": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/8737d8a5-c0b9-11e3-ac8e-002590a28eca"}, "name": "Отменен"}}},
		"name": "19379",
		"entity": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/0de266d9-2c42-11f0-0a80-0cd40022203a"}}
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if !out.cancelled {
		t.Error("cancelled = false, want true (state → Отменён)")
	}
	if len(out.removals) != 0 {
		t.Errorf("len(removals) = %d, want 0", len(out.removals))
	}
}

func TestParseDetailCancelledWrongStateID(t *testing.T) {
	// Тот же state, но MSAPI_CANCELLED_STATE_ID задан другим статусом — не отмена.
	const fixture = `{"rows": [{
		"diff": {"state": {
			"oldValue": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/785d7841-1ac2-11f0-0a80-071f000f6177"}},
			"newValue": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/8737d8a5-c0b9-11e3-ac8e-002590a28eca"}}}}
	}]}`

	out := parseDetail(rowsFixture(t, fixture), "00000000-0000-0000-0000-000000000000")
	if out.cancelled {
		t.Error("cancelled = true, want false (другой id статуса)")
	}

	// Пустой CANCELLED_STATE_ID — отмена не детектится вовсе.
	if out := parseDetail(rowsFixture(t, fixture), ""); out.cancelled {
		t.Error("cancelled = true with empty cancelledStateID, want false")
	}
}

func TestParseDetailRemoval(t *testing.T) {
	// Удаление позиции из заказа 01726 (пример ТЗ): Чак ролл, oldValue без
	// newValue, quantity 0.657, reserve 0.0, uom кг.
	const fixture = `{"rows": [{
		"source": "app", "eventType": "update", "entityType": "customerorder",
		"moment": "2026-09-08 23:11:44.554",
		"diff": {"positions": [{"oldValue": {
			"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/a02a9121-7ef5-11e5-7a40-e897001b4cc6"}, "name": "Стейк Чак ролл Праймбиф. Охл."},
			"quantity": 0.657, "uom": "кг", "reserve": 0.0, "price": 1990.0, "vat": 10.0, "discount": 0.0}}]},
		"name": "01726",
		"entity": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/d09aaa19-1c68-11f1-0a80-1a86003de80f"}}
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if out.cancelled {
		t.Error("cancelled = true, want false")
	}
	if len(out.removals) != 1 {
		t.Fatalf("len(removals) = %d, want 1", len(out.removals))
	}

	r := out.removals[0]
	if r.ProductID != "a02a9121-7ef5-11e5-7a40-e897001b4cc6" {
		t.Errorf("ProductID = %s, want uuid товара из meta.href", r.ProductID)
	}
	if r.Name != "Стейк Чак ролл Праймбиф. Охл." {
		t.Errorf("Name = %q, want название из диффа", r.Name)
	}
	if r.Quantity != 0.657 || r.Reserve != 0.0 {
		t.Errorf("Quantity/Reserve = %v/%v, want 0.657/0.0", r.Quantity, r.Reserve)
	}
	if r.Uom != "кг" {
		t.Errorf("Uom = %q, want кг", r.Uom)
	}
}

func TestParseDetailIgnoresChangesAndAdds(t *testing.T) {
	// Подбор (изменение reserve 0 → 0.657: оба значения) и добавление строки
	// (только newValue) — удалениями не считаются.
	const fixture = `{"rows": [{
		"diff": {"positions": [
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-1"}}, "quantity": 1.0, "reserve": 0.0},
			 "newValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-1"}}, "quantity": 1.0, "reserve": 1.0}},
			{"newValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-2"}}, "quantity": 2.0}}
		]}
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if len(out.removals) != 0 {
		t.Errorf("len(removals) = %d, want 0 (изменения и добавления не удаления)", len(out.removals))
	}
}

func TestParseDetailRemovalWithNullUom(t *testing.T) {
	// У uom в диффе бывает null — removal собирается без паники, Uom пустой.
	const fixture = `{"rows": [{
		"diff": {"positions": [{"oldValue": {
			"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-3"}, "name": "Доставка"},
			"quantity": 1.0, "reserve": 0.0, "uom": null}}]}
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if len(out.removals) != 1 {
		t.Fatalf("len(removals) = %d, want 1", len(out.removals))
	}
	if out.removals[0].Uom != "" {
		t.Errorf("Uom = %q, want empty (null в диффе)", out.removals[0].Uom)
	}
}

func TestParseDetailRemovalAndCancelledTogether(t *testing.T) {
	// Отмена заказа с одновременным удалением строки (менеджер чистит заказ
	// и переводит в «Отменён») — оба признака; приоритет решает вызывающий.
	const fixture = `{"rows": [{
		"diff": {"state": {
			"newValue": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/8737d8a5-c0b9-11e3-ac8e-002590a28eca"}}}},
		"name": "19380"
	}, {
		"diff": {"positions": [{"oldValue": {
			"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-9"}, "name": "Рибай"},
			"quantity": 1.2, "reserve": 1.2, "uom": "кг"}}]},
		"name": "19380"
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if !out.cancelled {
		t.Error("cancelled = false, want true")
	}
	if len(out.removals) != 1 {
		t.Errorf("len(removals) = %d, want 1", len(out.removals))
	}
}

func TestLastPathSegment(t *testing.T) {
	cases := map[string]string{
		"https://api.moysklad.ru/api/remap/1.2/entity/product/a02a9121-7ef5-11e5-7a40-e897001b4cc6":                       "a02a9121-7ef5-11e5-7a40-e897001b4cc6",
		"https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/8737d8a5-c0b9-11e3-ac8e-002590a28eca": "8737d8a5-c0b9-11e3-ac8e-002590a28eca",
		"id-без-слэша": "id-без-слэша",
		"":             "",
	}
	for href, want := range cases {
		if got := lastPathSegment(href); got != want {
			t.Errorf("lastPathSegment(%q) = %q, want %q", href, got, want)
		}
	}
}

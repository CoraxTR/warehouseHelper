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

func TestParseDetailPartialRemoval(t *testing.T) {
	// Живой аудит 06.10.2026, заказ 07231: в одном событии две УДАЛЁННЫЕ
	// отложенные позиции и УРЕЗАННАЯ штучная — Карпаччо креветки 2 шт r=2 →
	// 1 шт r=1 (менеджер уменьшил количество в вебе, МС сам понизил резерв).
	// Фильтр «quantity == reserve» такое событие не видит (резерв снова
	// сходится) — единственный источник это дифф аудита.
	const fixture = `{"rows": [{
		"source": "app", "eventType": "update", "entityType": "customerorder",
		"uid": "solomyannov@steakhome", "moment": "2026-10-06 14:29:45.129",
		"diff": {"sum": {"oldValue": 64635.03, "newValue": 56375.68}, "positions": [
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/285f6985-ae84-11f1-0a80-16670027d938"}, "name": "Корейка ягненка Мясомелье 8 ребер, зам."}, "quantity": 0.515, "uom": "кг", "reserve": 0.515}},
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/6025db1e-d893-11ea-0a80-09d90006c6b2"}, "name": "Соль копчёная (дойпак) SPASSKIY. 115 гр."}, "quantity": 1.0, "uom": "шт", "reserve": 1.0}},
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/23dfc731-51c8-11f0-0a80-0d0400110454"}, "name": "Карпаччо из красной средиземноморской креветки 500г"}, "quantity": 2.0, "uom": "шт", "reserve": 2.0},
			 "newValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/23dfc731-51c8-11f0-0a80-0d0400110454"}, "name": "Карпаччо из красной средиземноморской креветки 500г"}, "quantity": 1.0, "uom": "шт", "reserve": 1.0}}
		]},
		"name": "07231",
		"entity": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/513c9424-c175-11f1-0a80-1f230013d26b"}}
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if out.cancelled {
		t.Error("cancelled = true, want false")
	}
	if len(out.removals) != 3 {
		t.Fatalf("len(removals) = %d, want 3 (2 удаления + урезание): %+v", len(out.removals), out.removals)
	}

	// Полное удаление: к возврату вся отложенная строка, Released пуст.
	if got := out.removals[0]; got.Quantity != 0.515 || got.Reserve != 0.515 || got.Released != 0 {
		t.Errorf("удаление = %+v, want 0.515/0.515 и Released 0", got)
	}

	// Урезание: Released — снятая часть резерва (1 шт), Quantity/Reserve —
	// снимок строки ДО уменьшения.
	r := out.removals[2]
	if r.ProductID != "23dfc731-51c8-11f0-0a80-0d0400110454" {
		t.Errorf("ProductID = %s, want uuid товара из meta.href", r.ProductID)
	}
	if r.Released != 1 {
		t.Errorf("Released = %v, want 1 (резерв 2 → 1)", r.Released)
	}
	if r.Quantity != 2 || r.Reserve != 2 {
		t.Errorf("Quantity/Reserve = %v/%v, want снимок до уменьшения 2/2", r.Quantity, r.Reserve)
	}
	if r.Uom != "шт" {
		t.Errorf("Uom = %q, want шт", r.Uom)
	}
}

func TestParseDetailPartialRemovalNotOurs(t *testing.T) {
	// Не частичное расформирование: рост количества, замена товара в строке и
	// уменьшение без снятия резерва (строка не была отложена) — в возврат не идут.
	const fixture = `{"rows": [{
		"diff": {"positions": [
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-1"}}, "quantity": 1.0, "reserve": 1.0, "uom": "шт"},
			 "newValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-1"}}, "quantity": 2.0, "reserve": 1.0, "uom": "шт"}},
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-1"}}, "quantity": 1.0, "reserve": 1.0, "uom": "шт"},
			 "newValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-2"}}, "quantity": 1.0, "reserve": 1.0, "uom": "шт"}},
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-3"}}, "quantity": 2.0, "reserve": 0.0, "uom": "шт"},
			 "newValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/p-3"}}, "quantity": 1.0, "reserve": 0.0, "uom": "шт"}}
		]}
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if len(out.removals) != 0 {
		t.Errorf("len(removals) = %d, want 0: рост/замена товара/без резерва — не расформирование (%+v)",
			len(out.removals), out.removals)
	}
}

func TestParseDetailWeightedPartialRemoval(t *testing.T) {
	// Урезание ВЕСОВОЙ строки (2.482 → 1.962 кг, резерв 2.482 → 1.962): тоже
	// removal с Released > 0 (снятая часть резерва). В возврат в продажу
	// scanmatch такую строку не пустит, но модуль «вес уменьшен вручную» читает
	// тот же дифф — парсер обязан её отдать.
	const fixture = `{"rows": [{
		"source": "app", "eventType": "update", "entityType": "customerorder",
		"moment": "2026-10-07 10:00:00.000",
		"diff": {"positions": [
			{"oldValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/a02a9121-7ef5-11e5-7a40-e897001b4cc6"}, "name": "Стриплойн 1\\2 Праймбиф"}, "quantity": 2.482, "reserve": 2.482, "uom": "кг"},
			 "newValue": {"assortment": {"meta": {"href": "https://api.moysklad.ru/api/remap/1.2/entity/product/a02a9121-7ef5-11e5-7a40-e897001b4cc6"}, "name": "Стриплойн 1\\2 Праймбиф"}, "quantity": 1.962, "reserve": 1.962, "uom": "кг"}}
		]},
		"name": "07246"
	}]}`

	out := parseDetail(rowsFixture(t, fixture), cancelledStateID)
	if len(out.removals) != 1 {
		t.Fatalf("len(removals) = %d, want 1 (урезание весовой строки)", len(out.removals))
	}
	r := out.removals[0]
	if r.Quantity != 2.482 || r.Reserve != 2.482 {
		t.Errorf("Quantity/Reserve = %v/%v, want снимок до уменьшения 2.482/2.482", r.Quantity, r.Reserve)
	}
	if r.Released < 0.519 || r.Released > 0.521 {
		t.Errorf("Released = %v, want ≈0.52 (резерв 2.482 → 1.962)", r.Released)
	}
	if r.Uom != "кг" {
		t.Errorf("Uom = %q, want кг", r.Uom)
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

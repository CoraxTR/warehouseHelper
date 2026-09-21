package client

import (
	"encoding/json"
	"testing"
)

// msAttr собирает атрибут так, как его отдаёт МС: тип из метаданных + сырое
// значение.
func msAttr(name, typ, value string) MSAttributes {
	return MSAttributes{Name: name, Type: typ, Value: json.RawMessage(value)}
}

// Разбор атрибутов заказа: знакомые типы ложатся в карту значениями, а
// НЕИЗВЕСТНЫЕ (long/text/time — живут в базе МС) пропускаются и разбор строки
// не роняют. Иначе одна заполненная «Бонусы» ослепляла бы reservewatch и импорт
// заказов целиком — прод-ошибка «error unmarshalling attribute» 21.09.2026.
func TestUnmarshalMSOrderAttributesSkipsUnknownTypes(t *testing.T) {
	order := MSOrder{
		Name: "Заказ №1",
		Attributes: []MSAttributes{
			msAttr("Имя получателя", "string", `"Иван"`),
			msAttr("Регион доставки", MSCustomEntityType,
				`{"meta":{"href":"https://api.moysklad.ru/x","type":"customentity"},"name":"МСК"}`),
			msAttr("Курьер", MSEmployeeType,
				`{"meta":{"href":"https://api.moysklad.ru/y","type":"employee"},"name":"Пётр"}`),
			msAttr("Кол-во гостей", "long", "4"),
			msAttr("Дата МК", "time", `"2026-09-21 16:59:00"`),
			msAttr("Скидки по бонусам", "text", `"10%"`),
		},
	}

	if err := unmarshalMSOrderAttributes(&order); err != nil {
		t.Fatalf("неизвестные типы не должны ронять разбор: %v", err)
	}

	want := map[string]string{
		"Имя получателя":  "Иван",
		"Регион доставки": "МСК",
		"Курьер":          "Пётр",
	}
	for name, wantVal := range want {
		if got := order.AttributesMap[name]; got != wantVal {
			t.Errorf("%s = %v, want %q", name, got, wantVal)
		}
	}

	for _, name := range []string{"Кол-во гостей", "Дата МК", "Скидки по бонусам"} {
		if _, ok := order.AttributesMap[name]; ok {
			t.Errorf("атрибут %q неизвестного типа попал в карту — его надо пропускать", name)
		}
	}
}

// Битое значение ЗНАКОМОГО типа остаётся ошибкой: сломан формат того, что
// читают модули (регион, интервал, коробки), — тихо терять поле нельзя.
func TestUnmarshalMSOrderAttributesBrokenKnownValueErrors(t *testing.T) {
	order := MSOrder{
		Name:       "Заказ №2",
		Attributes: []MSAttributes{msAttr("Имя получателя", "string", `{"name":"объект вместо строки"}`)},
	}

	if err := unmarshalMSOrderAttributes(&order); err == nil {
		t.Error("битое значение string-атрибута — ожидалась ошибка")
	}
}

// Страница заказов целиком: строка с атрибутом неизвестного типа разбор
// страницы не роняет (именно это и падало в проде).
func TestUnmarshalMSFetchOrdersResponseKeepsRowsWithUnknownAttribute(t *testing.T) {
	body := []byte(`{"meta":{"size":2},"rows":[` +
		`{"id":"a1","name":"Заказ №1","attributes":[{"name":"Бонусы","type":"long","value":1500}]},` +
		`{"id":"a2","name":"Заказ №2","attributes":[{"name":"Имя получателя","type":"string","value":"Иван"}]}` +
		`]}`)

	resp, err := unmarshalMSFetchOrdersResponse(body)
	if err != nil {
		t.Fatalf("страница с неизвестным типом атрибута не должна падать: %v", err)
	}

	if len(resp.Rows) != 2 {
		t.Fatalf("строк = %d, want 2", len(resp.Rows))
	}

	if got := resp.Rows[1].AttributesMap["Имя получателя"]; got != "Иван" {
		t.Errorf("Имя получателя второй строки = %v, want Иван", got)
	}
}

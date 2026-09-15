package postgres

import (
	"reflect"
	"testing"

	"warehouseHelper/internal/stock"
)

// TestScanCatalogProductNullableText — карточка каталога для «Обновить сроки»:
// NULL в internal_code и group_name даёт пустые строки (у товара без кода МС и
// без группы в БД именно NULL), а Lots всегда пустой массив, не nil: клиент
// итерирует p.lots.length.
func TestScanCatalogProductNullableText(t *testing.T) {
	got, err := scanCatalogProduct(fakeRow{vals: []any{"p-1", nil, "Без группы", nil, false}})
	if err != nil {
		t.Fatalf("scanCatalogProduct: %v", err)
	}
	want := stock.Product{ID: "p-1", Name: "Без группы", Lots: []stock.Lot{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanCatalogProduct = %+v, want %+v", got, want)
	}

	got, err = scanCatalogProduct(fakeRow{vals: []any{"p-2", "00001234", "Молоко", "Молочка", true}})
	if err != nil {
		t.Fatalf("scanCatalogProduct: %v", err)
	}
	want = stock.Product{
		ID: "p-2", InternalCode: "00001234", Name: "Молоко",
		GroupName: "Молочка", ShortList: true, Lots: []stock.Lot{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanCatalogProduct = %+v, want %+v", got, want)
	}
}

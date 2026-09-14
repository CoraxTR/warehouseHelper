package postgres

import (
	"reflect"
	"testing"

	"warehouseHelper/internal/receiving"
)

// TestScanBarcodeRefNullableCode — связка «внешний код → товар»: NULL в
// p.internal_code (товар без кода МС) даёт пустой InternalCode вместо падения
// Scan, а вид товара (Weighted) считается по uom.
func TestScanBarcodeRefNullableCode(t *testing.T) {
	got, err := scanBarcodeRef(fakeRow{vals: []any{"460123", "p-1", "Мясо", nil, "кг"}})
	if err != nil {
		t.Fatalf("scanBarcodeRef: %v", err)
	}
	want := receiving.BarcodeRef{
		ExternalCode: "460123", ProductID: "p-1", ProductName: "Мясо", Weighted: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanBarcodeRef = %+v, want %+v (пустой код, весовой товар)", got, want)
	}

	got, err = scanBarcodeRef(fakeRow{vals: []any{"460999", "p-2", "Стакан", "00009999", "шт"}})
	if err != nil {
		t.Fatalf("scanBarcodeRef: %v", err)
	}
	if got.InternalCode != "00009999" || got.Weighted {
		t.Errorf("scanBarcodeRef = %+v, want код 00009999 и штучный товар", got)
	}
}

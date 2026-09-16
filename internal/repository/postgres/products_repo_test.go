package postgres

import (
	"reflect"
	"testing"

	"warehouseHelper/internal/domain"
)

// TestScanProductNullableText — NULL в nullable TEXT-колонках каталога
// (internal_code, group_name, folder_id) даёт пустые строки, а не падение Scan
// («cannot scan NULL into *string»): у товара без кода МС и без папки в БД
// именно NULL, а модель хранит пустую строку.
func TestScanProductNullableText(t *testing.T) {
	tests := []struct {
		name string
		vals []any
		want domain.Product
	}{
		{
			name: "три TEXT-колонки NULL",
			vals: []any{"p-1", nil, "Молоко", "шт", nil, nil, nil,
				int16(14), int16(6), "Копейка", true, false},
			want: domain.Product{
				ID: "p-1", Name: "Молоко", UOM: "шт",
				ShelfLife: new(int16(14)), PackSize: new(int16(6)),
				InventoryType: "Копейка", ShortList: true,
			},
		},
		{
			name: "значения есть — переносятся как есть",
			vals: []any{"p-2", "00001234", "Сыр", "кг", "Молочка/Сыры", "folder-7", 0.35,
				nil, nil, "Копейка", false, true},
			want: domain.Product{
				ID: "p-2", InternalCode: "00001234", Name: "Сыр", UOM: "кг",
				GroupName: "Молочка/Сыры", FolderID: "folder-7",
				AverageWeight: new(0.35), InventoryType: "Копейка", TrackWeekly: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanProduct(fakeRow{vals: tc.vals})
			if err != nil {
				t.Fatalf("scanProduct: %v", err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("scanProduct = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

package receiving

import (
	"reflect"
	"testing"
)

func TestPairCodeRows(t *testing.T) {
	tests := []struct {
		name       string
		codes      []string
		productIDs []string
		want       []CodeRow
	}{
		{
			name:       "пустой вход",
			codes:      nil,
			productIDs: nil,
			want:       nil,
		},
		{
			name:       "оба массива пустые (не nil)",
			codes:      []string{},
			productIDs: []string{},
			want:       nil,
		},
		{
			name:       "одна валидная пара (старый одиночный POST)",
			codes:      []string{"123456"},
			productIDs: []string{"p-1"},
			want: []CodeRow{
				{Row: 1, ExternalCode: "123456", ProductID: "p-1"},
			},
		},
		{
			name:       "код без товара",
			codes:      []string{"123456"},
			productIDs: []string{""},
			want: []CodeRow{
				{Row: 1, ExternalCode: "123456", Errors: []string{"внешний код без товара"}},
			},
		},
		{
			name:       "товар без кода",
			codes:      []string{""},
			productIDs: []string{"p-1"},
			want: []CodeRow{
				{Row: 1, ProductID: "p-1", Errors: []string{"товар без внешнего кода"}},
			},
		},
		{
			name:       "полностью пустая пара пропускается",
			codes:      []string{"", "222"},
			productIDs: []string{"", "p-2"},
			want: []CodeRow{
				{Row: 2, ExternalCode: "222", ProductID: "p-2"},
			},
		},
		{
			name:       "пустая пара в середине не сдвигает номера строк",
			codes:      []string{"111", "", "333"},
			productIDs: []string{"p-1", "", "p-3"},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 3, ExternalCode: "333", ProductID: "p-3"},
			},
		},
		{
			name:       "длины выравниваются: кодов больше",
			codes:      []string{"111", "222"},
			productIDs: []string{"p-1"},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ExternalCode: "222", Errors: []string{"внешний код без товара"}},
			},
		},
		{
			name:       "длины выравниваются: товаров больше",
			codes:      []string{"111"},
			productIDs: []string{"p-1", "p-2"},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ProductID: "p-2", Errors: []string{"товар без внешнего кода"}},
			},
		},
		{
			name:       "пробелы обрезаются, строка из пробелов = пустая",
			codes:      []string{" 111 ", "   ", "333"},
			productIDs: []string{" p-1 ", "p-2", "  "},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ProductID: "p-2", Errors: []string{"товар без внешнего кода"}},
				{Row: 3, ExternalCode: "333", Errors: []string{"внешний код без товара"}},
			},
		},
		{
			name:       "дубликат кода: второе вхождение — ошибка",
			codes:      []string{"111", "111"},
			productIDs: []string{"p-1", "p-2"},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ExternalCode: "111", ProductID: "p-2", Errors: []string{"дубликат внешнего кода в батче"}},
			},
		},
		{
			name:       "дубликат кода: все вхождения после первого — ошибки",
			codes:      []string{"111", "222", "111", "111"},
			productIDs: []string{"p-1", "p-2", "p-3", "p-4"},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ExternalCode: "222", ProductID: "p-2"},
				{Row: 3, ExternalCode: "111", ProductID: "p-3", Errors: []string{"дубликат внешнего кода в батче"}},
				{Row: 4, ExternalCode: "111", ProductID: "p-4", Errors: []string{"дубликат внешнего кода в батче"}},
			},
		},
		{
			name:       "дубликат с пробелами по краям тоже ловится",
			codes:      []string{"111", " 111"},
			productIDs: []string{"p-1", "p-2"},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ExternalCode: "111", ProductID: "p-2", Errors: []string{"дубликат внешнего кода в батче"}},
			},
		},
		{
			name:       "дубликат кода без товара копит две ошибки",
			codes:      []string{"111", "111"},
			productIDs: []string{"p-1", ""},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ExternalCode: "111", Errors: []string{"внешний код без товара", "дубликат внешнего кода в батче"}},
			},
		},
		{
			name:       "смешанный батч: порядок и номера строк сохраняются",
			codes:      []string{"111", "", "333", "111", ""},
			productIDs: []string{"p-1", "p-2", "", "p-4", ""},
			want: []CodeRow{
				{Row: 1, ExternalCode: "111", ProductID: "p-1"},
				{Row: 2, ProductID: "p-2", Errors: []string{"товар без внешнего кода"}},
				{Row: 3, ExternalCode: "333", Errors: []string{"внешний код без товара"}},
				{Row: 4, ExternalCode: "111", ProductID: "p-4", Errors: []string{"дубликат внешнего кода в батче"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PairCodeRows(tt.codes, tt.productIDs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("PairCodeRows(%q, %q)\nполучили: %#v\nожидали:  %#v", tt.codes, tt.productIDs, got, tt.want)
			}
		})
	}
}

func TestCodeRowValid(t *testing.T) {
	if !(CodeRow{ExternalCode: "1", ProductID: "p"}).Valid() {
		t.Error("строка без ошибок должна быть валидной")
	}

	if (CodeRow{ExternalCode: "1", Errors: []string{"x"}}).Valid() {
		t.Error("строка с ошибкой не должна быть валидной")
	}
}

package innercode

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// Примеры пользователя: кусок 250 г (10 шт × 250 г = 2500 г в коробке),
// выработка 29.08.2026, срок 29.09.2026.
const (
	itemBarcode = "00210003002502908202629092026"
	boxBarcode  = "002100030025000102908202629092026"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Code
	}{
		{
			name: "item from user example",
			raw:  itemBarcode,
			want: Code{
				Kind:         KindItem,
				InternalCode: "00210003",
				WeightG:      250,
				Qty:          1,
				ProdDate:     time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
				ExpDate:      time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "box from user example",
			raw:  boxBarcode,
			want: Code{
				Kind:         KindBox,
				InternalCode: "00210003",
				WeightG:      2500,
				Qty:          10,
				ProdDate:     time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
				ExpDate:      time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "item minimal weight 1 g",
			raw:  "10210003000012912202501012026",
			want: Code{
				Kind:         KindItem,
				InternalCode: "10210003",
				WeightG:      1,
				Qty:          1,
				ProdDate:     time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC),
				ExpDate:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "box with single item",
			raw:  "212100031005000010101202602012026",
			want: Code{
				Kind:         KindBox,
				InternalCode: "21210003",
				WeightG:      100500,
				Qty:          1,
				ProdDate:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				ExpDate:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "item with equal prod and exp dates",
			raw:  "00210003010001501202615012026",
			want: Code{
				Kind:         KindItem,
				InternalCode: "00210003",
				WeightG:      1000,
				Qty:          1,
				ProdDate:     time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
				ExpDate:      time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if err != nil {
				t.Fatalf("Parse(%s) error: %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%s) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParse_NotInternal(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "short", raw: "123456789"},
		{name: "item truncated to 28", raw: itemBarcode[:28]},
		{name: "30 digits", raw: itemBarcode + "0"},
		{name: "31 digits", raw: itemBarcode + "00"},
		{name: "32 digits", raw: itemBarcode + "000"},
		{name: "34 digits", raw: boxBarcode + "0"},
		{name: "35 digits", raw: boxBarcode + "00"},
		{name: "non-digits short", raw: "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.raw)
			if !errors.Is(err, ErrNotInternal) {
				t.Errorf("Parse(%q) error = %v, want ErrNotInternal", tt.raw, err)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "internal code mode 3", raw: "30210003002502908202629092026"},
		{name: "internal code with letter", raw: "002A0003002502908202629092026"},
		{name: "item zero weight", raw: "00210003000002908202629092026"},
		{name: "item weight with letter", raw: "002100030O2502908202629092026"},
		{name: "box zero qty", raw: "002100030025000002908202629092026"},
		{name: "box weight with letter", raw: "002100030O25000102908202629092026"},
		{name: "impossible date feb 31", raw: "00210003002503102202629092026"},
		{name: "leap day in non-leap year", raw: "00210003002502902202529092026"},
		{name: "exp date before prod date", raw: "00210003002502909202629082026"},
		{name: "date with letter", raw: "002100030025029O8202629092026"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.raw)
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("Parse(%q) error = %v, want ErrInvalid", tt.raw, err)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{kind: KindItem, want: "item"},
		{kind: KindBox, want: "box"},
		{kind: Kind(0), want: "unknown"},
		{kind: Kind(255), want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.kind.String(); got != tt.want {
				t.Errorf("Kind(%d).String() = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

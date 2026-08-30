package decoderules

import (
	"strings"
	"testing"
)

// Поля полного правила куска из примера "28-1-6-7-6-13-8-21-8".
func TestParseItemFull(t *testing.T) {
	r, err := ParseItem("28-1-6-7-6-13-8-21-8")
	if err != nil {
		t.Fatalf("ParseItem: %v", err)
	}
	if r.Length != 28 {
		t.Errorf("Length = %d, want 28", r.Length)
	}
	want := []Field{{1, 6}, {7, 6}, {13, 8}, {21, 8}}
	if len(r.Fields) != len(want) {
		t.Fatalf("полей %d, want %d", len(r.Fields), len(want))
	}
	for i, w := range want {
		if r.Fields[i] != w {
			t.Errorf("поле %d = %+v, want %+v", i, r.Fields[i], w)
		}
	}
	for i := range want {
		if !r.Has(i) {
			t.Errorf("поле %d должно быть задано", i)
		}
	}
}

// Отсутствующее поле — пробел на месте позиции, длина пары остаётся.
func TestParseItemMissingField(t *testing.T) {
	r, err := ParseItem("28- -6-7-6-13-8-21-8")
	if err != nil {
		t.Fatalf("ParseItem: %v", err)
	}
	if r.Has(FieldCode) {
		t.Error("поле кода товара должно отсутствовать")
	}
	if !r.Has(FieldWeight) || !r.Has(FieldProducedOn) || !r.Has(FieldBestBefore) {
		t.Error("остальные поля должны быть заданы")
	}
	// Пробел с обеих сторон разделителя тоже валиден.
	if _, err := ParseItem("28- -6-7-6-13-8-21-8"); err != nil {
		t.Fatalf("пробел вокруг позиции: %v", err)
	}
}

func TestParseItemZeroIsNotMissing(t *testing.T) {
	// Позиция 0 — не маркер отсутствия, а ошибка: позиции 1-based.
	if _, err := ParseItem("28-0-6-7-6-13-8-21-8"); err == nil {
		t.Error("позиция 0 должна быть ошибкой")
	}
}

func TestParseBox(t *testing.T) {
	r, err := ParseBox("33-1-6-7-6-13-3-16-8-24-8")
	if err != nil {
		t.Fatalf("ParseBox: %v", err)
	}
	if r.Length != 33 || len(r.Fields) != BoxFieldCount {
		t.Fatalf("Length=%d Fields=%d, want 33/%d", r.Length, len(r.Fields), BoxFieldCount)
	}
	if !r.Has(BoxCode) || !r.Has(BoxWeight) || !r.Has(BoxQty) || !r.Has(BoxProducedOn) || !r.Has(BoxBestBefore) {
		t.Error("все поля коробки должны быть заданы")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"",                         // пустое
		"28",                       // без пар
		"28-1",                     // нечётное число токенов
		"28-1-6-7-6-13-8",          // 3 пары вместо 4
		"28-1-6-7-6-13-8-21-8-1-1", // 5 пар вместо 4
		"abc-1-6-7-6-13-8-21-8",    // длина не число
		"-1-6-7-6-13-8-21-8",       // длина пустая
		"0-1-6-7-6-13-8-21-8",      // длина 0
		"28-x-6-7-6-13-8-21-8",     // позиция не число
		"28-1-x-7-6-13-8-21-8",     // длина не число
		"28-1-0-7-6-13-8-21-8",     // длина 0
		"28-30-6-7-6-13-8-21-8",    // поле за пределами кода
	}
	for _, c := range cases {
		if _, err := ParseItem(c); err == nil {
			t.Errorf("ParseItem(%q): ожидалась ошибка", c)
		}
	}
}

// Slice: нарезка полей из кода длиной 28 (формат поставщика: 6+6+8+8).
// Строки собираются конкатенацией констант — потерянная цифра меняет длину
// и валит разбор (питфолл из innercode).
const (
	testCode       = "123456"   // код товара
	testWeight     = "000250"   // вес, граммы
	testProduced   = "29082026" // выработка ДДММГГГГ
	testBestBefore = "29092026" // срок ДДММГГГГ
)

func TestSlice(t *testing.T) {
	r, err := ParseItem("28-1-6-7-6-13-8-21-8")
	if err != nil {
		t.Fatalf("ParseItem: %v", err)
	}
	raw := testCode + testWeight + testProduced + testBestBefore
	if len(raw) != r.Length {
		t.Fatalf("длина кода %d != длина правила %d", len(raw), r.Length)
	}
	if got, ok := r.Slice(raw, FieldCode); !ok || got != testCode {
		t.Errorf("код = %q (ok=%v), want %q", got, ok, testCode)
	}
	if got, ok := r.Slice(raw, FieldWeight); !ok || got != testWeight {
		t.Errorf("вес = %q (ok=%v), want %q", got, ok, testWeight)
	}
	if got, ok := r.Slice(raw, FieldProducedOn); !ok || got != testProduced {
		t.Errorf("выработка = %q (ok=%v), want %q", got, ok, testProduced)
	}
	if got, ok := r.Slice(raw, FieldBestBefore); !ok || got != testBestBefore {
		t.Errorf("срок = %q (ok=%v), want %q", got, ok, testBestBefore)
	}
}

func TestSliceMissingField(t *testing.T) {
	r, err := ParseItem("28- -6-7-6-13-8-21-8")
	if err != nil {
		t.Fatalf("ParseItem: %v", err)
	}
	if _, ok := r.Slice(testCode+testWeight+testProduced+testBestBefore, FieldCode); ok {
		t.Error("Slice отсутствующего поля должен вернуть ok=false")
	}
}

// Slice короткого штрих-кода: поле не выходит за границы строки.
func TestSliceShortRaw(t *testing.T) {
	r, err := ParseItem("28-1-6-7-6-13-8-21-8")
	if err != nil {
		t.Fatalf("ParseItem: %v", err)
	}
	if _, ok := r.Slice("12345", FieldCode); ok {
		t.Error("Slice по короткой строке должен вернуть ok=false")
	}
}

// Пифолл: тестовые строки правил не собирать руками с потерянными цифрами —
// длина штрих-кода и пары должны соответствовать друг другу.
func TestRuleAgainstRealBarcode(t *testing.T) {
	rule := "28-1-6-7-6-13-8-21-8"
	r, err := ParseItem(rule)
	if err != nil {
		t.Fatalf("ParseItem: %v", err)
	}
	raw := testCode + testWeight + testProduced + testBestBefore
	if len(raw) != r.Length {
		t.Fatalf("длина штрих-кода %d != длина правила %d", len(raw), r.Length)
	}
	// Поля, собранные конкатенацией, должны дать исходный код целиком.
	parts := []string{}
	for i := range r.Fields {
		v, ok := r.Slice(raw, i)
		if !ok {
			t.Fatalf("поле %d не вычиталось", i)
		}
		parts = append(parts, v)
	}
	if got := strings.Join(parts, ""); got != raw {
		t.Errorf("конкатенация полей %q != исходный код %q", got, raw)
	}
}

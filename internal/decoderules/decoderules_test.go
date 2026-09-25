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
	parts := make([]string, 0, len(r.Fields))
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

// --- формат дат в правиле: необязательный хвостовой токен (25.09.2026) ---

// Дата в ШК поставщика не всегда ДДММГГГГ: последний токен правила задаёт
// формат — «ггммдд» (GS1: AI 11 выработка, AI 17 срок) или «ддммгг».
func TestParseItemDateFormat(t *testing.T) {
	cases := []struct {
		rule string
		want DateFormat
	}{
		{"28-1-6-7-6-13-8-21-8", ""},                    // токена нет — исторический ДДММГГГГ
		{"28-1-6-7-6-13-8-21-8-ддммгггг", DateDDMMYYYY}, // токен задан явно
		{"28-1-6-7-6-13-6-21-6-ггммдд", DateYYMMDD},     // 6 цифр, год-месяц-день
		{"28-1-6-7-6-13-6-21-6-ддммгг", DateDDMMYY},     // 6 цифр, день-месяц-год
		{"28- -6-7-6-13-6-21-6-ггммдд", DateYYMMDD},     // без кода товара
	}
	for _, c := range cases {
		r, err := ParseItem(c.rule)
		if err != nil {
			t.Errorf("ParseItem(%q): %v", c.rule, err)
			continue
		}
		if r.DateFormat != c.want {
			t.Errorf("ParseItem(%q).DateFormat = %q, want %q", c.rule, r.DateFormat, c.want)
		}
	}
}

func TestParseBoxDateFormat(t *testing.T) {
	// Пять пар: код, вес, вложения, выработка (6), срок (6).
	r, err := ParseBox("33-1-6-7-6-13-3-16-6-22-6-ггммдд")
	if err != nil {
		t.Fatalf("ParseBox: %v", err)
	}
	if r.DateFormat != DateYYMMDD {
		t.Errorf("DateFormat = %q, want %q", r.DateFormat, DateYYMMDD)
	}
}

// Name/Layout/Digits — единый источник правды для приёмки (Go) и страницы (JS
// читает тот же токен из кеша).
func TestDateFormatLayout(t *testing.T) {
	cases := []struct {
		format DateFormat
		name   string
		layout string
		digits int
	}{
		{"", string(DateDDMMYYYY), "02012006", 8},
		{DateDDMMYYYY, "ддммгггг", "02012006", 8},
		{DateDDMMYY, "ддммгг", "020106", 6},
		{DateYYMMDD, "ггммдд", "060102", 6},
	}
	for _, c := range cases {
		if got := c.format.Name(); got != c.name {
			t.Errorf("Name(%q) = %q, want %q", c.format, got, c.name)
		}
		if got := c.format.Layout(); got != c.layout {
			t.Errorf("Layout(%q) = %q, want %q", c.format, got, c.layout)
		}
		if got := c.format.Digits(); got != c.digits {
			t.Errorf("Digits(%q) = %d, want %d", c.format, got, c.digits)
		}
	}
}

// Неизвестный/лишний токен формата — ошибка сохранения карточки.
func TestParseDateFormatErrors(t *testing.T) {
	cases := []struct {
		rule string
		do   func(string) (Rule, error)
	}{
		{"28-1-6-7-6-13-8-21-8-ггм", ParseItem},              // незнакомый формат
		{"28-1-6-7-6-13-8-21-8-6", ParseItem},                // число вместо формата
		{"28-1-6-7-6-13-8-21-8-ггммдд-лишнее", ParseItem},    // лишний токен после формата
		{"28-1-6-7-6-13-8-21-8-ггммдд-ддммгг", ParseItem},    // два формата
		{"33-1-6-7-6-13-3-16-8-24-8-ддммгггг-ещё", ParseBox}, // коробка: лишний токен
	}
	for _, c := range cases {
		if _, err := c.do(c.rule); err == nil {
			t.Errorf("разбор %q: ожидалась ошибка", c.rule)
		}
	}
}

// Длина поля даты обязана совпадать с форматом: раньше 6-значное поле
// сохранялось на карточке и падало только на приёмке («дата не распознана»),
// теперь отказ приходит при сохранении — с подсказкой о формате.
func TestParseDateLengthMustMatchFormat(t *testing.T) {
	bad := []struct {
		rule string
		do   func(string) (Rule, error)
	}{
		{"28-1-6-7-6-13-6-21-6", ParseItem},            // 6 цифр, формата нет
		{"28-1-6-7-6-13-6-21-6-ддммгггг", ParseItem},   // 6 цифр, формат 8-значный
		{"28-1-6-7-6-13-8-21-8-ггммдд", ParseItem},     // 8 цифр, формат 6-значный
		{"28-1-6-7-6-13-5-21-5-ггммдд", ParseItem},     // 5 цифр
		{"33-1-6-7-6-13-3-16-8-24-8-ддммгг", ParseBox}, // коробка: 8 цифр, формат 6-значный
	}
	for _, c := range bad {
		_, err := c.do(c.rule)
		if err == nil {
			t.Errorf("разбор %q: ожидалась ошибка длины поля даты", c.rule)
			continue
		}
		if !strings.Contains(err.Error(), "формат") {
			t.Errorf("разбор %q: в ошибке нет подсказки о формате: %v", c.rule, err)
		}
	}
	// Без токена подсказка называет оба 6-значных формата — оператор правит
	// карточку по тексту ошибки.
	_, err := ParseItem("28-1-6-7-6-13-6-21-6")
	if err == nil || !strings.Contains(err.Error(), string(DateYYMMDD)) || !strings.Contains(err.Error(), string(DateDDMMYY)) {
		t.Errorf("подсказка без форматов: %v", err)
	}
	// Поле даты не задано (пустая позиция) — формат ни при чём.
	if _, err := ParseItem("28-1-6-7-6- -0- -0"); err != nil {
		t.Errorf("правило без дат длиной 6 отклонено: %v", err)
	}
	if _, err := ParseBox("33-1-6-7-6-13-3- -0- -0"); err != nil {
		t.Errorf("правило коробки без дат отклонено: %v", err)
	}
}

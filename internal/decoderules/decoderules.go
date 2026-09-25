// Пакет decoderules — парсер правил вычитки штрих-кодов поставщиков.
//
// Формат правила: "<длина штрих-кода>-<позиция1>-<длина1>-<позиция2>-<длина2>-..."
// Пары «позиция-длина» идут по порядку полей (код товара, вес, ...), позиции
// 1-based — отсчёт от начала штрих-кода. Пример: "28-1-6-7-6-13-8-21-8" — код
// длиной 28: код товара с позиции 1 (6 знаков), вес с 7 (6), выработка
// с 13 (8), срок с 21 (8).
//
// Отсутствующее поле — пустая позиция (пробел между дефисами), длина пары при
// этом указывается, чтобы не ломалась чётность токенов. Пример: "28- -6-7-6-13-8-21-8" —
// код товара не вычитывается. Ноль — обычное значение, маркером отсутствия не служит.
//
// Даты в штрих-коде не всегда ДДММГГГГ: последним (необязательным) токеном
// правило может задавать формат дат — DateDDMMYY («ддммгг», 6 цифр) или
// DateYYMMDD («ггммдд», 6 цифр, GS1: AI 11 выработка, AI 17 срок). Пример кода
// поставщика длиной 50: "50-3-13-20-6-36-6-28-6-ггммдд" (срок в коде стоит
// раньше выработки — пары идут по полям правила, а не по позициям). Токена нет —
// даты читаются как ДДММГГГГ. Формат один на оба поля правила. Длина поля даты
// обязана совпадать с форматом (8 или 6 цифр): сверяется здесь, чтобы карточка
// поставщика падала при сохранении, а не приёмка на скане.
//
// Пакет — нижний слой (как internal/innercode): используется модулем поставщиков
// для валидации правил при сохранении и модулем приёмки для вычитки кодов.
package decoderules

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Поля правила единичного товара (decode_rules).
const (
	FieldCode       = iota // код товара у поставщика (external_code → product_supplier_barcodes)
	FieldWeight            // вес, граммы
	FieldProducedOn        // дата выработки
	FieldBestBefore        // срок годности
	ItemFieldCount
)

// Поля правила коробки (box_decode_rules): к полям товара добавляется кол-во
// вложений перед датами.
const (
	BoxCode       = iota // код товара
	BoxWeight            // общий вес коробки, граммы
	BoxQty               // кол-во вложений
	BoxProducedOn        // дата выработки
	BoxBestBefore        // срок годности
	BoxFieldCount
)

// Индексы полей-дат: их длина сверяется с форматом при разборе правила.
var (
	dateFieldsItem = []int{FieldProducedOn, FieldBestBefore}
	dateFieldsBox  = []int{BoxProducedOn, BoxBestBefore}
)

// DateFormat — формат дат правила (выработки и срока годности).
type DateFormat string

const (
	DateDDMMYYYY DateFormat = "ддммгггг" // 8 цифр, формат по умолчанию (токена в правиле нет)
	DateDDMMYY   DateFormat = "ддммгг"   // 6 цифр, день-месяц-год
	DateYYMMDD   DateFormat = "ггммдд"   // 6 цифр, год-месяц-день (GS1: AI 11/17)
)

// dateFormats — допустимые форматы; порядок используется в подсказке об ошибке.
var dateFormats = []DateFormat{DateDDMMYYYY, DateDDMMYY, DateYYMMDD}

// ParseDateFormat разбирает токен формата дат из правила.
func ParseDateFormat(s string) (DateFormat, error) {
	f := DateFormat(strings.ToLower(strings.TrimSpace(s)))
	if slices.Contains(dateFormats, f) {
		return f, nil
	}
	names := make([]string, 0, len(dateFormats))
	for _, known := range dateFormats {
		names = append(names, string(known))
	}
	return "", fmt.Errorf("формат дат %q неизвестен — допустимые: %s", s, strings.Join(names, ", "))
}

// Name — имя формата для сообщений; пустой формат (токена в правиле нет) —
// формат по умолчанию.
func (f DateFormat) Name() string {
	if f == "" {
		return string(DateDDMMYYYY)
	}
	return string(f)
}

// layouts — layout для time.Parse/time.Format по формату. Одна таблица вместо
// switch: значения не дублируются (revive identical-switch-branches), а новый
// формат добавляется одной строкой.
var layouts = map[DateFormat]string{
	DateDDMMYYYY: "02012006",
	DateDDMMYY:   "020106",
	DateYYMMDD:   "060102",
}

// Layout — layout для time.Parse/time.Format. Пустой («токена в правиле нет») и
// незнакомый формат — исторический ДДММГГГГ: незнакомый до приёмки не доходит,
// правило валидируется при сохранении карточки.
func (f DateFormat) Layout() string {
	if layout, ok := layouts[f]; ok {
		return layout
	}
	return layouts[DateDDMMYYYY]
}

// Digits — сколько цифр занимает дата в этом формате.
func (f DateFormat) Digits() int {
	return len(f.Layout())
}

// Field — одно поле правила.
type Field struct {
	Pos int // 1-based позиция начала в штрих-коде; 0 = поле не задано
	Len int // длина поля
}

// Rule — распарсенное правило вычитки.
type Rule struct {
	Length     int        // длина штрих-кода
	DateFormat DateFormat // формат дат полей выработки и срока; "" — ДДММГГГГ
	Fields     []Field    // поля по порядку (FieldCode, FieldWeight, ...)
}

// Has сообщает, задано ли поле с индексом i.
func (r Rule) Has(i int) bool {
	return i >= 0 && i < len(r.Fields) && r.Fields[i].Pos > 0
}

// Slice вырезает значение поля из штрих-кода; ok=false, если поле не задано
// или выходит за границы строки.
func (r Rule) Slice(raw string, i int) (string, bool) {
	if !r.Has(i) {
		return "", false
	}
	f := r.Fields[i]
	start := f.Pos - 1
	if start+f.Len > len(raw) {
		return "", false
	}
	return raw[start : start+f.Len], true
}

// ParseItem разбирает правило единичных товаров: ровно 4 пары
// (код, вес, выработка, срок).
func ParseItem(s string) (Rule, error) {
	return parse(s, ItemFieldCount, dateFieldsItem, "правило вычитки штрихкодов")
}

// ParseBox разбирает правило коробок: ровно 5 пар
// (код, вес, кол-во вложений, выработка, срок).
func ParseBox(s string) (Rule, error) {
	return parse(s, BoxFieldCount, dateFieldsBox, "правило вычитки коробок")
}

// parse разбирает правило: длина штрих-кода + fields пар «позиция-длина» +
// необязательный хвостовой токен формата дат.
func parse(s string, fields int, dateFields []int, label string) (Rule, error) {
	parts := strings.Split(strings.TrimSpace(s), "-")

	length, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || length <= 0 {
		return Rule{}, fmt.Errorf("%s: первым числом должна быть длина штрих-кода (положительное число), получили %q", label, parts[0])
	}

	// После длины — ровно fields пар (2*fields токенов); лишний токен в конце —
	// формат дат.
	tokens := parts[1:]
	var format DateFormat
	switch len(tokens) {
	case fields * 2:
	case fields*2 + 1:
		format, err = ParseDateFormat(tokens[fields*2])
		if err != nil {
			return Rule{}, fmt.Errorf("%s: %w", label, err)
		}
		tokens = tokens[:fields*2]
	default:
		return Rule{}, fmt.Errorf("%s: ожидается %d пар «позиция-длина» и необязательный токен формата дат в конце, получилось %d токенов после длины", label, fields, len(tokens))
	}

	rule := Rule{Length: length, DateFormat: format, Fields: make([]Field, fields)}
	for i := range fields {
		posTok := strings.TrimSpace(tokens[2*i])
		lenTok := strings.TrimSpace(tokens[2*i+1])

		f := Field{Pos: 0, Len: 0}
		if posTok == "" {
			// Поле не задано: длина пары указывается, но значения нет.
			if _, err := strconv.Atoi(lenTok); err != nil {
				return Rule{}, fmt.Errorf("%s: поле %d: длина %q не число", label, i+1, lenTok)
			}
		} else {
			pos, err := strconv.Atoi(posTok)
			if err != nil || pos <= 0 {
				return Rule{}, fmt.Errorf("%s: поле %d: позиция %q должна быть положительным числом или пустой", label, i+1, posTok)
			}
			ln, err := strconv.Atoi(lenTok)
			if err != nil || ln <= 0 {
				return Rule{}, fmt.Errorf("%s: поле %d: длина %q должна быть положительным числом", label, i+1, lenTok)
			}
			if pos+ln-1 > length {
				return Rule{}, fmt.Errorf("%s: поле %d (позиция %d, длина %d) выходит за пределы кода длиной %d", label, i+1, pos, ln, length)
			}
			f = Field{Pos: pos, Len: ln}
		}
		rule.Fields[i] = f
	}

	if err := checkDateLengths(rule, dateFields, label); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

// checkDateLengths сверяет длину полей-дат с форматом: 8 цифр для ДДММГГГГ,
// 6 — для ддммгг/ггммдд. Без этой проверки поле даты длиной 6 сохранялось на
// карточке, а падало только на приёмке («дата не распознана»).
func checkDateLengths(r Rule, dateFields []int, label string) error {
	want := r.DateFormat.Digits()
	for _, i := range dateFields {
		if !r.Has(i) {
			continue
		}
		if got := r.Fields[i].Len; got != want {
			hint := ""
			if r.DateFormat == "" {
				hint = fmt.Sprintf(" — если даты в ШК идут в другом порядке, добавьте в конец правила формат: %s или %s", DateYYMMDD, DateDDMMYY)
			}
			return fmt.Errorf("%s: поле %d (дата) длиной %d не подходит формату %s (%d цифр)%s",
				label, i+1, got, r.DateFormat.Name(), want, hint)
		}
	}
	return nil
}

// Пакет decoderules — парсер правил вычитки штрих-кодов поставщиков.
//
// Формат правила: "<длина штрих-кода>-<позиция1>-<длина1>-<позиция2>-<длина2>-..."
// Пары «позиция-длина» идут по порядку полей (код товара, вес, ...), позиции
// 1-based — отсчёт от начала штрих-кода. Пример: "28-1-6-7-6-13-8-21-8" —
// код длиной 28: код товара с позиции 1 (6 знаков), вес с 7 (6), выработка
// с 13 (8), срок с 21 (8).
//
// Отсутствующее поле — пустая позиция (пробел между дефисами), длина пары при
// этом указывается, чтобы не ломалась чётность токенов. Пример: "28- -6-7-6-13-8-21-8" —
// код товара не вычитывается. Ноль — обычное значение, маркером отсутствия не служит.
//
// Пакет — нижний слой (как internal/innercode): используется модулем поставщиков
// для валидации правил при сохранении и модулем приёмки для вычитки кодов.
package decoderules

import (
	"fmt"
	"strconv"
	"strings"
)

// Поля правила единичного товара (decode_rules).
const (
	FieldCode       = iota // код товара у поставщика (external_code → product_supplier_barcodes)
	FieldWeight            // вес, граммы
	FieldProducedOn        // дата выработки, ДДММГГГГ
	FieldBestBefore        // срок годности, ДДММГГГГ
	ItemFieldCount
)

// Поля правила коробки (box_decode_rules): к полям товара добавляется кол-во
// вложений перед датами.
const (
	BoxCode       = iota // код товара
	BoxWeight            // общий вес коробки, граммы
	BoxQty               // кол-во вложений
	BoxProducedOn        // дата выработки, ДДММГГГГ
	BoxBestBefore        // срок годности, ДДММГГГГ
	BoxFieldCount
)

// Field — одно поле правила.
type Field struct {
	Pos int // 1-based позиция начала в штрих-коде; 0 = поле не задано
	Len int // длина поля
}

// Rule — распарсенное правило вычитки.
type Rule struct {
	Length int     // длина штрих-кода
	Fields []Field // поля по порядку (FieldCode, FieldWeight, ...)
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
	return parse(s, ItemFieldCount, "правило вычитки штрихкодов")
}

// ParseBox разбирает правило коробок: ровно 5 пар
// (код, вес, кол-во вложений, выработка, срок).
func ParseBox(s string) (Rule, error) {
	return parse(s, BoxFieldCount, "правило вычитки коробок")
}

// parse разбирает правило: длина штрих-кода + fields пар «позиция-длина».
func parse(s string, fields int, label string) (Rule, error) {
	parts := strings.Split(strings.TrimSpace(s), "-")

	length, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || length <= 0 {
		return Rule{}, fmt.Errorf("%s: первым числом должна быть длина штрих-кода (положительное число), получили %q", label, parts[0])
	}
	if len(parts)-1 != fields*2 {
		return Rule{}, fmt.Errorf("%s: ожидается %d пар «позиция-длина», получилось %d", label, fields, (len(parts)-1)/2)
	}

	rule := Rule{Length: length, Fields: make([]Field, fields)}
	for i := range fields {
		posTok := strings.TrimSpace(parts[1+2*i])
		lenTok := strings.TrimSpace(parts[2+2*i])

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
	return rule, nil
}

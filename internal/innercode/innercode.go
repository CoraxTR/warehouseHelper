// Package innercode разбирает внутренние штрих-коды склада: единицы товара
// (куски) и коробки. Форматы жёстко закреплены в коде — это инвариант
// товарного домена, в базе они не хранятся (в отличие от правил вычитки
// штрих-кодов поставщиков).
package innercode

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Kind — тип упаковки, закодированный во внутреннем штрих-коде.
type Kind uint8

const (
	// KindItem — единица товара (кусок/штука).
	KindItem Kind = iota + 1
	// KindBox — коробка с несколькими единицами одного товара.
	KindBox
)

// String возвращает читаемое имя типа упаковки (для логов).
func (k Kind) String() string {
	switch k {
	case KindItem:
		return "item"
	case KindBox:
		return "box"
	default:
		return "unknown"
	}
}

// Code — результат разбора внутреннего штрих-кода.
type Code struct {
	Kind         Kind
	InternalCode string    // 8 цифр: режим (0/1/2) + группа (3) + номер (4)
	WeightG      int       // граммы: единица — вес одной штуки; коробка — общий вес
	Qty          int       // единица — 1; коробка — число вложений
	ProdDate     time.Time // дата выработки
	ExpDate      time.Time // срок годности
}

// Ошибки разбора.
var (
	// ErrNotInternal — штрих-код не внутреннего формата (длина не 29/33).
	// Вызывающий код может передать строку вычитывателю штрих-кодов поставщиков.
	ErrNotInternal = errors.New("innercode: not an internal barcode")

	// ErrInvalid — штрих-код внутреннего формата, но данные невалидны.
	ErrInvalid = errors.New("innercode: invalid internal barcode")
)

// Длины форматов и полей.
const (
	itemLen = 29 // internal_code(8) + вес(5) + выработка(8) + срок(8)
	boxLen  = 33 // internal_code(8) + вес(6) + кол-во(3) + выработка(8) + срок(8)

	codeLen       = 8
	itemWeightLen = 5
	boxWeightLen  = 6
	qtyLen        = 3
	dateLen       = 8

	maxQty = 999 // предел трёх цифр
)

// Parse разбирает внутренний штрих-код.
//
// Строка не внутреннего формата (длина не 29 и не 33) возвращает
// ErrNotInternal — например, это штрих-код поставщика, и его нужно передать
// другому вычитывателю. Строка правильной длины с невалидными данными
// возвращает ErrInvalid.
func Parse(raw string) (Code, error) {
	switch len(raw) {
	case itemLen:
		return parseItem(raw)
	case boxLen:
		return parseBox(raw)
	default:
		return Code{}, fmt.Errorf("%w: length %d", ErrNotInternal, len(raw))
	}
}

// ValidLengths возвращает допустимые длины внутренних штрих-кодов (29 — кусок,
// 33 — коробка). Это «правила проверки» разбирателя для потребителей, которым
// нужно отсеивать чужие штрих-коды до полного разбора (например, на клиенте
// страницы сканирования): значение приходит от владельца формата, а не
// дублируется в вызывающем коде.
func ValidLengths() []int {
	return []int{itemLen, boxLen}
}

func parseItem(raw string) (Code, error) {
	c := Code{Kind: KindItem, Qty: 1}

	code, err := parseInternalCode(raw[0:codeLen])
	if err != nil {
		return Code{}, err
	}
	c.InternalCode = code

	weight, err := parseWeight(raw[codeLen : codeLen+itemWeightLen])
	if err != nil {
		return Code{}, err
	}
	c.WeightG = weight

	prod, exp, err := parseDates(raw[codeLen+itemWeightLen:])
	if err != nil {
		return Code{}, err
	}
	c.ProdDate, c.ExpDate = prod, exp

	return c, nil
}

func parseBox(raw string) (Code, error) {
	c := Code{Kind: KindBox}

	code, err := parseInternalCode(raw[0:codeLen])
	if err != nil {
		return Code{}, err
	}
	c.InternalCode = code

	weight, err := parseWeight(raw[codeLen : codeLen+boxWeightLen])
	if err != nil {
		return Code{}, err
	}
	c.WeightG = weight

	qty, err := parseQty(raw[codeLen+boxWeightLen : codeLen+boxWeightLen+qtyLen])
	if err != nil {
		return Code{}, err
	}
	c.Qty = qty

	prod, exp, err := parseDates(raw[codeLen+boxWeightLen+qtyLen:])
	if err != nil {
		return Code{}, err
	}
	c.ProdDate, c.ExpDate = prod, exp

	return c, nil
}

// parseInternalCode проверяет внутренний код: ровно 8 цифр, первая — режим
// учёта (0 охлаждёнка/Вагю, 1 заморозка, 2 сопутка).
func parseInternalCode(s string) (string, error) {
	if len(s) != codeLen || !digitsOnly(s) {
		return "", fmt.Errorf("%w: internal_code %q", ErrInvalid, s)
	}
	if s[0] < '0' || s[0] > '2' {
		return "", fmt.Errorf("%w: internal_code %q: mode must be 0, 1 or 2", ErrInvalid, s)
	}
	return s, nil
}

// parseWeight разбирает вес в граммах: только цифры, строго больше нуля.
func parseWeight(s string) (int, error) {
	if len(s) != itemWeightLen && len(s) != boxWeightLen {
		return 0, fmt.Errorf("%w: weight %q: bad length", ErrInvalid, s)
	}
	if !digitsOnly(s) {
		return 0, fmt.Errorf("%w: weight %q: not digits", ErrInvalid, s)
	}
	n, _ := strconv.ParseUint(s, 10, 32)
	if n == 0 {
		return 0, fmt.Errorf("%w: weight %q: must be positive", ErrInvalid, s)
	}
	return int(n), nil
}

// parseQty разбирает число вложений в коробке: 1..999.
func parseQty(s string) (int, error) {
	if len(s) != qtyLen || !digitsOnly(s) {
		return 0, fmt.Errorf("%w: qty %q", ErrInvalid, s)
	}
	n, _ := strconv.ParseUint(s, 10, 32)
	if n == 0 || n > maxQty {
		return 0, fmt.Errorf("%w: qty %q: out of range 1..999", ErrInvalid, s)
	}
	return int(n), nil
}

// parseDates разбирает даты выработки и срока годности (ДДММГГГГ) и проверяет,
// что срок годности не раньше даты выработки.
func parseDates(s string) (prod, exp time.Time, err error) {
	if len(s) != 2*dateLen {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: dates %q: bad length", ErrInvalid, s)
	}
	prod, err = parseDate(s[:dateLen])
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	exp, err = parseDate(s[dateLen:])
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if prod.After(exp) {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"%w: exp date %s before prod date %s",
			ErrInvalid, exp.Format("02.01.2006"), prod.Format("02.01.2006"))
	}
	return prod, exp, nil
}

// parseDate разбирает одну дату в формате ДДММГГГГ с проверкой календарной
// корректности (включая високосные годы).
func parseDate(s string) (time.Time, error) {
	if len(s) != dateLen || !digitsOnly(s) {
		return time.Time{}, fmt.Errorf("%w: date %q: want DDMMYYYY", ErrInvalid, s)
	}
	d, err := time.Parse("02012006", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: date %q: %w", ErrInvalid, s, err)
	}
	return d, nil
}

func digitsOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

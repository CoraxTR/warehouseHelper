// Пакет scanmatch — общее ядро сверки сканов склада: из чего собираются
// строки ожидания и как их гасят сканы этикеток. Про МойСклад и БД пакет не
// знает: состав строк собирает вызывающий код, а сами правила (единицы сверки,
// весовую строку гасит ровно один скан с тем же весом, штучную — ExpectedQty
// сканов, несоответствие — ValidationError) живут здесь. Переиспользуют
// возврат в продажу (internal/returns) и возврат в сроки при переподборе
// (internal/msorders).
package scanmatch

import "math"

// Expected — строка ожидания: РОВНО одна строка отчёта (позиция живого заказа /
// удалённая позиция диффа / строка подбора) — одна строка формы, без склейки
// по товару. Решение владельца 10.09: 5 строк одного товара с разными весами
// показываются как 5 строк, каждая гасится своим сканом; штучная строка «5 шт»
// остаётся одной строкой и гасится пятью сканами. В БД не хранится.
type Expected struct {
	Idx          int    // порядок строки в отчёте: дубли «код + вес» различимы только по нему
	ProductID    string // uuid товара в МС (для записи в остатки)
	InternalCode string // код склада (первые 8 цифр этикетки)
	Name         string // название товара
	Weighted     bool   // весовой (сверка в граммах) или штучный (сверка по количеству)
	// ExpectedQty — ожидание строки: весовой — граммы (вес ЭТОЙ позиции),
	// штучный — количество единиц. Весовую строку гасит ровно один скан с тем
	// же весом; штучную — ExpectedQty сканов.
	ExpectedQty int64
}

// Candidate — строка-кандидат возврата: позиция заказа, удалённая позиция
// диффа аудита или строка подбора, из которой собирается ожидание (или
// отбрасывается — правила отбора в BuildExpected).
type Candidate struct {
	ProductID string  // uuid товара (последний сегмент meta.href)
	Name      string  // название (из диффа / позиции заказа)
	Quantity  float64 // количество строки (кг для весовых, штуки для штучных)
	Reserve   float64 // резерв строки на момент проверки
}

// CatalogProduct — товар каталога склада, нужный сверке.
type CatalogProduct struct {
	ProductID    string // uuid товара в МС
	InternalCode string // код склада; пусто — товар без кода, в возврат не идёт
	Weighted     bool   // весовой (сверка в граммах) или штучный (по количеству)
}

// QtyUnit — единица сверки количества строки: весовой товар сводится в граммы
// (кг × 1000), штучный — в единицы.
type QtyUnit uint8

const (
	// QtyGrams — весовой товар: сравнение/накопление в граммах.
	QtyGrams QtyUnit = iota
	// QtyPieces — штучный товар: по количеству единиц.
	QtyPieces
)

// QtyInt — количество в единицах сверки: весовой товар → граммы
// (round кг×1000 — вес этикетки 29 в граммах), штучный → штуки (round).
func QtyInt(v float64, u QtyUnit) int64 {
	if u == QtyGrams {
		return int64(math.Round(v * 1000))
	}
	return int64(math.Round(v))
}

// ReservedEquals — «товар физически отложен»: quantity == reserve строго,
// без допуска (решение пользователя). Сравнение в единицах сверки (int),
// не float по кг — 0.657 и 0.657 в double равны, но округление защищает
// от хвостов вида 0.6570000000001.
func ReservedEquals(q, r float64, u QtyUnit) bool {
	return QtyInt(q, u) == QtyInt(r, u)
}

// BuildExpected собирает ожидания по строкам-кандидатам и каталогу: по строке
// на КАЖДУЮ прошедшую фильтры строку, без склейки по товару (решение владельца
// 10.09). Порядок строк — порядок кандидатов, он же Idx. Пропускаются: строки
// без резерва (quantity != reserve — «товар не был физически отложен»), без
// internal_code, неизвестные каталогу и с нулевым количеством. Пустой результат
// означает «возвращать нечего» — ошибкой это считать или нет, решает вызывающий.
func BuildExpected(cands []Candidate, products map[string]CatalogProduct) []Expected {
	expected := make([]Expected, 0, len(cands))
	for _, c := range cands {
		p, ok := products[c.ProductID]
		if !ok || p.InternalCode == "" {
			continue // нет в каталоге или без кода склада — не складской товар
		}

		unit := QtyPieces
		if p.Weighted {
			unit = QtyGrams
		}
		if !ReservedEquals(c.Quantity, c.Reserve, unit) {
			continue // не отложен физически — возвращать нечего
		}
		qty := QtyInt(c.Quantity, unit)
		if qty <= 0 {
			continue // пустая строка: погасить её сканом нельзя
		}

		expected = append(expected, Expected{
			Idx:          len(expected),
			ProductID:    c.ProductID,
			InternalCode: p.InternalCode,
			Name:         c.Name,
			Weighted:     p.Weighted,
			ExpectedQty:  qty,
		})
	}
	return expected
}

// Пакет stockreport — ежедневный отчёт «остатки против средних продаж» в чат
// управления складом.
//
// Отчёт отвечает на три вопроса управления (решения владельца 11.11.2026):
//   - чего нет в наличии — заказывать;
//   - что кончается: остаток не больше средней продажи за неделю;
//   - что лежит: запас от двух недель и больше, плюс позиции без истории
//     продаж (оценить нечем, но остаток есть) — они идут в конец списка.
//
// Позиции, не попавшие ни в один список, в отчёт не входят вовсе.
//
// Пакет не знает ни про БД, ни про Telegram: он раскладывает готовые позиции
// по спискам и собирает строки сообщений (юнит-тесты — без моков).
package stockreport

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	// excessFactor — порог «избытка»: остаток ≥ 2 средних продаж за неделю.
	excessFactor = 2
	// precision — знаков после запятой у среднего и запаса в неделях
	// (решение владельца: округление до 1 знака).
	precision = 1
)

// Position — позиция отчёта: товар, физический остаток и средняя продажа за
// неделю.
type Position struct {
	Name string
	Qty  int64 // остаток, шт
	// AvgWeek — средняя продажа за неделю, шт; nil — истории продаж нет
	// (товар не продавался ни за один период окна). Модуль средних продаж
	// отдаёт средние в недельной единице: недельные товары — как есть,
	// месячные — среднемесячные / 4.
	AvgWeek *float64
}

// Report — списки отчёта. NoHistory — позиции с остатком, но без истории
// продаж: сравнивать не с чем, они дописываются в конец сообщения об избытке.
type Report struct {
	OutOfStock []Position
	LowStock   []Position
	Excess     []Position
	NoHistory  []Position
}

// Empty — отправлять нечего: все списки пусты.
func (r Report) Empty() bool {
	return len(r.OutOfStock) == 0 && len(r.LowStock) == 0 &&
		len(r.Excess) == 0 && len(r.NoHistory) == 0
}

// Split раскладывает позиции по спискам. Порядок правил важен:
//
//  1. qty ≤ 0 → «нет в наличии» и ТОЛЬКО туда: нулевой остаток не дублируется
//     в «кончается»/«избытке», даже когда средняя известна;
//  2. средняя неизвестна (nil или ≤ 0 — делить на неё нельзя, и продаж по
//     сути не было) → «нет истории продаж» (хвост сообщения об избытке);
//  3. qty ≤ средней недельной → «кончается»;
//  4. qty ≥ 2 × средней недельной → «избыток»;
//  5. остальное → никуда: остаток больше недельной нормы, но меньше двух —
//     повода писать нет.
//
// Все списки сортируются по имени: порядок обхода кэша остатков
// недетерминирован, а сообщение должно быть одинаковым от прогона к прогону.
func Split(positions []Position) Report {
	var r Report

	for _, p := range positions {
		switch {
		case p.Qty <= 0:
			r.OutOfStock = append(r.OutOfStock, p)
		case p.AvgWeek == nil || *p.AvgWeek <= 0:
			r.NoHistory = append(r.NoHistory, p)
		case float64(p.Qty) <= *p.AvgWeek:
			r.LowStock = append(r.LowStock, p)
		case float64(p.Qty) >= *p.AvgWeek*excessFactor:
			r.Excess = append(r.Excess, p)
		}
	}

	sortByName(r.OutOfStock)
	sortByName(r.LowStock)
	sortByName(r.Excess)
	sortByName(r.NoHistory)

	return r
}

// OutOfStockLines — строки «нет в наличии»: «{name} - нет в наличии».
func (r Report) OutOfStockLines() []string {
	lines := make([]string, 0, len(r.OutOfStock))
	for _, p := range r.OutOfStock {
		lines = append(lines, p.Name+" - нет в наличии")
	}

	return lines
}

// LowStockLines — строки «кончается»: остаток и средняя продажа за неделю.
func (r Report) LowStockLines() []string {
	lines := make([]string, 0, len(r.LowStock))
	for _, p := range r.LowStock {
		lines = append(lines, fmt.Sprintf("%s - в наличии: %d - среднее в неделю: %s",
			p.Name, p.Qty, formatAvg(*p.AvgWeek)))
	}

	return lines
}

// ExcessLines — строки «избытка»: запас в неделях, а в конце — позиции без
// истории продаж отдельными строками «{name} - нет истории продаж».
func (r Report) ExcessLines() []string {
	lines := make([]string, 0, len(r.Excess)+len(r.NoHistory))
	for _, p := range r.Excess {
		lines = append(lines, fmt.Sprintf("%s - в наличии: %d - запас на %s недель",
			p.Name, p.Qty, formatAvg(float64(p.Qty) / *p.AvgWeek)))
	}
	for _, p := range r.NoHistory {
		lines = append(lines, p.Name+" - нет истории продаж")
	}

	return lines
}

// formatAvg — число с одним знаком после запятой: разделитель — точка
// (так записано в ТЗ: «2.5 недель»), в сообщении это не таблица, а строка.
func formatAvg(v float64) string {
	return strconv.FormatFloat(v, 'f', precision, 64)
}

// sortByName — по имени без учёта регистра, при равенстве — по точному имени:
// сортировка должна быть устойчивой к «Курица» / «курица».
func sortByName(positions []Position) {
	sort.Slice(positions, func(i, j int) bool {
		a, b := strings.ToLower(positions[i].Name), strings.ToLower(positions[j].Name)
		if a != b {
			return a < b
		}

		return positions[i].Name < positions[j].Name
	})
}

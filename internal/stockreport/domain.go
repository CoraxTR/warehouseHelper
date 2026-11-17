// Пакет stockreport — ежедневный отчёт «остатки против средних продаж» в чат
// управления складом.
//
// Отчёт отвечает на три вопроса управления:
//   - чего нет в наличии — заказывать;
//   - что кончается: остаток не больше средней продажи за неделю;
//   - где коэффициент изменения заказа больше единицы: позиция распродаётся
//     быстрее, чем пополняется (решение владельца 17.11.2026: порог «запас
//     от двух недель» отменён, блок отбирается ТОЛЬКО по коэффициенту).
//     Позиции без истории продаж попадают в этот же блок, если коэффициент
//     больше единицы: сравнивать остаток не с чем, но повод писать есть.
//
// Позиции, не попавшие ни в один список, в отчёт не входят вовсе.
//
// Пакет не знает ни про БД, ни про Telegram: он раскладывает готовые позиции
// по спискам и собирает блоки сообщений в HTML (parse_mode=HTML; названия
// товаров экранируются). Юнит-тесты — без моков.
package stockreport

import (
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
)

const (
	// excessCoeff — порог блока «избыток»: коэффициент больше единицы.
	excessCoeff = 1
	// precision — знаков после запятой у среднего и запаса в неделях
	// (решение владельца: округление до 1 знака).
	precision = 1
)

// Position — позиция отчёта: товар, физический остаток, средняя продажа за
// неделю и коэффициент изменения заказа текущего периода.
type Position struct {
	Name string
	Qty  int64 // остаток, шт
	// AvgWeek — средняя продажа за неделю, шт; nil — истории продаж нет
	// (товар не продавался ни за один период окна). Модуль средних продаж
	// отдаёт средние в недельной единице: недельные товары — как есть,
	// месячные — среднемесячные / 4.
	AvgWeek *float64
	// Coeff — накопленный коэффициент изменения заказа текущего периода
	// (модуль ordercoeff): недельный у недельных товаров, месячный у
	// остальных. 0 — событий периода не было.
	Coeff int16
}

// Report — списки отчёта.
type Report struct {
	OutOfStock []Position
	LowStock   []Position
	Excess     []Position
}

// Empty — отправлять нечего: все списки пусты.
func (r Report) Empty() bool {
	return len(r.OutOfStock) == 0 && len(r.LowStock) == 0 && len(r.Excess) == 0
}

// Split раскладывает позиции по спискам. Порядок правил важен:
//
//  1. qty ≤ 0 → «нет в наличии» и ТОЛЬКО туда: нулевой остаток не дублируется
//     в «кончается»/«избытке», даже когда средняя известна;
//  2. средняя неизвестна (nil или ≤ 0 — делить на неё нельзя, и продаж по
//     сути не было): пишем только при коэффициенте больше единицы — в
//     «избыток», строкой «нет истории продаж»;
//  3. qty ≤ средней недельной → «кончается» (это действие важнее коэффициента);
//  4. коэффициент больше единицы → «избыток»;
//  5. остальное → никуда: остаток выше недельной нормы и повода писать нет.
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
			if p.Coeff > excessCoeff {
				r.Excess = append(r.Excess, p)
			}
		case float64(p.Qty) <= *p.AvgWeek:
			r.LowStock = append(r.LowStock, p)
		case p.Coeff > excessCoeff:
			r.Excess = append(r.Excess, p)
		default:
			// Остаток выше недельной нормы, коэффициент не больше единицы —
			// повода писать нет.
		}
	}

	sortByName(r.OutOfStock)
	sortByName(r.LowStock)
	sortByName(r.Excess)

	return r
}

// OutOfStockItems — блоки «нет в наличии»: «№. {name}».
func (r Report) OutOfStockItems() []string {
	items := make([]string, 0, len(r.OutOfStock))
	for i, p := range r.OutOfStock {
		items = append(items, fmt.Sprintf("<b>%d. %s</b>", i+1, escape(p.Name)))
	}

	return items
}

// LowStockItems — блоки «кончается»: имя и строка цифр (остаток, средняя).
func (r Report) LowStockItems() []string {
	items := make([]string, 0, len(r.LowStock))
	for i, p := range r.LowStock {
		items = append(items, fmt.Sprintf("<b>%d. %s</b>\n<code>остаток %d шт · среднее %s/нед</code>",
			i+1, escape(p.Name), p.Qty, formatAvg(*p.AvgWeek)))
	}

	return items
}

// ExcessItems — блоки «избыток»: имя и строка цифр (остаток, запас в неделях,
// коэффициент). Позиция без истории продаж: вместо запаса — «нет истории
// продаж», остаток всё равно показываем.
func (r Report) ExcessItems() []string {
	items := make([]string, 0, len(r.Excess))
	for i, p := range r.Excess {
		details := fmt.Sprintf("%d шт · нет истории продаж", p.Qty)
		if p.AvgWeek != nil && *p.AvgWeek > 0 {
			details = fmt.Sprintf("%d шт · запас %s нед", p.Qty, formatAvg(float64(p.Qty) / *p.AvgWeek))
		}
		items = append(items, fmt.Sprintf("<b>%d. %s</b>\n<code>%s · коэф %d</code>",
			i+1, escape(p.Name), details, p.Coeff))
	}

	return items
}

// escape — название товара для parse_mode=HTML: «&», «<», «>» в названиях
// ломают разметку, и сообщение целиком не уходит.
func escape(name string) string {
	return html.EscapeString(name)
}

// formatAvg — число с одним знаком после запятой: разделитель — точка
// (так записано в ТЗ: «2.5 недель»), в сообщении это не таблица, а строка.
func formatAvg(v float64) string {
	return strconv.FormatFloat(v, 'f', precision, 64)
}

// sortByName — по имени без учёта регистра, при равенстве — по точному имени:
// сортировка должна быть устойчивой к «Курица» / «курица».
func sortByName(positions []Position) {
	slices.SortFunc(positions, func(a, b Position) int {
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return strings.Compare(la, lb)
		}

		return strings.Compare(a.Name, b.Name)
	})
}

// Пакет stockreport — ежедневный отчёт «остатки против средних продаж» в чат
// управления складом.
//
// Отчёт отвечает на три вопроса управления:
//   - чего нет в наличии — заказывать;
//   - что кончается: остаток не больше средней продажи за неделю;
//   - где избыток: остаток, накопленный до срока, больше того, что успеем
//     продать до этой даты (коэффициент избытка больше единицы).
//
// Блок избытка НЕ считается здесь: это расчёт модуля скидок (тот же, что даёт
// скидку 10 % по избытку), отчёт только раскладывает готовые пары по блокам
// (решение владельца 17.11.2026: числа в чате обязаны совпадать с движком).
//
// Пакет не знает ни про БД, ни про Telegram: он раскладывает готовые позиции
// по спискам и собирает блоки сообщений в HTML (parse_mode=HTML; названия
// товаров экранируются). Юнит-тесты — без моков.
package stockreport

import (
	"fmt"
	"html"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/discounts"
)

const (
	// dateOnly — формат срока годности в блоке избытка: день и месяц, год
	// лишний шум в телефоне.
	dateOnly = "02.01"
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

// Report — списки отчёта.
type Report struct {
	OutOfStock []Position
	LowStock   []Position
	// Surplus — избытки от модуля скидок: пара (товар + срок) с накопленным
	// остатком и планом продаж до срока.
	Surplus []discounts.SurplusLot
}

// Empty — отправлять нечего: все списки пусты.
func (r Report) Empty() bool {
	return len(r.OutOfStock) == 0 && len(r.LowStock) == 0 && len(r.Surplus) == 0
}

// Split раскладывает позиции по спискам. Порядок правил важен:
//
//  1. qty ≤ 0 → «нет в наличии» и ТОЛЬКО туда: нулевой остаток не дублируется
//     в «кончается», даже когда средняя известна;
//  2. средняя неизвестна (nil или ≤ 0 — делить на неё нельзя, и продаж по
//     сути не было): никуда — сравнивать остаток не с чем;
//  3. qty ≤ средней недельной → «кончается»;
//  4. остальное → никуда: повода писать нет.
//
// Избыток сюда не входит вовсе — его отдаёт модуль скидок (см. Report.Surplus).
func Split(positions []Position) Report {
	var r Report

	for _, p := range positions {
		switch {
		case p.Qty <= 0:
			r.OutOfStock = append(r.OutOfStock, p)
		case p.AvgWeek == nil || *p.AvgWeek <= 0:
			// Истории продаж нет: остаток сравнивать не с чем.
		case float64(p.Qty) <= *p.AvgWeek:
			r.LowStock = append(r.LowStock, p)
		default:
			// Остаток выше недельной нормы и срок не поджимает — повода
			// писать нет.
		}
	}

	sortByName(r.OutOfStock)
	sortByName(r.LowStock)

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
		items = append(items, fmt.Sprintf("<b>%d. %s</b>\n<code>остаток %d шт · продажа %s/нед</code>",
			i+1, escape(p.Name), p.Qty, formatAvg(*p.AvgWeek)))
	}

	return items
}

// SurplusItems — блоки «избыток»: одна позиция на ТОВАР, внутри — строки по
// срокам. Позиция и срок, накопленный до срока остаток, план продаж до даты и
// пометка, если сайт уже живёт ступенью по сроку (решение владельца 17.11.2026).
//
// Товар повторяется один раз: у лота с тремя сроками три отдельные строки
// читались бы как три разных товара, а сортировка по коэффициенту ещё и
// раскидывала бы их по списку.
func (r Report) SurplusItems() []string {
	groups := groupSurplus(r.Surplus)

	items := make([]string, 0, len(groups))
	for i, g := range groups {
		lines := make([]string, 0, len(g.Lots))
		for _, s := range g.Lots {
			line := fmt.Sprintf("до %s — накоплено %d шт · продадим ~%d шт",
				s.BestBefore.Format(dateOnly), s.CumQty, s.PlannedQty)
			if s.ExpiryWins() {
				line += fmt.Sprintf(" · уже по сроку %d %%", *s.Expiry)
			}
			lines = append(lines, "<code>"+line+"</code>")
		}
		items = append(items, fmt.Sprintf("<b>%d. %s</b> · коэф до %s\n%s",
			i+1, escape(g.Name), formatCoeff(g.Lots[0].Coeff), strings.Join(lines, "\n")))
	}

	return items
}

// surplusGroup — позиция блока избытка: товар и его сроки с избытком.
type surplusGroup struct {
	Name string
	Lots []discounts.SurplusLot
}

// groupSurplus — товары в порядке модуля скидок (по убыванию коэффициента),
// сроки внутри — по возрастанию срока: накопленный остаток читается сверху
// вниз, как FIFO.
func groupSurplus(lots []discounts.SurplusLot) []surplusGroup {
	groups := make([]surplusGroup, 0, len(lots))
	index := make(map[string]int, len(lots))

	for _, s := range lots {
		idx, ok := index[s.ProductID]
		if !ok {
			idx = len(groups)
			index[s.ProductID] = idx
			groups = append(groups, surplusGroup{Name: s.Name})
		}
		groups[idx].Lots = append(groups[idx].Lots, s)
	}

	for i := range groups {
		sortLotsByDate(groups[i].Lots)
	}

	return groups
}

// sortLotsByDate — сроки внутри позиции по возрастанию даты.
func sortLotsByDate(lots []discounts.SurplusLot) {
	slices.SortStableFunc(lots, func(a, b discounts.SurplusLot) int {
		return a.BestBefore.Compare(b.BestBefore)
	})
}

// sortByName — порядок по имени: обход кэша остатков недетерминирован, а
// сообщение должно быть одинаковым от прогона к прогону.
func sortByName(ps []Position) {
	slices.SortStableFunc(ps, func(a, b Position) int {
		return strings.Compare(a.Name, b.Name)
	})
}

// escape — название товара для parse_mode=HTML.
func escape(s string) string {
	return html.EscapeString(s)
}

// formatAvg — средняя продажа целым числом: дробные штуки в чате не нужны
// (решение владельца 17.11.2026).
func formatAvg(v float64) string {
	return strconv.FormatInt(int64(math.Round(v)), 10)
}

// formatCoeff — коэффициент избытка: всегда один знак после запятой. Ноль
// после точки не срезаем: «коэф 1» читалось бы как «избытка нет».
func formatCoeff(v float64) string {
	return strings.ReplaceAll(strconv.FormatFloat(v, 'f', 1, 64), ".", ",")
}

// SurplusDate — срок пары в формате сообщения (для тестов и вызывающих).
func SurplusDate(t time.Time) string {
	return t.Format(dateOnly)
}

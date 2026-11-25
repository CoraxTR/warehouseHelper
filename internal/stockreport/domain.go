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

// RichMarkdown — сообщение отчёта для rich-сообщения Telegram
// (sendRichMessage, markdown): три блока под стрелкой (<details>), «кончается»
// и «избыток» — таблицами с рамками. Пустой блок не печатается, пустой отчёт —
// пустая строка (слать нечего).
//
// Решения владельца 25.11.2026: линии между товарами (bordered); «остаток» и
// «продажа» — отдельными колонками; «накоплено / продадим» — одной ячейкой;
// коэффициент избытка в таблице не печатается; подсветка (mark) убрана —
// белый текст на светло-жёлтом не читался, скидка выделена жирным.
//
// Названия экранируются: в rich-режиме текст разбирается как HTML, и «Z&P» в
// имени товара иначе ломает разметку.
func (r Report) RichMarkdown() string {
	var b strings.Builder

	if len(r.OutOfStock) > 0 {
		fmt.Fprintf(&b, "<details><summary>🟥 НЕТ В НАЛИЧИИ · %d</summary>\n\n", len(r.OutOfStock))
		for i, p := range r.OutOfStock {
			fmt.Fprintf(&b, "%d. %s\n", i+1, escape(p.Name))
		}
		b.WriteString("\n</details>\n\n")
	}

	if len(r.LowStock) > 0 {
		fmt.Fprintf(&b, "<details><summary>🟨 КОНЧАЕТСЯ · %d</summary>\n\n", len(r.LowStock))
		b.WriteString("<table bordered compact>\n")
		b.WriteString("<tr><th>Товар</th><th>Остаток</th><th>Продажа/нед</th></tr>\n")
		for _, p := range r.LowStock {
			fmt.Fprintf(&b, "<tr><td>%s</td><td>%d</td><td>%s</td></tr>\n",
				escape(p.Name), p.Qty, formatAvg(*p.AvgWeek))
		}
		b.WriteString("</table>\n\n</details>\n\n")
	}

	if groups := groupSurplus(r.Surplus); len(groups) > 0 {
		fmt.Fprintf(&b, "<details><summary>🟦 ИЗБЫТОК · %d</summary>\n\n", len(groups))
		b.WriteString("<table bordered compact>\n")
		b.WriteString("<tr><th>Товар</th><th>Срок</th><th>Накоплено / продадим</th><th>Скидка</th></tr>\n")
		for _, g := range groups {
			for i, s := range g.Lots {
				name := ""
				switch {
				case i > 0:
					// Ячейка имени занята первой строкой (rowspan).
				case len(g.Lots) > 1:
					// Имя склеено по строкам лотов: три срока одного товара
					// не должны читаться как три товара.
					name = fmt.Sprintf(`<td rowspan="%d">%s</td>`, len(g.Lots), escape(g.Name))
				default:
					name = fmt.Sprintf("<td>%s</td>", escape(g.Name))
				}
				fmt.Fprintf(&b, "<tr>%s<td>%s</td><td>%d / ~%d</td><td>%s</td></tr>\n",
					name, s.BestBefore.Format(dateOnly), s.CumQty, s.PlannedQty, discountCell(s))
			}
		}
		b.WriteString("</table>\n\n</details>\n\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// discountCell — скидка пары для таблицы избытка: что должно стоять на сайте.
// Берём большее из действующей скидки сайта и ступени по сроку: избыток
// поднимает постоянную, но глубже лестницы не бывает. Нет скидки — прочерк.
func discountCell(s discounts.SurplusLot) string {
	best := s.Site
	if s.Expiry != nil && (best == nil || *s.Expiry > *best) {
		best = s.Expiry
	}
	if best == nil || *best <= 0 {
		return "—"
	}

	return fmt.Sprintf("<b>%d %%</b>", *best)
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

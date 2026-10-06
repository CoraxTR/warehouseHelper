package usecase

import (
	"strings"

	"warehouseHelper/internal/msclient/client"
)

// Разбор раскрытия события аудита (GET audit/<id>/events). Чистые функции:
// каталог и БД не знают. Фильтры «quantity == reserved» и «internal_code
// задан» применяет вызывающий код (нужен тип товара из каталога).

// parseOutcome — найденные в событии паттерны.
type parseOutcome struct {
	cancelled bool      // заказ переведён в статус «Отменён» (MSAPI_CANCELLED_STATE_ID)
	removals  []removal // позиции, ушедшие из заказа: строка удалена целиком либо урезана
}

// removal — позиция, ушедшая из заказа: строка УДАЛЕНА целиком (Released == 0)
// либо УРЕЗАНА — количество уменьшили, а строка в заказе осталась (Released > 0,
// частичное расформирование). Quantity/Reserve — снимок строки ДО ухода части;
// к возврату у урезанной идёт Released (у удалённой — Quantity при
// Quantity == Reserve).
type removal struct {
	ProductID string  // uuid товара (последний сегмент assortment.meta.href)
	Name      string  // название товара из диффа
	Quantity  float64 // количество строки на момент урезания (кг для весовых, шт для штучных)
	Reserve   float64 // резерв строки на момент урезания
	Released  float64 // урезание: сколько единиц стало не нужно (0 — строку удалили целиком)
	Uom       string  // "кг" / "шт" (может отсутствовать в диффе)
}

// lastPathSegment — последний сегмент href МС (id сущности). href'ы целиком
// не храним и не сравниваем: id — последний сегмент (конвенция msclient).
func lastPathSegment(href string) string {
	if i := strings.LastIndex(href, "/"); i >= 0 {
		return href[i+1:]
	}
	return href
}

// parseDetail ищет в строках события паттерны «позиция ушла из заказа»
// (diff.positions[]: удаление — есть oldValue, нет newValue; урезание — оба
// значения) и «перевод в Отменён» (diff.state.newValue.meta.href → id статуса).
// Отмена и уход позиций в одном событии не исключают друг друга — приоритет
// решает вызывающий код.
func parseDetail(rows []client.AuditEventRow, cancelledStateID string) parseOutcome {
	var out parseOutcome

	for _, row := range rows {
		if diff := row.Diff; diff.State != nil && diff.State.NewValue != nil && cancelledStateID != "" {
			if lastPathSegment(diff.State.NewValue.Meta.HREF) == cancelledStateID {
				out.cancelled = true
			}
		}

		for _, pos := range row.Diff.Positions {
			switch {
			// Удаление: снимок строки остался в oldValue, newValue отсутствует.
			case pos.OldValue != nil && pos.NewValue == nil:
				out.removals = append(out.removals, removalOf(*pos.OldValue, 0))

			// Изменение строки: добавление (только newValue) — не наше;
			// уменьшение количества — частичное расформирование (см. partialRemoval).
			case pos.OldValue != nil && pos.NewValue != nil:
				if r, ok := partialRemoval(*pos.OldValue, *pos.NewValue); ok {
					out.removals = append(out.removals, r)
				}

			default:
				// Добавление строки (только newValue) — не наше событие.
			}
		}
	}

	return out
}

// removalOf — снимок ушедшей строки; released > 0 только у урезанных.
func removalOf(p client.AuditPosition, released float64) removal {
	r := removal{
		ProductID: lastPathSegment(p.Assortment.Meta.HREF),
		Name:      p.Assortment.Name,
		Quantity:  p.Quantity,
		Reserve:   p.Reserve,
		Released:  released,
	}
	if p.Uom != nil {
		r.Uom = *p.Uom
	}

	return r
}

// partialRemoval — частичное расформирование: строку НЕ удалили, но количество
// уменьшили (2 шт → 1 шт), и МС САМ понизил резерв вслед за количеством.
// Фильтр «quantity == reserved» снова сходится, поэтому такая строка не видна
// ни удалением, ни проверкой резерва — единственный источник её появления в
// диффе аудита (живая проба 06.10.2026, заказ 07231: Карпаччо креветки 2 шт
// r=2 → 1 шт r=1, в том же событии удалены ещё две отложенные позиции).
//
// К возврату идёт снятая часть резерва — ровно она стала не нужна заказу.
// Не наше: замена товара в строке (разная номенклатура — МС пишет это правкой
// позиции, а не удалением) и рост/переподбор количества (Reserve при этом не
// снимается, а весовой перевес ловит подбор).
func partialRemoval(before, after client.AuditPosition) (removal, bool) {
	if lastPathSegment(before.Assortment.Meta.HREF) != lastPathSegment(after.Assortment.Meta.HREF) {
		return removal{}, false
	}
	if after.Quantity >= before.Quantity {
		return removal{}, false
	}

	released := before.Reserve - after.Reserve
	if released <= 0 {
		return removal{}, false // резерв не снят — физически отложенное не изменилось
	}

	return removalOf(before, released), true
}

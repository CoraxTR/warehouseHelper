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
	removals  []removal // позиции, удалённые из заказа (oldValue без newValue)
}

// removal — позиция, удалённая из заказа: снимок строки до удаления.
type removal struct {
	ProductID string  // uuid товара (последний сегмент assortment.meta.href)
	Name      string  // название товара из диффа
	Quantity  float64 // количество на момент удаления (кг для весовых, шт для штучных)
	Reserve   float64 // резерв на момент удаления
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

// parseDetail ищет в строках события паттерны «удаление позиций»
// (diff.positions[]: есть oldValue, нет newValue) и «перевод в Отменён»
// (diff.state.newValue.meta.href → id статуса). Отмена и удаления в одном
// событии не исключают друг друга — приоритет решает вызывающий код.
func parseDetail(rows []client.AuditEventRow, cancelledStateID string) parseOutcome {
	var out parseOutcome

	for _, row := range rows {
		if diff := row.Diff; diff.State != nil && diff.State.NewValue != nil && cancelledStateID != "" {
			if lastPathSegment(diff.State.NewValue.Meta.HREF) == cancelledStateID {
				out.cancelled = true
			}
		}

		for _, pos := range row.Diff.Positions {
			// Удаление: снимок строки остался в oldValue, newValue отсутствует.
			// Изменение (оба значения) или добавление (только newValue) — не наше.
			if pos.OldValue == nil || pos.NewValue != nil {
				continue
			}

			r := removal{
				ProductID: lastPathSegment(pos.OldValue.Assortment.Meta.HREF),
				Name:      pos.OldValue.Assortment.Name,
				Quantity:  pos.OldValue.Quantity,
				Reserve:   pos.OldValue.Reserve,
			}
			if pos.OldValue.Uom != nil {
				r.Uom = *pos.OldValue.Uom
			}

			out.removals = append(out.removals, r)
		}
	}

	return out
}

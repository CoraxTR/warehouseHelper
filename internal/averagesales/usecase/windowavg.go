package usecase

import (
	"errors"

	"warehouseHelper/internal/averagesales"
)

// ErrNoData — у товара нет ни одного интервала продаж (продаж не было вообще).
// AverageSales возвращает его вместо среднего; потребители обязаны уметь
// пропускать такие товары (пометка в AGENTS.md модуля).
var ErrNoData = errors.New("нет данных о продажах")

// windowAvg — среднее по правилу владельца.
//
// finished — завершённые интервалы в хронологическом порядке ПО УБЫВАНИЮ
// (последний элемент — самая дальняя/самая старая), len <= n;
// current — текущий незакрытый период (может быть nil). k = len(finished).
//
// (nil, ErrNoData) — ТОЛЬКО когда k == 0 И current == nil: продаж не было вообще
// (новый товар без единых продаж; потребители обязаны пропускать такие товары).
//
// Иначе считаем по имеющемуся (даже один текущий незакрытый → avg = current/1):
//
//	includeCurrent = current != nil && (k < n || current.Qty > finished[k-1].Qty)
//	окно = includeCurrent ? (последние n-1 завершённых, или все k при k < n-1) + current
//	                     : (n завершённых, или все k при k < n)
//	avg = sum(окно) / len(окно)   // неполное окно: делитель — фактическое число
//
// «Самая дальняя» — по календарю (та, что выпадает из окна при добавлении новой
// строки, естественный rolling), НЕ слабейшая по продажам. Сравнение частичного
// периода с полным самоограничивающее: замена срабатывает только на реально
// горячем темпе. Свойства из дизайна (sales-turnover-design.md).
func windowAvg(finished []averagesales.TurnoverRow, current *averagesales.TurnoverRow, n int) (*float64, error) {
	k := len(finished)
	if k == 0 && current == nil {
		return nil, ErrNoData
	}

	includeCurrent := current != nil && (k < n || current.Qty > finished[k-1].Qty)

	window := make([]averagesales.TurnoverRow, 0, min(k, n)+1)
	if includeCurrent {
		window = append(window, finished[:min(k, n-1)]...)
		window = append(window, *current)
	} else {
		window = append(window, finished[:min(k, n)]...)
	}

	sum := 0.0
	for _, r := range window {
		sum += r.Qty
	}

	avg := sum / float64(len(window))
	return &avg, nil
}

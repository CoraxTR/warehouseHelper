package usecase

import (
	"time"

	"warehouseHelper/internal/boxlabel"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
)

// ExportBoxLabels формирует xlsx-файл наклеек принятых коробок в tempdir и
// возвращает путь к нему. Геометрия печати и 33-значный код — в пакете
// boxlabel; приёмка только маппит коробки отчёта в данные наклейки.
func (uc *ReceivingUseCase) ExportBoxLabels(boxes []receiving.Box) (string, error) {
	done := metrics.Track(trackPkg, "ExportBoxLabels")
	defer done()

	return boxlabel.Export(toLabelBoxes(boxes))
}

// toLabelBoxes маппит коробки отчёта приёмки в данные наклейки: код, товар,
// весовость, вес, число вложений и обе даты. Коробку без срока годности
// пропускаем — 33-значный код без даты не собрать (у коробки со правилом, не
// вычитывающим срок, наклейки не будет).
func toLabelBoxes(boxes []receiving.Box) []boxlabel.Box {
	out := make([]boxlabel.Box, 0, len(boxes))
	for _, b := range boxes {
		if b.BestBefore == nil {
			continue
		}
		var produced time.Time
		if b.ProducedOn != nil {
			produced = *b.ProducedOn
		}
		out = append(out, boxlabel.Box{
			InternalCode: b.InternalCode,
			ProductName:  b.ProductName,
			Weighted:     b.Weighted,
			WeightG:      b.WeightG,
			Qty:          int(b.Qty),
			ProducedOn:   produced,
			BestBefore:   *b.BestBefore,
		})
	}
	return out
}

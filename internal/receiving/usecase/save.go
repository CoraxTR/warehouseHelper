package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"warehouseHelper/internal/avgweight"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/stock"
)

// (trackPkg объявлена в первом файле пакета)

// Save принимает приёмку: резолв всех сканов (куски и коробки с
// подсписками), добавление к остаткам через модуль сроков, запись весов,
// отчёт. Коробка принимается «как есть» при расхождении (Mismatch) —
// подсвечивается, но не блокирует.
func (uc *ReceivingUseCase) Save(ctx context.Context, req receiving.SaveRequest) (*receiving.SaveResult, error) {
	defer metrics.Track(trackPkg, "Save")()
	cache, err := uc.GetCache(ctx, req.SupplierID)
	if err != nil {
		return nil, err
	}

	units := make([]receiving.Unit, 0, len(req.Scans))
	boxes := make([]receiving.Box, 0)
	for _, e := range req.Scans {
		scan, err := uc.Resolve(ctx, cache, e)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", e.Raw, err)
		}
		if scan.Kind == receiving.KindBox {
			box, boxUnits, err := uc.resolveBox(ctx, cache, scan, e)
			if err != nil {
				return nil, fmt.Errorf("коробка %q: %w", e.Raw, err)
			}
			boxes = append(boxes, *box)
			units = append(units, boxUnits...)
			continue
		}
		if err := validateUnitScan(scan); err != nil {
			return nil, fmt.Errorf("%q: %w", e.Raw, err)
		}
		units = append(units, unitOf(scan, false, false))
	}

	if len(units) == 0 {
		return nil, errors.New("нет принятых единиц")
	}

	// Остатки: лоты (товар, срок) с суммой количества — через модуль сроков.
	if err := uc.stock.AcceptStock(ctx, buildLots(units)); err != nil {
		return nil, fmt.Errorf("принять в остатки: %w", err)
	}

	// Веса принятых единиц: только весовые товары. Запись поштучно,
	// FIFO-лимит, пересчёт среднего и синк каталога/вики — модуль среднего
	// веса; предупреждения его синков уходят в отчёт приёмки.
	var rows []avgweight.WeightRow
	for _, u := range units {
		if u.WeightG > 0 {
			rows = append(rows, avgweight.WeightRow{ProductID: u.ProductID, WeightG: u.WeightG})
		}
	}
	result := &receiving.SaveResult{Rows: buildReport(units), Units: units, Boxes: boxes}
	if len(rows) > 0 && uc.weights != nil {
		warnings, err := uc.weights.RecordWeights(ctx, rows)
		if err != nil {
			return nil, fmt.Errorf("записать веса: %w", err)
		}
		result.Warnings = translateWeightWarnings(warnings, units)
	}

	return result, nil
}

// translateWeightWarnings заменяет в предупреждениях модуля среднего веса
// id товара на его имя (в отчёте приёмки имя читается, id — нет).
func translateWeightWarnings(warnings []string, units []receiving.Unit) []string {
	if len(warnings) == 0 {
		return nil
	}

	names := make(map[string]string, len(units))
	for _, u := range units {
		if _, ok := names[u.ProductID]; !ok {
			names[u.ProductID] = u.ProductName
		}
	}

	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		for id, name := range names {
			if name != "" && strings.Contains(w, id) {
				w = strings.Replace(w, id, name, 1)
				break
			}
		}
		out = append(out, w)
	}

	return out
}

// resolveBox резолвит коробку и её подсписок: дети должны быть кусками
// ОДНОГО товара; факт (кол-во, Σ вес) сверяется с заявленным из кода.
func (uc *ReceivingUseCase) resolveBox(ctx context.Context, cache *receiving.Cache, box *receiving.DecodedScan, e receiving.ScanEntry) (*receiving.Box, []receiving.Unit, error) {
	if len(e.Children) == 0 {
		return nil, nil, errors.New("в коробке нет отсканированных товаров")
	}

	units := make([]receiving.Unit, 0, len(e.Children))
	var (
		firstCode string
	)
	for _, child := range e.Children {
		c, err := uc.Resolve(ctx, cache, child)
		if err != nil {
			return nil, nil, err
		}
		if c.Kind == receiving.KindBox {
			return nil, nil, errors.New("в коробке не может быть другая коробка")
		}
		if err := validateUnitScan(c); err != nil {
			return nil, nil, err
		}
		if firstCode == "" {
			firstCode = c.InternalCode
		} else if c.InternalCode != firstCode {
			return nil, nil, fmt.Errorf("в коробке товары разных позиций (%s и %s)", firstCode, c.InternalCode)
		}
		units = append(units, unitOf(c, true, false))
	}

	var totalWeight int64
	for _, u := range units {
		totalWeight += u.WeightG
	}
	actualQty := int64(len(units))

	box.ActualQty = actualQty
	box.ActualWeightG = totalWeight
	if box.DeclaredQty != nil && *box.DeclaredQty != actualQty {
		box.Mismatch = true
	}
	if box.DeclaredWeightG != nil && *box.DeclaredWeightG != totalWeight {
		box.Mismatch = true
	}
	for i := range units {
		units[i].BoxMismatch = box.Mismatch
	}

	return &receiving.Box{
		ProductID:       box.ProductID,
		InternalCode:    box.InternalCode,
		ProductName:     box.ProductName,
		WeightG:         totalWeight,
		Qty:             actualQty,
		ProducedOn:      box.ProducedOn,
		BestBefore:      box.BestBefore,
		DeclaredQty:     box.DeclaredQty,
		DeclaredWeightG: box.DeclaredWeightG,
		Mismatch:        box.Mismatch,
	}, units, nil
}

// validateUnitScan проверяет обязательные поля куска: товар, срок годности
// (правило/ручной ввод), для весового — вес.
func validateUnitScan(s *receiving.DecodedScan) error {
	if s.ProductID == "" || s.InternalCode == "" {
		return errors.New("не определён товар")
	}
	if s.BestBefore == nil {
		return errors.New("не указан срок годности — задайте вручную")
	}
	if s.WeightG == nil {
		return errors.New("не указан вес — задайте вручную")
	}
	if *s.WeightG <= 0 {
		return errors.New("вес должен быть больше нуля")
	}
	return nil
}

// unitOf превращает скан куска в единицу приёмки.
func unitOf(s *receiving.DecodedScan, inBox, boxMismatch bool) receiving.Unit {
	return receiving.Unit{
		ProductID:    s.ProductID,
		InternalCode: s.InternalCode,
		ProductName:  s.ProductName,
		WeightG:      deref(s.WeightG),
		ProducedOn:   s.ProducedOn,
		BestBefore:   *s.BestBefore,
		InBox:        inBox,
		BoxMismatch:  boxMismatch,
	}
}

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// buildLots группирует единицы по (товар, срок) и собирает партии для
// AcceptStock. Производитель берётся из первой единицы партии.
func buildLots(units []receiving.Unit) []stock.LotIn {
	type key struct {
		productID  string
		bestBefore time.Time
	}
	agg := make(map[key]*stock.LotIn)
	order := make([]key, 0, len(units))
	for _, u := range units {
		k := key{u.ProductID, u.BestBefore}
		if _, ok := agg[k]; !ok {
			agg[k] = &stock.LotIn{ProductID: u.ProductID, BestBefore: u.BestBefore, ProducedOn: u.ProducedOn}
			order = append(order, k)
		}
		agg[k].Qty++
	}

	lots := make([]stock.LotIn, 0, len(agg))
	for _, k := range order {
		lots = append(lots, *agg[k])
	}
	return lots
}

// buildReport группирует единицы в строки отчёта:
// весовые — {name} - {срок} - {принято, кг}; штучные — {name} - {срок} - {штук}.
func buildReport(units []receiving.Unit) []receiving.ReportRow {
	type key struct {
		name string
		date string
	}
	agg := make(map[key]*receiving.ReportRow)
	order := make([]key, 0, len(units))
	for _, u := range units {
		k := key{u.ProductName, u.BestBefore.Format(time.DateOnly)}
		row, ok := agg[k]
		if !ok {
			row = &receiving.ReportRow{ProductName: u.ProductName, BestBefore: k.date}
			agg[k] = row
			order = append(order, k)
		}
		row.Qty++
		row.QtyKg += float64(u.WeightG) / 1000.0
	}

	// Штучные товары: вес = 1 единица → qty в штуках, кг не показываем.
	// (Отличие штучных от весовых — WeightG 0 в единицах штучных товаров,
	// приёмка штучных идёт без весов.)
	rows := make([]receiving.ReportRow, 0, len(agg))
	for _, k := range order {
		rows = append(rows, *agg[k])
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ProductName != rows[j].ProductName {
			return strings.ToLower(rows[i].ProductName) < strings.ToLower(rows[j].ProductName)
		}
		return rows[i].BestBefore < rows[j].BestBefore
	})
	return rows
}

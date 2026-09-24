package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"warehouseHelper/internal/msorders"
	"warehouseHelper/internal/scanmatch"
)

// journalWarn — предупреждение оператору: заказ в МС обновлён, но журнал сроков
// не записался. Молчать нельзя (даты по позициям станут недостоверными, а
// страница отчитается успехом) — поэтому текст уходит в ответ страницы.
const journalWarn = "заказ обновлён в МС, но журнал сроков не записан — сроки по позициям нужно пересчитать вручную"

// cleanupInterval — период фоновой чистки журнала подбора (ретеншен).
const cleanupInterval = 24 * time.Hour

// journalUnitsFromRef — единицы журнала по покрытой строке отправки: каждая
// запись скана — одна единица товара (весовые — кусок с весом из этикетки,
// штучные — штука, вес 1). Все единицы строки пишутся на «живую» позицию
// группы: у смёрженных хвостов своей позиции в заказе уже не остаётся.
func journalUnitsFromRef(orderID string, ref *coveredRef) []msorders.PickingUnit {
	units := make([]msorders.PickingUnit, 0, len(ref.records))
	for i := range ref.records {
		w := 1.0
		if ref.weighted {
			w = float64(ref.records[i].weightG) / 1000
		}
		units = append(units, msorders.PickingUnit{
			OrderID:      orderID,
			PositionID:   ref.live.id,
			InternalCode: ref.live.code,
			ProductID:    ref.productID,
			ProductName:  ref.live.name,
			Weighted:     ref.weighted,
			WeightKg:     w,
			ProducedOn:   ref.records[i].pd,
			BestBefore:   ref.records[i].bb,
		})
	}

	return units
}

// writeSubmitJournal пишет журнал после успешного обновления заказа в МС:
// покрытые строки с from == 0 (подбор, переподбор, добор новой позиции)
// заменяют свои строки журнала по позиции, с from > 0 (добор уже подобранной
// части строки, склейка группы с добором) — дозаписываются.
//
// Возвращает текст предупреждения для страницы: ошибка журнала подбор не
// отменяет (заказ в МС уже обновлён в отличие от списания остатков) и потому
// не роняет запрос, но и не молчит — оператор видит предупреждение.
func (uc *UseCase) writeSubmitJournal(ctx context.Context, orderID string, req SubmitRequest, records map[int][]parsedScan, entry *submitEntry) string {
	if uc.journal == nil {
		return ""
	}

	_, metaByID, err := orderRowMetas(entry)
	if err != nil {
		slog.Error("msorders: журнал сроков: строки заказа недоступны", "order", orderID, "err", err)

		return journalWarn
	}

	replace := msorders.PickingReplace{OrderID: orderID}
	var appendUnits []msorders.PickingUnit
	for i := range req.Rows {
		row := &req.Rows[i]
		live, ok := metaByID[strings.TrimSpace(row.IDs[0])]
		if !ok {
			continue // строку не нашли — о ней уже сказал сборщик positions
		}
		ref := &coveredRef{
			live:      live,
			records:   records[i],
			from:      row.From,
			productID: live.productID,
			weighted:  live.weighted,
		}
		units := journalUnitsFromRef(orderID, ref)
		if row.From > 0 {
			appendUnits = append(appendUnits, units...)
			continue
		}
		replace.PositionIDs = append(replace.PositionIDs, live.id)
		replace.Units = append(replace.Units, units...)
	}

	if err := uc.applyJournalWrites(ctx, replace, appendUnits); err != nil {
		slog.Error("msorders: журнал сроков не записан", "order", orderID, "err", err)

		return journalWarn
	}

	return ""
}

// applyJournalWrites выполняет обе части записи журнала: замену по позициям и
// дозапись единиц (пустые части — без запроса к БД).
func (uc *UseCase) applyJournalWrites(ctx context.Context, replace msorders.PickingReplace, appendUnits []msorders.PickingUnit) error {
	if len(replace.PositionIDs) > 0 {
		if err := uc.journal.ReplaceOrderPicking(ctx, replace); err != nil {
			return fmt.Errorf("replace order picking: %w", err)
		}
	}
	if len(appendUnits) > 0 {
		if err := uc.journal.AppendOrderPicking(ctx, appendUnits); err != nil {
			return fmt.Errorf("append order picking: %w", err)
		}
	}

	return nil
}

// clearJournalPositions убирает строки журнала по позициям заказа: ручное
// закрытие возврата в сроки и ручной подбор без сканов — даты этих позиций
// неизвестны, в журнале остаются только фактически отсканированные единицы.
// Пустой список позиций — no-op (чистить нечего).
func (uc *UseCase) clearJournalPositions(ctx context.Context, orderID string, positionIDs []string) string {
	if uc.journal == nil || len(positionIDs) == 0 {
		return ""
	}
	if err := uc.journal.ClearOrderPicking(ctx, orderID, positionIDs); err != nil {
		slog.Error("msorders: журнал сроков не очищен по позициям", "order", orderID, "err", err)

		return journalWarn
	}

	return ""
}

// removeJournalUnits убирает из журнала единицы, вернувшиеся в «Сроки» при
// возврате в сроки: каждая единица гасит одну строку журнала по коду склада,
// сроку и весу (весовой кусок возвращается тот же, штучных может вернуться
// несколько — совпадения одной пары копятся в Count).
func (uc *UseCase) removeJournalUnits(ctx context.Context, orderID string, units []scanmatch.ScannedUnit) string {
	if uc.journal == nil || len(units) == 0 {
		return ""
	}

	type key struct {
		code       string
		bestBefore time.Time
		weightKg   float64
	}
	counts := make(map[key]int, len(units))
	order := make([]key, 0, len(units))
	for i := range units {
		w := 1.0
		if units[i].Row.Weighted {
			w = float64(units[i].WeightG) / 1000
		}
		k := key{code: units[i].Row.InternalCode, bestBefore: units[i].ExpDate, weightKg: w}
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k]++
	}

	for _, k := range order {
		err := uc.journal.RemoveOrderPickingUnits(ctx, msorders.PickingReturn{
			OrderID:      orderID,
			InternalCode: k.code,
			BestBefore:   k.bestBefore,
			WeightKg:     k.weightKg,
			Count:        counts[k],
		})
		if err != nil {
			slog.Error("msorders: журнал сроков не очищен по вернувшимся единицам",
				"order", orderID, "code", k.code, "err", err)

			return journalWarn
		}
	}

	return ""
}

// manualRowPositions — «живые» позиции строк ручного подтверждения: по ним
// чистится журнал (у ручного веса дат выработки и срока годности нет).
func manualRowPositions(rows []ManualRow) []string {
	ids := make([]string, 0, len(rows))
	for i := range rows {
		if len(rows[i].IDs) == 0 {
			continue // строку без ids уже отверг сборщик positions
		}
		if id := strings.TrimSpace(rows[i].IDs[0]); id != "" {
			ids = append(ids, id)
		}
	}

	return ids
}

// returnRowPositions — «живые» позиции строк возврата в сроки: по ним чистится
// журнал при ручном закрытии (куски в остатки не вернулись — дат нет).
func returnRowPositions(rows []PickReturnRow) []string {
	ids := make([]string, 0, len(rows))
	for i := range rows {
		if len(rows[i].IDs) == 0 {
			continue
		}
		if id := strings.TrimSpace(rows[i].IDs[0]); id != "" {
			ids = append(ids, id)
		}
	}

	return ids
}

// ClearShelfLife — очистка журнала сроков по расформированному заказу (шов
// модуля «Возврат в продажу»): productIDs пусто — весь заказ (заказ отменён
// целиком), иначе — только позиции этих товаров (менеджер убрал их из заказа).
// Идентификаторы позиций в событии аудита не приходят, поэтому чистим по uuid
// товаров; журнал не подключён — чистить нечего, расформирование не страдает.
func (uc *UseCase) ClearShelfLife(ctx context.Context, orderID string, productIDs []string) error {
	if uc.journal == nil {
		return nil
	}
	if strings.TrimSpace(orderID) == "" {
		return ErrEmptyOrderID
	}
	if len(productIDs) == 0 {
		return uc.journal.ClearOrderPicking(ctx, orderID, nil)
	}

	return uc.journal.ClearOrderPickingProducts(ctx, orderID, productIDs)
}

// RunShelfLifeCleanup чистит журнал подбора старше ретеншена — фоновой задачей:
// сразу при старте и раз в сутки. Номер заказа в МС начинается заново каждый
// год, поэтому старые строки обязаны уходить: иначе поиск по номеру мог бы
// встретить прошлогоднего тёзку. Ретеншен не задан — чистка не запускается.
func (uc *UseCase) RunShelfLifeCleanup(ctx context.Context) {
	if uc.journal == nil || uc.retention <= 0 {
		slog.Info("msorders: чистка журнала подбора не запущена", "retention", uc.retention.String())

		return
	}

	uc.cleanupShelfLife(ctx)

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			uc.cleanupShelfLife(ctx)
		}
	}
}

// cleanupShelfLife — один проход чистки: строки, записанные раньше ретеншена.
func (uc *UseCase) cleanupShelfLife(ctx context.Context) {
	days := int(uc.retention.Hours() / 24)
	if days <= 0 {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)

	removed, err := uc.journal.CleanupOrderPicking(ctx, cutoff)
	if err != nil {
		slog.Error("msorders: чистка журнала подбора не прошла", "err", err)

		return
	}
	if removed > 0 {
		slog.Info("msorders: журнал подбора почищен", "rows", removed, "older_than", cutoff.Format("2006-01-02"))
	}
}

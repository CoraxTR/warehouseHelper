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
// целиком); иначе — только строки позиций, которых в заказе УЖЕ НЕТ.
//
// Одинаковые товары живут в заказе отдельными позициями (у каждой свой вес и
// свои сроки), поэтому чистить по uuid товара нельзя: удаление одной позиции
// сносило даты остальных живых позиций того же товара (прод-баг 04.10.2026,
// заказ 07189 — из ответа /sroki пропали все три строки Вырезки, две из них
// оставались в заказе). Границу «живая/мёртвая» даёт сам заказ: id позиций в
// событии аудита не приходят, но их отдаёт МС, а журнал хранит position_id.
// Журнал не подключён — чистить нечего, расформирование не страдает.
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

	dead, err := uc.deadJournalPositions(ctx, orderID, productIDs)
	if err != nil {
		// Состав заказа не получен — строки НЕ трогаем: потерять даты живых
		// позиций хуже, чем оставить лишние (их видно в /sroki и на странице).
		slog.Error("msorders: журнал сроков не очищен — позиции заказа не получены",
			"order", orderID, "err", err)

		return err
	}
	if len(dead) == 0 {
		return nil
	}

	if err := uc.journal.ClearOrderPicking(ctx, orderID, dead); err != nil {
		return err
	}
	slog.Info("msorders: журнал сроков очищен по удалённым позициям заказа",
		"order", orderID, "positions", len(dead))

	return nil
}

// deadJournalPositions — позиции журнала, которых в заказе уже нет (только по
// товарам события: чужие товары не трогаем). Строка журнала мёртвая, если её
// позиции нет среди живых ИЛИ та же позиция теперь держит другой товар
// (менеджер заменил товар в строке — МС пишет это правкой позиции, а не
// удалением). Пустой список — удалять нечего.
func (uc *UseCase) deadJournalPositions(ctx context.Context, orderID string, productIDs []string) ([]string, error) {
	units, err := uc.journal.OrderPickingByOrder(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("журнал заказа %s: %w", orderID, err)
	}
	if len(units) == 0 {
		return nil, nil
	}

	live, err := uc.livePositions(ctx, orderID)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]struct{}, len(productIDs))
	for _, id := range productIDs {
		wanted[id] = struct{}{}
	}

	dead := make([]string, 0, len(units))
	seen := make(map[string]struct{}, len(units))
	for i := range units {
		u := &units[i]
		if _, ok := wanted[u.ProductID]; !ok {
			continue // товар не из события — его позиции не наши
		}
		if u.PositionID == "" {
			continue // строка без позиции (старые записи) — удалять не по чему
		}
		if product, alive := live[u.PositionID]; alive && product == u.ProductID {
			continue // позиция жива и держит тот же товар — строки не наши
		}
		if _, dup := seen[u.PositionID]; dup {
			continue
		}
		seen[u.PositionID] = struct{}{}
		dead = append(dead, u.PositionID)
	}

	return dead, nil
}

// livePositions — живые позиции заказа: id позиции → uuid товара (последний
// сегмент assortment.meta.href). Нужны, чтобы отличить строки журнала удалённых
// позиций от строк позиций, оставшихся в заказе.
func (uc *UseCase) livePositions(ctx context.Context, orderID string) (map[string]string, error) {
	order, _, err := uc.ms.FetchOrderByID(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("заказ %s: %w", orderID, err)
	}

	positions, _, err := uc.ms.FetchOrderPositionsByHREF(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("позиции заказа %s: %w", orderID, err)
	}

	out := make(map[string]string, len(positions))
	for i := range positions {
		id := strings.TrimSpace(positions[i].ID)
		if id == "" {
			continue
		}
		href := positions[i].Assortment.Meta.HREF
		product := href
		if i := strings.LastIndex(href, "/"); i >= 0 {
			product = href[i+1:]
		}
		out[id] = product
	}

	return out, nil
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
		slog.Info("msorders: журнал подбора почищен", "rows", removed, "older_than", cutoff.Format(time.DateOnly))
	}
}

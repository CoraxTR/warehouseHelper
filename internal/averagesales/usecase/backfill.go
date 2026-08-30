package usecase

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
)

// Границы первичного бэкфилла (правило владельца).
const (
	minBackfillYear = 2014 // магазин открылся в 2014 — дальше не ходим
	monthlyNeed     = 12   // месячных not null интервалов добираем минимум
	weeklyNeed      = 5    // недельных — минимум 5 (окно недельных средних)
	noGroupBatch    = 20   // размер пачки безгрупповых товаров в дозаливке
)

// backfillRunner — фоновый исполнитель первичного бэкфилла: собственная горутина
// с собственным контекстом (паттерн PDFPreloader), очередь товаров. Запросы к МС
// идут через воркерпул msclient — рейт-лимит соблюдён.
type backfillRunner struct {
	uc *UseCase

	mu      sync.Mutex
	queue   []string
	cancel  context.CancelFunc
	started bool
	missing bool // идёт ли стартовая дозаливка
}

func newBackfillRunner(uc *UseCase) *backfillRunner {
	return &backfillRunner{uc: uc}
}

// enqueue добавляет товары в очередь и гарантирует запуск воркера.
func (b *backfillRunner) enqueue(ids []string) {
	if len(ids) == 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.queue = append(b.queue, ids...)
	b.ensureWorkerLocked()
}

// ensureWorkerLocked запускает воркер очереди, если он ещё не бежит.
func (b *backfillRunner) ensureWorkerLocked() {
	if b.started {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.started = true

	go b.worker(ctx)
}

// worker последовательно обрабатывает очередь товаров.
func (b *backfillRunner) worker(ctx context.Context) {
	for {
		b.mu.Lock()
		if len(b.queue) == 0 {
			b.started = false
			b.mu.Unlock()
			return
		}
		id := b.queue[0]
		b.queue = b.queue[1:]
		b.mu.Unlock()

		if err := ctx.Err(); err != nil {
			return
		}
		if err := b.uc.backfillProduct(ctx, id); err != nil {
			log.Printf("averagesales: бэкфилл товара %s: %v", id, err)
		}
	}
}

// runMissing запускает стартовую дозаливку товаров без месячной истории.
func (b *backfillRunner) runMissing() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.missing {
		return
	}
	b.missing = true

	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel

	go func() {
		defer func() {
			b.mu.Lock()
			b.missing = false
			b.cancel = nil
			b.mu.Unlock()
		}()

		if err := b.uc.backfillMissing(ctx); err != nil {
			log.Printf("averagesales: стартовая дозаливка: %v", err)
		}
	}()
}

// stop отменяет фоновые задачи (при завершении приложения).
func (b *backfillRunner) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
	}
}

// backfillProduct — первичное заполнение истории одного товара: месячной
// (всем) и недельной (track_weekly = true). Идём от прошлого года вглубь,
// пока не наберём need not null интервалов или не упрёмся в 2014.
// Уже заполненные периоды пропускаем (идемпотентность), товара в каталоге
// нет — пропуск без ошибки.
func (uc *UseCase) backfillProduct(ctx context.Context, id string) error {
	prod, err := uc.products.TurnoverProduct(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrProductNotFound) {
			return nil
		}
		return err
	}

	hasMonthly, err := uc.repo.HasMonthlyTurnover(ctx, id)
	if err != nil {
		return err
	}
	if !hasMonthly {
		if err := uc.fillMonthlyHistory(ctx, *prod, client.ProfitFilter{ProductIDs: []string{id}}); err != nil {
			return err
		}
	}
	if prod.TrackWeekly {
		hasWeekly, err := uc.repo.HasWeeklyTurnover(ctx, id)
		if err != nil {
			return err
		}
		if !hasWeekly {
			if err := uc.fillWeeklyHistory(ctx, *prod, client.ProfitFilter{ProductIDs: []string{id}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// fillMonthlyHistory — помесячная история «до начала текущего года»: годы от
// прошлого вглубь до 2014, пока не набрано monthlyNeed not null интервалов.
// Фильтр отчёта — как передан (точечный или пачечный), из строк берутся только
// товары из want (nil — все строки фильтра).
func (uc *UseCase) fillMonthlyHistory(ctx context.Context, p averagesales.TurnoverProduct, filter client.ProfitFilter) error {
	return uc.fillHistory(ctx, monthlyNeed, "month", p, filter, nil)
}

// fillWeeklyHistory — понедельная история до первой полной недели текущего года,
// вглубь до 2014, пока не набрано weeklyNeed not null интервалов.
func (uc *UseCase) fillWeeklyHistory(ctx context.Context, p averagesales.TurnoverProduct, filter client.ProfitFilter) error {
	return uc.fillHistory(ctx, weeklyNeed, "week", p, filter, nil)
}

// fillHistory — общий цикл бэкфилла: интервалы от границы вглубь, стоп по
// счётчику not null или пределу 2014. want — фильтр строк ответа по товарам
// (nil — принимаем все строки; для пачки/группы — множество нужных id).
func (uc *UseCase) fillHistory(ctx context.Context, need int, interval string, p averagesales.TurnoverProduct, filter client.ProfitFilter, want map[string]struct{}) error {
	now := time.Now()
	count := 0
	needReached := func() bool { return count >= need }

	switch interval {
	case "week":
		// Недели, начинающиеся строго раньше понедельника первой полной недели
		// текущего года, вглубь до первой полной недели 2014.
		firstFull := firstFullWeekStart(now.Year(), now.Location())
		lastBefore := firstFull.AddDate(0, 0, -7)
		firstAllowed := firstFullWeekStart(minBackfillYear, now.Location())

		for week := lastBefore; !week.Before(firstAllowed) && !needReached(); week = week.AddDate(0, 0, -7) {
			rows, err := uc.fetchPeriodRows(ctx, week, interval, filter, want, &count, p)
			if err != nil {
				return err
			}
			if err := uc.upsertRows(ctx, true, rows); err != nil {
				return err
			}
		}
		return nil

	default: // "month"
		for year := now.Year() - 1; year >= minBackfillYear && !needReached(); year-- {
			var batch []averagesales.TurnoverRow
			for m := 1; m <= 12; m++ {
				from := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, now.Location())
				if !from.Before(now) {
					continue
				}
				rows, err := uc.fetchPeriodRows(ctx, from, interval, filter, want, &count, p)
				if err != nil {
					return err
				}
				batch = append(batch, rows...)
			}
			if len(batch) > 0 {
				if err := uc.upsertRows(ctx, false, batch); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// fetchPeriodRows — один запрос отчёта за период; возвращает посчитанные строки
// только для товаров из want (nil — все) и увеличивает count на каждый
// not null интервал (для точечного бэкфилла — счётчик товара).
func (uc *UseCase) fetchPeriodRows(ctx context.Context, start time.Time, interval string, filter client.ProfitFilter, want map[string]struct{}, count *int, p averagesales.TurnoverProduct) ([]averagesales.TurnoverRow, error) {
	to := periodEnd(start, interval)

	rows, err := uc.ms.FetchProfitTurnover(ctx, start, to, interval, filter)
	if err != nil {
		return nil, err
	}

	out := make([]averagesales.TurnoverRow, 0, len(rows))
	for _, r := range rows {
		id := productIDFromHref(r.Assortment.Meta.Href)
		if want != nil {
			if _, ok := want[id]; !ok {
				continue
			}
		}
		qty, ok := uc.calcQty(r, p)
		if !ok {
			continue
		}
		out = append(out, averagesales.TurnoverRow{ProductID: id, PeriodStart: start, Qty: qty})
		if count != nil {
			*count++
		}
	}
	return out, nil
}

// backfillMissing — стартовая дозаливка: товары без месячной истории.
// Товары одной группы — общим запросом productFolder (1 запрос на период на
// группу), безгрупповые — пачками по noGroupBatch. Из строк апсертятся только
// товары из очереди. Недельная история — только для track_weekly.
func (uc *UseCase) backfillMissing(ctx context.Context) error {
	ids, err := uc.repo.ProductsWithoutMonthlyTurnover(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	prods, err := uc.products.TurnoverProductsByIDs(ctx, ids)
	if err != nil {
		return err
	}

	active := make(map[string]averagesales.TurnoverProduct, len(prods))
	for _, p := range prods {
		active[p.ID] = p
	}

	groups := make(map[string][]string)
	var singles []string
	for id := range active {
		if active[id].FolderID != "" {
			groups[active[id].FolderID] = append(groups[active[id].FolderID], id)
		} else {
			singles = append(singles, id)
		}
	}

	if err := uc.fillMonthlyMissing(ctx, active, groups, singles); err != nil {
		return err
	}

	// Недельная история — только товары с track_weekly.
	weeklyActive := make(map[string]averagesales.TurnoverProduct)
	for id, p := range active {
		if p.TrackWeekly {
			weeklyActive[id] = p
		}
	}
	if len(weeklyActive) > 0 {
		weeklyGroups := make(map[string][]string)
		var weeklySingles []string
		for id, p := range weeklyActive {
			if p.FolderID != "" {
				weeklyGroups[p.FolderID] = append(weeklyGroups[p.FolderID], id)
			} else {
				weeklySingles = append(weeklySingles, id)
			}
		}
		if err := uc.fillWeeklyMissing(ctx, weeklyActive, weeklyGroups, weeklySingles); err != nil {
			return err
		}
	}

	return nil
}

// fillMonthlyMissing — месячная история для всех активных товаров дозаливки.
func (uc *UseCase) fillMonthlyMissing(ctx context.Context, active map[string]averagesales.TurnoverProduct, groups map[string][]string, singles []string) error {
	now := time.Now()
	counts := make(map[string]int, len(active))

	done := func() bool {
		for id := range active {
			if counts[id] < monthlyNeed {
				return false
			}
		}
		return true
	}

	for year := now.Year() - 1; year >= minBackfillYear && !done(); year-- {
		var batch []averagesales.TurnoverRow
		for m := 1; m <= 12; m++ {
			from := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, now.Location())
			if !from.Before(now) {
				continue
			}

			// Группы: один запрос на группу, из строк — только активные товары.
			for folderID, memberIDs := range groups {
				want := toSet(memberIDs)
				rows, err := uc.fetchGroupRows(ctx, from, "month", client.ProfitFilter{ProductFolderID: folderID}, want, active, counts)
				if err != nil {
					return err
				}
				batch = append(batch, rows...)
			}

			// Безгрупповые — пачками; активные товары, не набравшие норму.
			var chunk []string
			for _, id := range singles {
				if counts[id] < monthlyNeed {
					chunk = append(chunk, id)
				}
			}
			for i := 0; i < len(chunk); i += noGroupBatch {
				end := min(i+noGroupBatch, len(chunk))
				rows, err := uc.fetchGroupRows(ctx, from, "month", client.ProfitFilter{ProductIDs: chunk[i:end]}, nil, active, counts)
				if err != nil {
					return err
				}
				batch = append(batch, rows...)
			}
		}
		if len(batch) > 0 {
			if err := uc.repo.UpsertMonthlyTurnover(ctx, batch); err != nil {
				return err
			}
		}
	}
	return nil
}

// fillWeeklyMissing — недельная история для активных (track_weekly) товаров.
func (uc *UseCase) fillWeeklyMissing(ctx context.Context, active map[string]averagesales.TurnoverProduct, groups map[string][]string, singles []string) error {
	now := time.Now()
	counts := make(map[string]int, len(active))

	done := func() bool {
		for id := range active {
			if counts[id] < weeklyNeed {
				return false
			}
		}
		return true
	}

	firstFull := firstFullWeekStart(now.Year(), now.Location())
	lastBefore := firstFull.AddDate(0, 0, -7)
	firstAllowed := firstFullWeekStart(minBackfillYear, now.Location())

	for week := lastBefore; !week.Before(firstAllowed) && !done(); week = week.AddDate(0, 0, -7) {
		var batch []averagesales.TurnoverRow
		for folderID, memberIDs := range groups {
			want := toSet(memberIDs)
			rows, err := uc.fetchGroupRows(ctx, week, "week", client.ProfitFilter{ProductFolderID: folderID}, want, active, counts)
			if err != nil {
				return err
			}
			batch = append(batch, rows...)
		}

		var chunk []string
		for _, id := range singles {
			if counts[id] < weeklyNeed {
				chunk = append(chunk, id)
			}
		}
		for i := 0; i < len(chunk); i += noGroupBatch {
			end := min(i+noGroupBatch, len(chunk))
			rows, err := uc.fetchGroupRows(ctx, week, "week", client.ProfitFilter{ProductIDs: chunk[i:end]}, nil, active, counts)
			if err != nil {
				return err
			}
			batch = append(batch, rows...)
		}

		if len(batch) > 0 {
			if err := uc.repo.UpsertWeeklyTurnover(ctx, batch); err != nil {
				return err
			}
		}
	}
	return nil
}

// fetchGroupRows — запрос отчёта за период для группы/пачки: строки только из
// want (nil — все строки фильтра), qty по каталогу товара, счётчик not null.
func (uc *UseCase) fetchGroupRows(ctx context.Context, start time.Time, interval string, filter client.ProfitFilter, want map[string]struct{}, active map[string]averagesales.TurnoverProduct, counts map[string]int) ([]averagesales.TurnoverRow, error) {
	to := periodEnd(start, interval)

	rows, err := uc.ms.FetchProfitTurnover(ctx, start, to, interval, filter)
	if err != nil {
		return nil, err
	}

	out := make([]averagesales.TurnoverRow, 0, len(rows))
	for _, r := range rows {
		id := productIDFromHref(r.Assortment.Meta.Href)
		if want != nil {
			if _, ok := want[id]; !ok {
				continue
			}
		}
		p, ok := active[id]
		if !ok {
			continue
		}
		qty, ok := uc.calcQty(r, p)
		if !ok {
			continue
		}
		out = append(out, averagesales.TurnoverRow{ProductID: id, PeriodStart: start, Qty: qty})
		counts[id]++
	}
	return out, nil
}

// toSet — множество из списка id.
func toSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

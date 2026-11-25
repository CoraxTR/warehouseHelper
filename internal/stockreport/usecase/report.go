// Пакет usecase — ежедневная сборка отчёта «остатки против средних продаж» и
// отправка его в чат управления складом (модуль stockreport).
//
// Источники чужие, читаются через швы: остатки — кэш модуля «Сроки»
// (владелец product_stock), средние продажи за неделю — модуль «Средние
// продажи» (владелец product_weekly_turnover/product_monthly_turnover).
// Своей таблицы у модуля нет: отчёт ничего не пишет.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"warehouseHelper/internal/discounts"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/stock"
	"warehouseHelper/internal/stockreport"
)

const (
	trackPkg = "stockreport"

	// pollInterval — период проверки «наступило ли время отчёта».
	pollInterval = time.Minute
)

// StockReader — кэш остатков модуля «Сроки»: весь каталог с лотами. Товары без
// лотов приходят с пустым Lots — для отчёта это «нет в наличии», поэтому
// нулевые остатки видны, даже когда строк product_stock нет вовсе.
type StockReader interface {
	Snapshot() []stock.Product
}

// SalesReader — средние продажи за неделю (шт) по товарам: недельные товары —
// своё недельное среднее, месячные — среднемесячное / 4. Товары без продаж в
// карту не попадают.
type SalesReader interface {
	WeeklyAverages(ctx context.Context, productIDs []string) (map[string]float64, error)
}

// SurplusReader — избытки дня от модуля скидок (владелец расчёта: остаток,
// накопленный до срока, против продаж до этой даты). Отчёт не считает избыток
// сам: числа в чате обязаны совпадать с движком, который ставит 10 %.
type SurplusReader interface {
	SurplusLots(ctx context.Context, at time.Time) ([]discounts.SurplusLot, error)
}

// Notifier — сообщения в чат управления складом. Rich-вариант: отчёт уходит
// одним rich-сообщением (заголовки, таблицы, сворачиваемые блоки), названия
// экранируются на стороне сборки.
type Notifier interface {
	NotifyWarehouseManagementRich(markdown string) error
}

// Part — сообщение отчёта: rich-markdown одного прохода. Частей в проходе не
// больше одной: лимит rich-сообщения (32 768 символов) перекрывает весь отчёт,
// резать на куски больше не нужно.
type Part struct {
	Text string
}

// UseCase — ежедневный отчёт. Живёт в одной горутине: состояние (маркер дня и
// очередь недоставленного) под мутексом, потому что очередь пробует отправить
// первый тик после рестарта.
type UseCase struct {
	stock   StockReader
	sales   SalesReader
	surplus SurplusReader
	notify  Notifier

	// now — часы юзкейса (в тестах подменяются): день и время отчёта считаются
	// по локальному времени склада.
	now func() time.Time

	mu sync.RWMutex
	// lastRunDay — дата последнего собранного отчёта. Маркер в памяти, а не в
	// БД: рестарт после 16:00 даст повторный проход, а он безвреден — отчёт
	// только читает остатки и средние.
	lastRunDay time.Time
	// pending — недоставленные сообщения: Telegram не принял — шлём со
	// следующего тика по порядку, срез только после успеха. Недоставленное
	// ДОПИСЫВАЕТСЯ, не перезаписывается (иначе список прошлого прохода
	// потерялся бы).
	pending []Part
}

// New собирает юзкейс. notify может быть nil (тесты): отправка тогда no-op,
// сборка отчёта работает.
func New(stockReader StockReader, salesReader SalesReader, surplusReader SurplusReader, notify Notifier) *UseCase {
	return &UseCase{
		stock:   stockReader,
		sales:   salesReader,
		surplus: surplusReader,
		notify:  notify,
		now:     time.Now,
	}
}

// Start запускает ежедневный отчёт до отмены ctx. Раз в минуту проверяет,
// наступило ли время at (время дня от полуночи, локальное время процесса) и не
// собран ли отчёт уже сегодня. ПК проспал 16:00 — отчёт уходит первым же тиком
// после пробуждения; ошибка чтения БД — ретрай на следующем тике.
//
// Блокируется до отмены ctx: вызывать только из горутины (app.background).
func (uc *UseCase) Start(ctx context.Context, at time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		uc.Tick(ctx, at)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick — один тик отчёта; ошибки только логируются (ретрай на следующем тике).
// Вынесен отдельно от Start, чтобы тесты гоняли проход без тикера.
func (uc *UseCase) Tick(ctx context.Context, at time.Duration) {
	uc.flushQueue()

	now := uc.now()
	if minutesFromMidnight(now) < at {
		return
	}
	if uc.done(now) {
		return
	}

	parts, err := uc.Build(ctx)
	if err != nil {
		slog.Info(fmt.Sprintf("stockreport: отчёт не собран (повтор на следующем тике): %v", err))
		return
	}

	// День закрываем и когда отправлять нечего: пустой отчёт — это тоже
	// результат (всё в норме), повторять сборку каждую минуту незачем.
	uc.markDone(now)

	uc.enqueue(parts)
}

// Build собирает сообщения отчёта: по одному на список, длинные списки делятся
// на части. Пустые списки сообщений не дают вовсе.
func (uc *UseCase) Build(ctx context.Context) ([]Part, error) {
	done := metrics.Track(trackPkg, "Build")
	defer done()

	snapshot := uc.stock.Snapshot()

	ids := make([]string, 0, len(snapshot))
	for _, p := range snapshot {
		ids = append(ids, p.ID)
	}

	avgs, err := uc.sales.WeeklyAverages(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("средние продажи за неделю: %w", err)
	}

	// Избытки считает модуль скидок тем же расчётом, что даёт 10 %: отчёт
	// только перекладывает готовые пары в блок. Ошибка чтения не отменяет
	// первые два блока — «нет в наличии» важнее, а в логе остаётся след.
	surplus, err := uc.surplus.SurplusLots(ctx, uc.now())
	if err != nil {
		slog.Warn("stockreport: избытки не прочитаны", "err", err)
	}

	positions := make([]stockreport.Position, 0, len(snapshot))
	for _, p := range snapshot {
		pos := stockreport.Position{Name: p.Name, Qty: lotQty(p.Lots)}
		if avg, ok := avgs[p.ID]; ok {
			pos.AvgWeek = &avg
		}
		positions = append(positions, pos)
	}

	report := stockreport.Split(positions)
	report.Surplus = surplus

	text := report.RichMarkdown()
	if text == "" {
		return nil, nil // все три списка пусты — отправлять нечего
	}

	return []Part{{Text: text}}, nil
}

// enqueue ставит сообщения в очередь отправки и сразу пробует отправить.
func (uc *UseCase) enqueue(parts []Part) {
	if len(parts) == 0 {
		return
	}

	uc.mu.Lock()
	uc.pending = append(uc.pending, parts...)
	uc.mu.Unlock()

	uc.flushQueue()
}

// send отправляет одно сообщение отчёта.
func (uc *UseCase) send(part Part) error {
	return uc.notify.NotifyWarehouseManagementRich(part.Text)
}

// flushQueue отправляет недоставленное по порядку, пока Telegram не откажет
// (тогда остаток очереди ждёт следующего тика) или очередь не кончится.
func (uc *UseCase) flushQueue() {
	for {
		uc.mu.RLock()
		if uc.notify == nil || len(uc.pending) == 0 {
			uc.mu.RUnlock()
			return
		}
		part := uc.pending[0]
		uc.mu.RUnlock()

		if err := uc.send(part); err != nil {
			slog.Info(fmt.Sprintf("stockreport: сообщение не доставлено (повтор на следующем тике): %v", err))
			return
		}

		uc.mu.Lock()
		uc.pending = uc.pending[1:]
		uc.mu.Unlock()
	}
}

// done — отчёт за этот день уже собран (маркер в памяти процесса).
func (uc *UseCase) done(now time.Time) bool {
	uc.mu.RLock()
	defer uc.mu.RUnlock()

	return !uc.lastRunDay.IsZero() && uc.lastRunDay.Equal(normalizeDate(now))
}

// markDone — отметить день собранным.
func (uc *UseCase) markDone(now time.Time) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	uc.lastRunDay = normalizeDate(now)
}

// lotQty — физический остаток товара: сумма остатков всех партий. Пустой Lots —
// ноль (товара нет в наличии).
func lotQty(lots []stock.Lot) int64 {
	var qty int64
	for _, l := range lots {
		qty += l.Qty
	}

	return qty
}

// minutesFromMidnight — время дня в минутах от полуночи (локальное время).
func minutesFromMidnight(t time.Time) time.Duration {
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

// normalizeDate — дата без времени в UTC-полночь: сравнение «этот же день»
// не зависит от зоны и часового пояса.
func normalizeDate(t time.Time) time.Time {
	y, m, d := t.Date()

	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

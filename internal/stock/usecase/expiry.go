// Уборка пар под заморозку: раз в сутки (по умолчанию 20:00 локального
// времени склада) пары, у которых срок годности истекает ЗАВТРА или уже
// истёк, убираются из «Сроков», а склад получает сообщение со списком того,
// что надо заморозить. Правило владельца (21.10.2026): берём всё, что ≤
// завтра, — иначе просроченные пары, оставшиеся с прошлых дней (прогуляли,
// приложение лежало), висели бы в «Сроках» навсегда.
//
// Лоты удаляются по обычной цепочке модуля («БД → кэш → событие»): страница
// «Сроки» обновляется ws-дельтой lot_delete, daystate пересчитывает строку
// дня, расчёт скидок — свои пары. Отдельного шва «заморозка» не заводим:
// модуля заморозки нет, сообщение в чат склада и есть уведомление.
package usecase

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/stock"
)

// freezeHeader/freezeContHeader — заголовок сообщения; продолжение нужно,
// когда список не влезает в лимит Telegram (4096 символов).
const (
	freezeHeader     = "Позиции убраны из сроков, нужно заморозить:"
	freezeContHeader = "Позиции убраны из сроков, нужно заморозить (продолжение):"
	// freezeMaxLen — предел ОДНОГО сообщения. Меньше лимита Telegram (4096) с
	// запасом: len() считает байты, кириллица в UTF-8 — два байта на символ,
	// то есть фактический лимит символов выходит вдвое скромнее.
	freezeMaxLen = 3500
)

// expiryMatch — товар с парами, чей срок ≤ границы (снимок под RLock).
type expiryMatch struct {
	productID string
	name      string
	group     string
	dates     []time.Time         // сроки к удалению (включая нулевой остаток)
	frozen    []stock.ExpiringLot // только пары с физическим остатком
}

// StartExpiryWatch запускает ежедневную уборку пар под заморозку до отмены
// ctx (образец daystate.Start): раз в минуту проверяет, наступило ли
// freezeTime (время дня от полуночи, локальное время процесса), и не
// отработала ли уже уборка сегодня. ПК проспал 20:00 — уборка идёт первым же
// тиком после пробуждения; ошибка БД — ретрай на следующем тике.
//
// Блокируется до отмены ctx: вызывать только из горутины (app.background).
func (uc *StockUseCase) StartExpiryWatch(ctx context.Context, freezeTime time.Duration) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		uc.tryExpiryCleanup(ctx, freezeTime)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tryExpiryCleanup — один тик уборки; ошибки только логируются.
//
// Недоставленные сообщения (Telegram не принял) ретраятся на КАЖДОМ тике, вне
// зависимости от времени: смысл уборки — в сообщении, а лоты к этому моменту
// уже удалены. Пока очередь не пуста, новая уборка не начинается — иначе
// список прошлого прохода потерялся бы.
func (uc *StockUseCase) tryExpiryCleanup(ctx context.Context, freezeTime time.Duration) {
	uc.flushFreezeQueue()

	now := uc.now()
	if minutesFromMidnight(now) < freezeTime {
		return
	}
	if uc.freezeDone(now) {
		return
	}

	if _, err := uc.RemoveExpiringLots(ctx, now.AddDate(0, 0, 1)); err != nil {
		slog.Info(fmt.Sprintf("stock: уборка пар под заморозку: %v", err))
		return
	}

	uc.mu.Lock()
	uc.lastFreezeDay = normalizeDate(now)
	uc.mu.Unlock()
}

// RemoveExpiringLots убирает из «Сроков» пары со сроком ≤ limit (граница
// включительно, дата нормализуется к UTC-полуночи) и возвращает список с
// физическим остатком — по нему строится сообщение складу. Пары с нулевым
// остатком удаляются молча (морозить нечего), сообщение уходит только когда
// есть что морозить.
//
// Запись синхронна: БД → кэш → события ws; после успеха дёргаются daystate и
// слушатель смены лотов. Ошибка БД — ничего не изменено, ретрай на следующем
// тике. Идемпотентно: удалённых пар в кэше уже нет, повтор ничего не найдёт.
func (uc *StockUseCase) RemoveExpiringLots(ctx context.Context, limit time.Time) ([]stock.ExpiringLot, error) {
	done := metrics.Track(trackPkg, "RemoveExpiringLots")
	defer done()

	matches := uc.expiringMatches(normalizeDate(limit))
	if len(matches) == 0 {
		return nil, nil
	}

	writes := make([]stock.ProductWrite, 0, len(matches))
	ids := make([]string, 0, len(matches))
	var frozen []stock.ExpiringLot
	for _, m := range matches {
		writes = append(writes, stock.ProductWrite{ProductID: m.productID, Deletes: m.dates})
		ids = append(ids, m.productID)
		frozen = append(frozen, m.frozen...)
	}

	if err := uc.repo.ReplaceStockLots(ctx, writes); err != nil {
		return nil, fmt.Errorf("remove expiring stock lots: %w", err)
	}

	uc.mu.Lock()
	for _, m := range matches {
		if p, ok := uc.cache[m.productID]; ok {
			p.Lots = dropLots(p.Lots, m.dates)
		}
	}
	uc.mu.Unlock()

	if uc.pub != nil {
		for _, m := range matches {
			for _, bb := range m.dates {
				uc.pub.PublishStockChange(stock.Event{Kind: stock.EventLotDelete, ProductID: m.productID, BestBefore: bb})
			}
		}
	}

	uc.notifyDayState(ctx, ids...)
	uc.notifyLotChange(ctx, ids...)

	if len(frozen) > 0 {
		uc.enqueueFreeze(freezeMessageParts(frozen))
	}
	slog.Info(fmt.Sprintf("stock: убрано из сроков пар под заморозку: %d (товаров: %d)", len(frozen), len(matches)))

	return frozen, nil
}

// expiringMatches собирает товары с парами срок ≤ limit по кэшу. Порядок —
// как у страницы «Сроки» (группа, название, id): сообщение складу читается
// сверху вниз по группам, а не в порядке обхода map.
func (uc *StockUseCase) expiringMatches(limit time.Time) []expiryMatch {
	uc.mu.RLock()
	defer uc.mu.RUnlock()

	matches := make([]expiryMatch, 0)
	for _, p := range uc.cache {
		m := expiryMatch{productID: p.ID, name: p.Name, group: p.GroupName}
		for _, l := range p.Lots {
			bb := normalizeDate(l.BestBefore)
			if bb.After(limit) {
				continue
			}
			m.dates = append(m.dates, bb)
			if l.Qty > 0 {
				m.frozen = append(m.frozen, stock.ExpiringLot{
					ProductID: p.ID, Name: p.Name, Qty: l.Qty, BestBefore: bb,
				})
			}
		}
		if len(m.dates) > 0 {
			matches = append(matches, m)
		}
	}

	slices.SortFunc(matches, func(a, b expiryMatch) int {
		if c := cmp.Compare(strings.ToLower(a.group), strings.ToLower(b.group)); c != 0 {
			return c
		}
		if c := cmp.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); c != 0 {
			return c
		}
		return cmp.Compare(a.productID, b.productID)
	})

	return matches
}

// dropLots убирает из среза лоты с указанными сроками. Пустой результат —
// пустой МАССИВ, не nil: lots в JSON обязан быть [] (страница итерирует
// p.lots.length).
func dropLots(lots []stock.Lot, dates []time.Time) []stock.Lot {
	out := make([]stock.Lot, 0, len(lots))
	for _, l := range lots {
		if containsTime(dates, l.BestBefore) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// enqueueFreeze ставит сообщения в очередь отправки и сразу пробует отправить.
func (uc *StockUseCase) enqueueFreeze(parts []string) {
	if uc.notifier == nil || len(parts) == 0 {
		return
	}

	uc.mu.Lock()
	uc.pendingFreeze = append(uc.pendingFreeze, parts...)
	uc.mu.Unlock()

	uc.flushFreezeQueue()
}

// flushFreezeQueue отправляет недоставленное по порядку, пока Telegram не
// откажет (тогда остаток очереди ждёт следующего тика) или очередь не кончится.
func (uc *StockUseCase) flushFreezeQueue() {
	for {
		uc.mu.RLock()
		if uc.notifier == nil || len(uc.pendingFreeze) == 0 {
			uc.mu.RUnlock()
			return
		}
		text := uc.pendingFreeze[0]
		uc.mu.RUnlock()

		if err := uc.notifier.NotifyWarehouse(text); err != nil {
			slog.Info(fmt.Sprintf("stock: сообщение о заморозке не доставлено (повтор на следующем тике): %v", err))
			return
		}

		uc.mu.Lock()
		uc.pendingFreeze = uc.pendingFreeze[1:]
		uc.mu.Unlock()
	}
}

// freezeDone — уборка сегодня уже была (маркер в памяти процесса).
func (uc *StockUseCase) freezeDone(now time.Time) bool {
	uc.mu.RLock()
	defer uc.mu.RUnlock()

	return !uc.lastFreezeDay.IsZero() && uc.lastFreezeDay.Equal(normalizeDate(now))
}

// freezeMessageParts собирает сообщение складу, деля его на части под лимит
// Telegram: длинный список иначе не отправится вовсе, и ретрай стал бы вечным.
// Нумерация сквозная по всему списку.
func freezeMessageParts(lots []stock.ExpiringLot) []string {
	parts := make([]string, 0, 1)
	var b strings.Builder
	b.WriteString(freezeHeader)

	for i, l := range lots {
		line := fmt.Sprintf("%d. %d шт. %s до %s", i+1, l.Qty, l.Name, l.BestBefore.Format("02.01.2006"))
		if b.Len()+1+len(line) > freezeMaxLen {
			parts = append(parts, b.String())
			b.Reset()
			b.WriteString(freezeContHeader)
		}
		b.WriteString("\n")
		b.WriteString(line)
	}

	return append(parts, b.String())
}

// minutesFromMidnight — время дня в минутах от полуночи (локальное время).
func minutesFromMidnight(t time.Time) time.Duration {
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

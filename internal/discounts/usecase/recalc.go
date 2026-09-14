// Расчётный цикл модуля: утренний пересмотр лестницы по сроку годности
// (вт/чт/сб, «только вверх») и часовой пересчёт избытка (RecalcSurplus).
//
// Оба шага идут по одному снапшоту входа: состояния пар считает evaluate.go,
// значения пишет шов стока (DiscountWriter), снапшот расчёта и изменения
// эффективной скидки держит реестр (registry.go), про изменения людям сообщает
// notify.go. Своих часов и соединений пакет не заводит: часы — uc.now, данные —
// швы ports.go.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"warehouseHelper/internal/discounts"
)

// expiryDay — день пересмотра лестницы по сроку: вторник, четверг, суббота
// (КТ-дни склада). В остальные дни автоматика ступени не двигает.
func expiryDay(t time.Time) bool {
	switch t.Weekday() {
	case time.Tuesday, time.Thursday, time.Saturday:
		return true
	default:
		return false
	}
}

// loadInputs — общий шаг обоих пересчётов: снапшот входа расчёта на день.
// today обнулён до суток (время суток в расчёт не входит, а по дате
// выбирается завершённый период оборота).
func (uc *UseCase) loadInputs(ctx context.Context, today time.Time) ([]discounts.Input, error) {
	inputs, err := uc.repo.LoadDiscountInput(ctx, today)
	if err != nil {
		return nil, fmt.Errorf("снапшот входа на %s: %w", today.Format(time.DateOnly), err)
	}
	return inputs, nil
}

// RecalcExpiry — утренний пересчёт по сроку годности.
//
// Лестницу пересматривает только в КТ-дни (вт/чт/сб) и только один раз за день:
// маркер дня закрывает и повторный запуск, и «догон» после сна (после рестарта
// шаг дорабатывается, повторно уже сделанный — нет). Записанное значение —
// строго вверх: оно должно быть выше и эффективной скидки канала, и значения
// plain-колонки, иначе пара остаётся как есть (понижать и затирать чужое
// автоматика не имеет права). Пары без ступени на сегодня (вне окна, срок не
// задан, партия просрочена) в правки не попадают вовсе: 0 и NULL не пишем.
func (uc *UseCase) RecalcExpiry(ctx context.Context, now time.Time) error {
	today := beginningOfDay(now)
	if !expiryDay(today) {
		return nil
	}

	done, err := uc.repo.DayFlagDone(ctx, today, discounts.FlagExpiry)
	if err != nil {
		return fmt.Errorf("маркер дня пересчёта по сроку: %w", err)
	}
	if done {
		return nil
	}

	inputs, err := uc.loadInputs(ctx, today)
	if err != nil {
		return err
	}
	// Оборот берём из шва: лестница от продаж не зависит, но этим расчётом
	// заменяется снапшот реестра — без оборота из него выпали бы избыточные
	// пары (окно, страница «Скидки» и дайджест показали бы пустую очередь).
	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return fmt.Errorf("оборот товаров расчёта: %w", err)
	}
	pairs := Evaluate(inputs, rates, today)

	if err := uc.writeAndRegister(ctx, pairs, expiryWrites(pairs)); err != nil {
		return err
	}

	if err := uc.repo.MarkDayFlag(ctx, today, discounts.FlagExpiry); err != nil {
		return fmt.Errorf("маркер дня пересчёта по сроку: %w", err)
	}
	return nil
}

// RecalcSurplus — часовой пересчёт избытка.
//
// Оборот приходит только швом модуля средних продаж: сохранённый —
// Turnover.Averages (без обращений в МС), затем свежий — Turnover.RefreshCurrent
// по товарам, где решение может уйти (события стока MarkDirty и пары в избытке
// сейчас). Ошибка Averages тик НЕ выполняет: без оборота все избытки выглядели
// бы снятыми, а снятие избытка — это запись. Ошибка RefreshCurrent тик не
// роняет (считаем по сохранённому обороту, свежий догонит следующий час).
// Маркер дня отмечается на каждом часу: он говорит «пересчёт за день был», по
// нему приложение добирает пропущенный запуск после сна.
func (uc *UseCase) RecalcSurplus(ctx context.Context, now time.Time) error {
	today := beginningOfDay(now)

	inputs, err := uc.loadInputs(ctx, today)
	if err != nil {
		return err
	}

	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return fmt.Errorf("оборот товаров расчёта: %w", err)
	}
	pairs := Evaluate(inputs, rates, today)

	if fresh := uc.freshTurnover(ctx, pairs); fresh != nil {
		for pid, v := range fresh {
			rates[pid] = v
		}
		pairs = Evaluate(inputs, rates, today)
	}

	if err := uc.writeAndRegister(ctx, pairs, surplusWrites(pairs)); err != nil {
		return err
	}

	if err := uc.repo.MarkDayFlag(ctx, today, discounts.FlagSurplus); err != nil {
		return fmt.Errorf("маркер дня пересчёта избытка: %w", err)
	}
	return nil
}

// expiryWrites — правки по сроку: только рост. Ступень идёт в правки, если она
// строго выше того, что уже стоит у пары (эффективной скидки канала и значения
// plain-колонки): равенство — уже применено, меньше — понижение, которого
// автоматика не делает. Ручная скидка тоже входит в «уже стоит», поэтому рост
// под ней поднимает только колонку движка, а на сайте остаётся ручная.
func expiryWrites(pairs []PairState) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(pairs))
	for _, p := range pairs {
		if p.Expiry == nil || *p.Expiry <= appliedTop(p) {
			continue
		}
		writes = append(writes, discounts.DiscountWrite{
			ProductID:  p.ProductID,
			BestBefore: p.BestBefore,
			General:    p.Expiry,
			// Колонку ТГ ведёт ТГ-день (план 14:00 и подъём 16:00): своё
			// значение передаём как есть, чтобы запись general её не затирала.
			Telegram: p.TelegramPlain,
			Source:   discounts.SourceExpiry.String(),
		})
	}
	return writes
}

// surplusWrites — правки по избытку.
//
// Избыток — младший источник (ручная → срок → избыток), поэтому 10 % ставим
// только на паре без ручной и без сроковой скидки и только на пустое место:
// стоящее значение (в том числе подъём ТГ-дня до 20 %) автоматика не понижает.
// Снятие (в БД NULL) — только у значения, поставленного избытком: чужую
// ступень и метку источника пересчёт не убирает.
func surplusWrites(pairs []PairState) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(pairs))
	for _, p := range pairs {
		switch {
		case p.Manual == nil && p.Expiry == nil && p.HasSurplus:
			if appliedTop(p) != 0 {
				continue // место занято — избыток не понижает и не переписывает
			}
			percent := discounts.SurplusPercent()
			writes = append(writes, discounts.DiscountWrite{
				ProductID:  p.ProductID,
				BestBefore: p.BestBefore,
				General:    &percent,
				// Колонку ТГ ведёт ТГ-день: своё значение отдаём как есть.
				Telegram: p.TelegramPlain,
				Source:   discounts.SourceSurplus.String(),
			})
		case surplusLeft(p):
			writes = append(writes, discounts.DiscountWrite{
				ProductID:  p.ProductID,
				BestBefore: p.BestBefore,
				General:    nil, // снятие: движок пишет NULL, а не 0
				Telegram:   p.TelegramPlain,
			})
		}
	}
	return writes
}

// surplusLeft — значение plain-колонки поставлено избытком и больше не нужно:
// избыток пропал или дорогу уступил ручной либо сроковой скидке. Узнаём по
// значению и метке: ровно 10 % (discounts.SurplusPercent) без ступени по сроку
// на сегодня, а метка источника (product_stock.discount_source), если она есть,
// должна говорить «избыток».
func surplusLeft(p PairState) bool {
	if discountPercent(p.AppliedPlain) != discounts.SurplusPercent() {
		return false // стоит не избыточное значение — не наше
	}
	if p.SourceRaw != "" && p.SourceRaw != discounts.SourceSurplus.String() {
		return false // метка говорит: значение поставлено не избытком
	}
	if p.Expiry != nil {
		return false // ступень по сроку держит это же значение — не снимаем
	}
	return p.Manual != nil || !p.HasSurplus
}

// freshTurnover — свежий оборот по товарам, где решение может уйти: события
// стока с прошлого тика (MarkDirty) и пары с избытком сейчас. Пустой список —
// шва не касаемся (nil-карта: расчёт остаётся на сохранённом обороте).
// Ошибка шва — в лог и та же nil-карта: часовой тик из-за недоступного МС
// пропускать нельзя, сохранённый оборот уже получен от Averages.
func (uc *UseCase) freshTurnover(ctx context.Context, pairs []PairState) map[string]float64 {
	ids := uc.freshIDs(pairs)
	if len(ids) == 0 {
		return nil
	}

	rates, err := uc.turnover.RefreshCurrent(ctx, ids)
	if err != nil {
		slog.Info(fmt.Sprintf("discounts: оборот избытка (%d товаров): %v", len(ids), err))
		return nil
	}
	return rates
}

// inputProductIDs — товары входа расчёта без повторов (кого спрашивать об
// обороте): лоты одного товара дают одну строку.
func inputProductIDs(inputs []discounts.Input) []string {
	seen := make(map[string]struct{}, len(inputs))
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if _, ok := seen[in.ProductID]; ok {
			continue
		}
		seen[in.ProductID] = struct{}{}
		ids = append(ids, in.ProductID)
	}
	return ids
}

// freshIDs — товары свежего оборота: события стока (их забирает takeDirty) и
// пары в избытке, без повторов, в порядке появления.
func (uc *UseCase) freshIDs(pairs []PairState) []string {
	ids := uc.takeDirty()
	seen := make(map[string]struct{}, len(ids))
	for _, pid := range ids {
		seen[pid] = struct{}{}
	}
	for _, pid := range SurplusPairs(pairs) {
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		ids = append(ids, pid)
	}
	return ids
}

// appliedTop — верхняя граница того, что уже стоит у пары в канале сайта:
// эффективная скидка (ручная перекрывает plain) и отдельно значение
// plain-колонки. 0 — скидки не стоит. Автоматика пишет только выше границы.
func appliedTop(p PairState) int16 {
	top := discountPercent(p.Applied)
	if v := discountPercent(p.AppliedPlain); v > top {
		top = v
	}
	return top
}

// writeAndRegister — запись правок через шов стока, обновление снапшота реестра
// и уведомления об изменениях: пустой батч в сток не уходит (работы нет), реестр
// обновляется всегда — окно, очередь и отчёт живут по последнему расчёту, даже
// если записывать было нечего. Изменения эффективной скидки (реестр сравнивает
// новый расчёт с предыдущим) уходят людям в общий канал.
func (uc *UseCase) writeAndRegister(ctx context.Context, pairs []PairState, writes []discounts.DiscountWrite) error {
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("запись скидок: %w", err)
		}
	}

	applyWrites(pairs, writes)
	uc.notifyChanges(ctx, uc.reg.Replace(pairs))

	return nil
}

// applyWrites — снапшот расчёта после записи: у пары с правкой эффективная
// скидка канала — ручная (если она есть), иначе записанное значение; снятое
// значение (nil) убирает и plain, и эффективную. Без этого реестр сравнивал бы
// новые значения со старыми из БД и не видел бы изменений.
func applyWrites(pairs []PairState, writes []discounts.DiscountWrite) {
	byKey := make(map[discounts.LotKey]discounts.DiscountWrite, len(writes))
	for _, w := range writes {
		byKey[discounts.LotKey{ProductID: w.ProductID, BestBefore: beginningOfDay(w.BestBefore)}] = w
	}

	for i := range pairs {
		w, ok := byKey[pairs[i].Key]
		if !ok {
			continue
		}
		pairs[i].AppliedPlain = copyDiscount(w.General)
		pairs[i].Applied = effectiveDiscount(pairs[i].Manual, w.General)
		pairs[i].SourceRaw = w.Source
	}
}

// copyDiscount — своя копия значения скидки: снапшот расчёта не держит
// указатели вызывающего (значения правок переиспользуются в батче шва).
func copyDiscount(v *int16) *int16 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

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
	pairs := Evaluate(inputs, nil, today)

	// Оборот здесь не освежаем: лестница по сроку от продаж не зависит,
	// ей хватает действующего оборота снапшота.
	if err := uc.writeAndRegister(ctx, pairs, expiryWrites(pairs)); err != nil {
		return err
	}

	if err := uc.repo.MarkDayFlag(ctx, today, discounts.FlagExpiry); err != nil {
		return fmt.Errorf("маркер дня пересчёта по сроку: %w", err)
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

// writeAndRegister — запись правок через шов стока и обновление снапшота
// реестра: пустой батч в сток не уходит (работы нет), реестр обновляется всегда
// — окно, очередь и отчёт живут по последнему расчёту, даже если записывать
// было нечего.
func (uc *UseCase) writeAndRegister(ctx context.Context, pairs []PairState, writes []discounts.DiscountWrite) error {
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("запись скидок: %w", err)
		}
	}

	applyWrites(pairs, writes)
	uc.reg.Replace(pairs)

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

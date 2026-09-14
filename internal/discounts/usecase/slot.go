package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"warehouseHelper/internal/discounts"
)

// ТГ-день модуля (решение владельца 14.09.2026, §13 черновика): в 14:00 чат
// склада получает план слота, в 16:00 скидка сайта поднимается до плана.
//
// План — это ВЫБОРКА позиций, а не новая скидка: percent позиции уже известен
// расчёту (ступень по сроку, ручная), слот лишь говорит менеджерам, что пора
// ставить её на сайте, и держит ёмкость (cap). ТГ-колонка (discount_telegram)
// получает значение плана — она показывает, до чего скидку поднимут в 16:00.

// Ёмкость слота и пороги отбора (решение владельца, §13 черновика).
const (
	// slotMainPercent — скидка дня, с которой позиция идёт в план сразу.
	slotMainPercent = 20
	// slotBasePercent — ступень, которую план поднимает до slotMainPercent
	// (добор: слот наполнен меньше половины).
	slotBasePercent = 10
	// slotMinDays — минимальный остаток дней до срока: позиция должна успеть
	// продаться, иначе скидка в ТГ бессмысленна.
	slotMinDays = 2
)

// slotPosition — позиция плана: пара и скидка, которую ей предлагают.
type slotPosition struct {
	pair    PairState
	percent int16
	// writeTelegram — ставить ли значение в ТГ-колонку: у ручной скидки
	// значение уже на сайте, писать её в ТГ незачем.
	writeTelegram bool
}

// RunSlotPlan — собрать и отправить план ТГ-слота (14:00): позиции со скидкой
// дня от 20 %, добираем позиции со ступенью 10 % (их план — 20 %), если слот
// наполнен меньше чем наполовину. Позиции предыдущей рассылки не повторяем
// (антидубль по лоту). Пустой слот — молчание: сообщение «позиций нет» ничего
// не сообщает, а дайджест 09:00 про это уже сказал.
//
// Шаги идемпотентны: перед работой проверяется маркер дня (флаг стоит — шаг
// пропускается), после успеха ставится FlagPlan.
func (uc *UseCase) RunSlotPlan(ctx context.Context, now time.Time, capacity int) error {
	day := beginningOfDay(now)

	done, err := uc.repo.DayFlagDone(ctx, day, discounts.FlagPlan)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return err
	}
	// Оборот обязателен: слот строится по лестнице (продажи ему не нужны), но
	// этим же расчётом заменяется снапшот реестра — без оборота из него
	// пропали бы избыточные пары, и страница с отчётом показали бы пустую очередь.
	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return fmt.Errorf("оборот товаров слота: %w", err)
	}
	pairs := Evaluate(inputs, rates, day)

	prev, err := uc.repo.LastDigestPairs(ctx)
	if err != nil {
		return err
	}
	slot := uc.pickSlot(pairs, prev, capacity)

	if err := uc.writeSlot(ctx, slot, now); err != nil {
		return err
	}
	// Реестр отражает действующие значения канала сайта: слот их не меняет
	// (ТГ-колонка — не значение для сайта), поэтому изменения пусты и
	// уведомлений не будет. Вызов нужен, чтобы снапшот после сбора был свежим.
	uc.replaceRegistry(pairs, now)

	if err := uc.repo.MarkDayFlag(ctx, day, discounts.FlagPlan); err != nil {
		return err
	}
	if len(slot) == 0 {
		slog.Info("discounts: план ТГ-слота пуст — рассылка не отправлена")
		return nil
	}
	return nil
}

// RunRaise — поднять скидку сайта до плана ТГ-слота (16:00): по позициям
// сегодняшней ОТПРАВЛЕННОЙ рассылки general := max(текущее, план).
//
// Распроданные позиции пропускаем (поднимать нечего), ручные — не трогаем:
// значение менеджера важнее плана. Текущее больше плана — тоже пропуск:
// понижать автоматика не умеет.
func (uc *UseCase) RunRaise(ctx context.Context, now time.Time) error {
	day := beginningOfDay(now)

	done, err := uc.repo.DayFlagDone(ctx, day, discounts.FlagRaise)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	plan, err := uc.repo.TodaySlot(ctx, day)
	if err != nil {
		return err
	}

	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return err
	}
	// Оборот нужен не подъёму, а снапшоту реестра: он уходит в diff уведомлений
	// и на страницу, и без оборота из него выпали бы избыточные пары.
	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return fmt.Errorf("оборот товаров подъёма: %w", err)
	}
	pairs := Evaluate(inputs, rates, day)

	raised := make([]discounts.LotKey, 0, len(plan))
	if len(plan) > 0 {
		writes := make([]discounts.DiscountWrite, 0, len(plan))
		// По индексу, а не по копии: в реестр должен уйти снапшот с поднятым
		// значением — иначе уведомление о подъёме не уйдёт, а на следующем часу
		// придёт ложное «поднять скидку» (движок перечитает БД и увидит рост).
		for i := range pairs {
			p := &pairs[i]
			percent, ok := plan[p.Key]
			if !ok {
				continue
			}
			raised = append(raised, p.Key)
			if p.Manual != nil && *p.Manual > 0 {
				continue // ручная скидка важнее плана
			}
			if discountPercent(p.Applied) >= percent {
				continue // уже не ниже плана: не понижаем
			}
			general := percent
			p.AppliedPlain = &general
			p.SourceRaw = discounts.SourceExpiry.String()
			writes = append(writes, writeFor(*p))
		}

		if len(writes) > 0 {
			if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
				return fmt.Errorf("подъём скидок: %w", err)
			}
			// Снапшот реестра должен нести УЖЕ поднятое значение: иначе
			// уведомление о подъёме не уйдёт, а на следующем часу придёт
			// ложное «поднимите скидку» (расчёт перечитает БД и увидит рост).
			applyWrites(pairs, writes)
		}
	}

	if len(raised) > 0 {
		if err := uc.repo.MarkGeneralRaised(ctx, raised, now); err != nil {
			return err
		}
	}

	// Подъём меняет значение канала сайта — изменения отдаём уведомлениями
	// (тот же путь, что у расчётного тика).
	changes := uc.replaceRegistry(pairs, now)
	uc.notifyChanges(ctx, changes)

	if err := uc.repo.MarkDayFlag(ctx, day, discounts.FlagRaise); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("discounts: подъём general по %d позициям слота", len(raised)))
	return nil
}

// pickSlot — выбор позиций плана: сначала скидка дня 20 % и выше (в порядке
// приоритетов отчёта: ручные → по сроку), затем добор — позиции со ступенью
// ровно 10 %, если слот наполнен меньше чем наполовину (их план — 20 %).
func (uc *UseCase) pickSlot(pairs []PairState, prev map[discounts.LotKey]struct{}, capacity int) []slotPosition {
	if capacity <= 0 {
		return nil
	}

	slot := make([]slotPosition, 0, capacity)
	for _, p := range pairs {
		if !slotEligible(p, prev) {
			continue
		}
		switch {
		case p.Manual != nil && *p.Manual >= slotMainPercent:
			slot = append(slot, slotPosition{pair: p, percent: *p.Manual})
		case p.Expiry != nil && *p.Expiry >= slotMainPercent:
			slot = append(slot, slotPosition{pair: p, percent: *p.Expiry, writeTelegram: true})
		}
	}
	sortSlot(slot)
	if len(slot) > capacity {
		slot = slot[:capacity]
	}

	if len(slot)*2 >= capacity {
		return slot
	}
	// Добор: ступень ровно 10 % — на сайте её поднимут до 20 %, поэтому в
	// план позиция идёт с двадцатью (решение владельца, §13).
	inSlot := make(map[discounts.LotKey]struct{}, len(slot))
	for _, s := range slot {
		inSlot[s.pair.Key] = struct{}{}
	}
	extra := make([]slotPosition, 0, capacity)
	for _, p := range pairs {
		if len(slot)+len(extra) >= capacity {
			break
		}
		if _, ok := inSlot[p.Key]; ok {
			continue
		}
		if !slotEligible(p, prev) {
			continue
		}
		if p.Manual != nil && *p.Manual > 0 {
			continue
		}
		if p.Expiry == nil || *p.Expiry != slotBasePercent {
			continue
		}
		extra = append(extra, slotPosition{pair: p, percent: slotMainPercent, writeTelegram: true})
	}
	sortSlot(extra)

	return append(slot, extra...)
}

// writeSlot — записать план в ТГ-колонку и отправить список в чат склада.
func (uc *UseCase) writeSlot(ctx context.Context, slot []slotPosition, now time.Time) error {
	if len(slot) == 0 {
		return nil
	}

	writes := make([]discounts.DiscountWrite, 0, len(slot))
	items := make([]discounts.DigestItem, 0, len(slot))
	rows := make([]discounts.Row, 0, len(slot))
	for _, s := range slot {
		if s.writeTelegram && discountPercent(s.pair.TelegramPlain) != s.percent {
			w := writeFor(s.pair)
			w.Telegram = &s.percent
			writes = append(writes, w)
		}
		items = append(items, discounts.DigestItem{
			ProductID:  s.pair.ProductID,
			BestBefore: s.pair.BestBefore,
			Percent:    s.percent,
			Reason:     slotReason(s),
		})
		row := s.pair.Row()
		row.Percent = s.percent
		rows = append(rows, row)
	}
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("план слота: %w", err)
		}
	}

	record := discounts.DigestRecord{PlannedAt: beginningOfDay(now), ChatKind: discounts.ChatWarehouse}
	if err := uc.repo.SaveDigest(ctx, record, items); err != nil {
		return err
	}

	// Текст — тем же строителем, что дайджест: секция «в скидках» — план слота
	// (с процентом плана), секция избытка — то, что можно допродать.
	digest := discounts.BuildDigest(rows)
	digest.Date = now
	if text := digest.Text(); uc.warehouse != nil {
		if err := uc.warehouse.NotifyWarehouse(text); err != nil {
			return err
		}
	} else {
		slog.Info(fmt.Sprintf("discounts: план слота (канал склада не подключён): %s", digest.Text()))
	}

	// Маркер отправки — только после успешной отправки: иначе подъём 16:00
	// считал бы несобранную рассылку отправленной.
	return uc.repo.MarkDigestSent(ctx, discounts.ChatWarehouse, beginningOfDay(now), now)
}

// replaceRegistry — положить в реестр свежий снапшот и вернуть изменения
// эффективной скидки канала сайта (уведомления). Товары, оставшиеся без скидки,
// тоже проходят через реестр — их исчезновение даёт уведомление «убрать».
func (uc *UseCase) replaceRegistry(pairs []PairState, _ time.Time) []Change {
	return uc.reg.Replace(pairs)
}

// slotEligible — пара вообще может попасть в план: остаток дней до срока не
// меньше двух (позиция успевает продаться) и была ли она в прошлой рассылке.
func slotEligible(p PairState, prev map[discounts.LotKey]struct{}) bool {
	if p.DaysLeft < slotMinDays {
		return false
	}
	if _, ok := prev[p.Key]; ok {
		return false
	}
	return true
}

// slotReason — причина позиции для истории рассылки.
func slotReason(s slotPosition) string {
	if s.pair.Manual != nil && *s.pair.Manual > 0 && !s.writeTelegram {
		return discounts.ReasonManual
	}
	return discounts.ReasonExpiry
}

// sortSlot — порядок плана: та же логика, что в отчёте (ручные → срок →
// избыток, внутри — по сроку), чтобы список читался как дайджест.
func sortSlot(slot []slotPosition) {
	rows := make([]discounts.Row, len(slot))
	for i, s := range slot {
		rows[i] = s.pair.Row()
	}
	order := make([]int, len(slot))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra, rb := rows[order[a]], rows[order[b]]
		return lessRow(ra, rb)
	})
	sorted := make([]slotPosition, len(slot))
	for i, idx := range order {
		sorted[i] = slot[idx]
	}
	copy(slot, sorted)
}

// lessRow — порядок строк отчёта (повторяет discounts.Sort, но без экспорта
// cmp-функции из домена).
func lessRow(a, b discounts.Row) bool {
	ra, rb := rowRank(a.Source), rowRank(b.Source)
	if ra != rb {
		return ra < rb
	}
	if a.Source == discounts.SourceSurplus {
		return a.Coeff > b.Coeff
	}
	return a.BestBefore.Before(b.BestBefore)
}

// rowRank — группа строки в порядке отчёта (как discounts.sortRank).
func rowRank(s discounts.Source) int {
	switch s {
	case discounts.SourceManual:
		return 0
	case discounts.SourceExpiry:
		return 1
	case discounts.SourceSurplus:
		return 2
	default:
		return 3
	}
}

// Распроданную позицию отдельно искать не нужно: лот без остатка в снапшоте
// остатков не появляется, значит плана по нему в цикле подъёма просто нет.

// writeFor — правка пары с сохранением текущих значений: движок меняет одно
// поле, остальные (в том числе метка источника) уезжают как есть.
func writeFor(p PairState) discounts.DiscountWrite {
	return discounts.DiscountWrite{
		ProductID:  p.ProductID,
		BestBefore: p.BestBefore,
		General:    p.AppliedPlain,
		Telegram:   p.TelegramPlain,
		Source:     p.SourceRaw,
	}
}

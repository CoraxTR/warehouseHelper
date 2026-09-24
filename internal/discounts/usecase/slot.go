package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"warehouseHelper/internal/discounts"
)

// ТГ-день модуля (решение владельца 14.09.2026, §13 черновика; переработка
// 24.09.2026): в 09:00 автоматика только СЧИТАЕТ — повышения этого дня в
// general не пишутся. В 14:00 план дня применяется разом: часть позиций уходит в
// ТГ-рассылку (им пишется ТГ-колонка, сайт не трогаем), остальные получают
// скидку сайта сразу. В 15:00 склад рассылает список руками: подписчики узнают
// о скидках раньше покупателей сайта. В 16:00 непроданным позициям слота скидка
// сайта поднимается до плана.
//
// В рассылку идут только позиции, у которых скидка ПОВЫШАЕТСЯ (повышение по
// сроку или добор из избытка): позиция, скидка которой уже стоит на сайте,
// подписчику ничего не даёт.

// Ёмкость слота и пороги отбора (решение владельца, §13 черновика).
const (
	// slotMainPercent — скидка дня, с которой позиция идёт в план сразу.
	slotMainPercent = 20
	// slotBasePercent — ступень, которую добор поднимает до slotMainPercent
	// (на сайте её поднимут до скидки дня).
	slotBasePercent = 10
	// slotMinDays — минимальный остаток дней до срока: позиция должна успеть
	// продаться, иначе скидка в ТГ бессмысленна.
	slotMinDays = 2
	// Ёмкость добора — 0,75 ёмкости слота (при cap 10 — 7 позиций): набралось
	// меньше — добираем до этого числа (решение владельца 24.09.2026).
	slotFillNum = 3
	slotFillDen = 4
)

// slotFill — до скольких позиций добирается слот: 0,75 ёмкости с округлением
// вниз (cap 10 → 7). Нулевая ёмкость — добирать некуда.
func slotFill(capacity int) int {
	return capacity * slotFillNum / slotFillDen
}

// dayPlan — разобранный план дня (14:00): что публикуем и что применяем на сайте.
type dayPlan struct {
	// slot — позиции рассылки: им пишется ТГ-колонка, скидка сайта поднимется
	// в 16:00 (и только непроданным).
	slot []slotPosition
	// extra — лишние повышения, не влезшие в ёмкость: скидка сайта ставится
	// сразу, в рассылку они не идут.
	extra []slotPosition
}

// slotPosition — позиция плана дня: пара, скидка дня и причина для истории.
type slotPosition struct {
	pair    PairState
	percent int16
	reason  string // причина скидки: discounts.Reason*
	// initialQty/planQty — контроль вечернего подъёма (16:00): остаток пары на
	// момент плана и план продаж по ней. Заполнены только у добора из избытка.
	initialQty *int64
	planQty    *int64
	// writeTelegram — ставить ли значение в ТГ-колонку: у ручной скидки оно
	// уже на сайте, писать её в ТГ незачем.
	writeTelegram bool
}

// RunSlotPlan — план дня (14:00): применить сегодняшние повышения, опубликовать
// слот в чат склада и отметить маркеры дня.
//
// Раскладка применения: позиции слота получают только ТГ-колонку (скидка сайта
// ждёт 16:00 — подписчики узнают раньше), лишние повышения получают скидку сайта
// сразу и уведомление об изменении в общий канал. Ручные скидки дня в слот идут
// как есть: сайт их уже несёт. Позиции предыдущей рассылки не повторяем
// (антидубль по лоту). Пустой слот — молчание: сообщение «позиций нет» ничего
// не сообщает, а дайджест 09:00 про это уже сказал.
//
// Шаг идемпотентен: перед работой проверяется маркер дня (флаг стоит — шаг
// пропускается), после успеха ставятся FlagPlan и FlagExpiry: этим же шагом
// применяется лестница по сроку, которую в ТГ-дни утренний пересчёт не пишет.
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
	// Оборот обязателен: этим же расчётом заменяется снапшот реестра — без
	// оборота из него пропали бы избыточные пары, и страница с отчётом показала
	// бы пустую очередь.
	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return fmt.Errorf("оборот товаров плана дня: %w", err)
	}
	pairs := Evaluate(inputs, rates, day)

	prev, err := uc.repo.LastDigestPairs(ctx)
	if err != nil {
		return err
	}
	plan := buildDayPlan(pairs, prev, capacity)

	// Применение плана: слот — ТГ-колонка, лишние — скидка сайта сразу.
	writes := dayPlanWrites(plan)
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("план дня: %w", err)
		}
	}
	applyWrites(pairs, writes)
	// Уведомления: у лишних меняется скидка канала сайта — человеку надо её
	// поставить. У слота меняется только ТГ-колонка, эффективная скидка сайта та
	// же, поэтому изменений реестр не видит и уведомлений по ним нет.
	uc.notifyChanges(ctx, uc.replaceRegistry(pairs, now))

	if err := uc.writeSlot(ctx, pairs, plan, now); err != nil {
		return err
	}

	if err := uc.repo.MarkDayFlag(ctx, day, discounts.FlagExpiry); err != nil {
		return fmt.Errorf("маркер дня пересчёта по сроку: %w", err)
	}
	if err := uc.repo.MarkDayFlag(ctx, day, discounts.FlagPlan); err != nil {
		return err
	}
	if len(plan.slot) == 0 {
		slog.Info("discounts: план ТГ-слота пуст — рассылка не отправлена")
	}
	return nil
}

// buildDayPlan — план дня из состояний пар: ручные скидки дня и сегодняшние
// повышения по сроку в порядке приоритета отчёта (ручные → срок ↑) идут в слот
// до заполнения ёмкости, не влезшие повышения — в лишние (скидка сайта сразу).
// Недобранный слот добирается (см. pickFill).
func buildDayPlan(pairs []PairState, prev map[discounts.LotKey]struct{}, capacity int) dayPlan {
	manual := make([]slotPosition, 0, capacity)
	raises := make([]slotPosition, 0, capacity)
	for _, p := range pairs {
		if !slotEligible(p, prev) {
			continue
		}
		switch {
		case p.Manual != nil && *p.Manual >= slotMainPercent:
			// Ручная скидка дня: значение уже стоит на сайте, в рассылке — как
			// приглашение продавать по ней.
			manual = append(manual, slotPosition{
				pair:    p,
				percent: *p.Manual,
				reason:  discounts.ReasonManual,
			})
		case p.Manual == nil && p.Expiry != nil && *p.Expiry > appliedTop(p):
			// Повышение по сроку: сегодня сайт получит эту ступень — сразу, если
			// позиция в ёмкость не влезла, и в 16:00, если попала в рассылку.
			// Пары с ручной скидкой сюда не идут: значение менеджера важнее, и
			// автоматика его не поднимает (ни в слот, ни в лишние).
			raises = append(raises, slotPosition{
				pair:          p,
				percent:       *p.Expiry,
				reason:        discounts.ReasonExpiry,
				writeTelegram: true,
			})
		default:
			// Ни ручной скидки дня, ни сегодняшнего повышения: паре в плане
			// делать нечего — скидка уже стоит на сайте либо её нет вовсе.
		}
	}
	sortSlot(manual)
	sortSlot(raises)

	// Ручные скидки приоритетнее: они идут в слот первыми, лишние ручные просто
	// не публикуем — их значение уже на сайте, терять нечего.
	slot := manual
	if capacity < len(slot) {
		slot = slot[:capacity]
	}
	var extra []slotPosition
	for _, r := range raises {
		if len(slot) < capacity {
			slot = append(slot, r)
			continue
		}
		extra = append(extra, r)
	}

	fill := slotFill(capacity)
	if len(slot) < fill {
		slot = append(slot, pickFill(pairs, slot, prev, fill-len(slot))...)
	}
	return dayPlan{slot: slot, extra: extra}
}

// pickFill — добор недобранного слота: сначала ступени ровно 10 % по сроку (их
// план — 20 %), затем пары избытка — по строкам раскладки объёма продаж.
//
// Каждая пара раскладки занимает своё место в ёмкости (решение владельца
// 24.09.2026): из одного товара в рассылку попадают несколько сроков — и сколько
// по каждому надо продать, чтобы коэффициент группы стал < 1. Скидка 20 % и
// печать — только тем парам, которые в добор попали.
func pickFill(pairs []PairState, slot []slotPosition, prev map[discounts.LotKey]struct{}, limit int) []slotPosition {
	if limit <= 0 {
		return nil
	}
	used := make(map[discounts.LotKey]struct{}, len(slot))
	for _, s := range slot {
		used[s.pair.Key] = struct{}{}
	}

	out := make([]slotPosition, 0, limit)
	// 1) Ступень ровно 10 % по сроку: добор поднимает её до скидки дня.
	base := make([]slotPosition, 0, limit)
	for _, p := range pairs {
		if _, ok := used[p.Key]; ok {
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
		base = append(base, slotPosition{
			pair:          p,
			percent:       slotMainPercent,
			reason:        discounts.ReasonExpiry,
			writeTelegram: true,
		})
	}
	sortSlot(base)
	if len(base) > limit {
		base = base[:limit]
	}
	out = append(out, base...)
	if len(out) >= limit {
		return out
	}

	// 2) Избыток: пары раскладки объёма продаж (FIFO), каждая — своя позиция.
	for _, sale := range surplusFills(pairs) {
		if len(out) >= limit {
			break
		}
		if _, ok := used[sale.pair.Key]; ok {
			continue
		}
		if !slotEligible(sale.pair, prev) {
			continue
		}
		if sale.pair.Manual != nil {
			continue // ручная скидка важнее: движок такую пару избытком не трогает
		}
		initial := sale.pair.Qty
		planQty := sale.qty
		out = append(out, slotPosition{
			pair:          sale.pair,
			percent:       slotMainPercent,
			reason:        discounts.ReasonSurplus,
			initialQty:    &initial,
			planQty:       &planQty,
			writeTelegram: true,
		})
	}
	return out
}

// surplusFill — пара раскладки добора: сколько её остатка надо продать.
type surplusFill struct {
	pair PairState
	qty  int64
}

// surplusFills — позиции добора из избытка по всем товарам выборки: блоки пар
// одного товара (Evaluate отдаёт их подряд, по возрастанию срока — FIFO) →
// объём продаж группы и его раскладка по парам. Товары без избытка и группы с
// нулевым объёмом раскладки не дают.
func surplusFills(pairs []PairState) []surplusFill {
	var out []surplusFill
	for start := 0; start < len(pairs); {
		end := start
		for end < len(pairs) && pairs[end].ProductID == pairs[start].ProductID {
			end++
		}
		block := pairs[start:end]
		if _, sales := SurplusSalePlan(block); len(sales) > 0 {
			byKey := make(map[discounts.LotKey]PairState, len(block))
			for _, p := range block {
				byKey[p.Key] = p
			}
			for _, sale := range sales {
				p, ok := byKey[sale.Key]
				if !ok {
					continue
				}
				out = append(out, surplusFill{pair: p, qty: sale.Qty})
			}
		}
		start = end
	}
	return out
}

// dayPlanWrites — правки плана дня: позициям слота — ТГ-колонка (сайт не
// трогаем), лишним — скидка канала сайта сразу. Ручные скидки слотов не трогаем
// вовсе: их значение уже на сайте.
func dayPlanWrites(plan dayPlan) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(plan.slot)+len(plan.extra))
	for _, s := range plan.slot {
		if !s.writeTelegram {
			continue
		}
		if discountPercent(s.pair.TelegramPlain) == s.percent {
			continue
		}
		percent := s.percent
		w := writeFor(s.pair)
		w.Telegram = &percent
		writes = append(writes, w)
	}
	for _, s := range plan.extra {
		percent := s.percent
		w := writeFor(s.pair)
		w.General = &percent
		w.Source = s.reason
		writes = append(writes, w)
	}
	return writes
}

// RunRaise — подъём скидки сайта по позициям сегодняшней ОТПРАВЛЕННОЙ рассылки
// (16:00) — тем, что к этому часу ещё не проданы.
//
// Распроданные позиции пропускаем (поднимать нечего), ручные — не трогаем:
// значение менеджера важнее плана. Текущее больше цели — тоже пропуск: понижать
// автоматика не умеет.
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

	raised, writes := raiseWrites(pairs, plan)
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("подъём скидок: %w", err)
		}
		// Снапшот реестра должен нести УЖЕ поднятое значение: иначе
		// уведомление о подъёме не уйдёт, а на следующем часу придёт
		// ложное «поднимите скидку» (расчёт перечитает БД и увидит рост).
		applyWrites(pairs, writes)
	}

	if len(raised) > 0 {
		if err := uc.repo.MarkGeneralRaised(ctx, raised, now); err != nil {
			return err
		}
	}

	// Подъём меняет значение канала сайта — изменения уходят уведомлениями в
	// общий канал (тот же путь, что у расчётного тика).
	uc.notifyChanges(ctx, uc.replaceRegistry(pairs, now))

	if err := uc.repo.MarkDayFlag(ctx, day, discounts.FlagRaise); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("discounts: подъём general по %d позициям слота", len(raised)))
	return nil
}

// raiseWrites — правки подъёма 16:00: по непроданным позициям плана слота скидка
// сайта поднимается до максимума плана и ТЕКУЩЕЙ ТГ-колонки (менеджер мог поднять
// её руками после 14:00 — решения владельца 14.09 и 24.09.2026). Ручная скидка
// важнее плана (в том числе 0 % — заморозка пары), понижений автоматика не делает.
//
// Что значит «не продано», решает контроль позиции (SlotItem): у добора из
// избытка план продаж по паре — пара продана, если её остаток опустился до
// initialQty−planQty или ниже; у сроковых позиций контроль проще — пара не должна
// обнулиться, а распроданной пары в снапшоте остатков уже нет. Пары правятся по
// индексу, а не по копии: в реестр должен уйти снапшот с поднятым значением —
// иначе уведомление о подъёме не уйдёт, а на следующем часу придёт ложное
// «поднять скидку» (расчёт перечитает БД и увидит рост).
func raiseWrites(pairs []PairState, plan []discounts.SlotItem) ([]discounts.LotKey, []discounts.DiscountWrite) {
	if len(plan) == 0 {
		return nil, nil
	}
	byKey := make(map[discounts.LotKey]int, len(pairs))
	for i := range pairs {
		byKey[pairs[i].Key] = i
	}

	raised := make([]discounts.LotKey, 0, len(plan))
	writes := make([]discounts.DiscountWrite, 0, len(plan))
	for _, item := range plan {
		idx, ok := byKey[item.LotKey]
		if !ok {
			continue // пара распродана: остатка нет, поднимать нечего
		}
		p := &pairs[idx]
		if soldOut(item, *p) {
			continue // план продаж выполнен — скидка на сайте не нужна
		}
		if p.Manual != nil {
			continue // ручная скидка на сайте важнее плана (в том числе 0 % — заморозка)
		}
		target := item.Percent
		if p.TelegramPlain != nil && *p.TelegramPlain > target {
			target = *p.TelegramPlain
		}
		if discountPercent(p.Applied) >= target {
			continue // уже не ниже цели: не понижаем
		}
		general := target
		p.AppliedPlain = &general
		p.SourceRaw = item.Reason
		raised = append(raised, p.Key)
		writes = append(writes, writeFor(*p))
	}
	return raised, writes
}

// soldOut — позиция добора уже отработана: её остаток опустился до контрольного
// значения (initialQty−planQty) или ниже, значит запланированный объём продан и
// поднимать скидку не за чем. У позиций без плана продаж (сроковые) контроля
// здесь нет: их пары проверяются тем, что остались в снапшоте остатков.
func soldOut(item discounts.SlotItem, p PairState) bool {
	if item.InitialQty == nil || item.PlanQty == nil {
		return false
	}
	return p.Qty <= *item.InitialQty-*item.PlanQty
}

// writeSlot — сохранить план рассылки и отправить сообщение складу. История —
// позиции плана (по ним работает подъём 16:00 и антидубль следующей рассылки);
// текст сообщения — всё окно активных скидок с метками канала: склад должен
// видеть, что уходит в канал ((ТГ)), а что просто стоит на сайте
// ((Срок)/(Избыток)/(Ручная)) — решение владельца 24.09.2026. Пустой слот —
// молчание, сообщение «позиций нет» ничего не сообщает.
func (uc *UseCase) writeSlot(ctx context.Context, pairs []PairState, plan dayPlan, now time.Time) error {
	if len(plan.slot) == 0 {
		return nil
	}

	items := make([]discounts.DigestItem, 0, len(plan.slot))
	for _, s := range plan.slot {
		items = append(items, discounts.DigestItem{
			ProductID:  s.pair.ProductID,
			BestBefore: s.pair.BestBefore,
			Percent:    s.percent,
			Reason:     s.reason,
			InitialQty: s.initialQty,
			PlanQty:    s.planQty,
		})
	}

	record := discounts.DigestRecord{PlannedAt: beginningOfDay(now), ChatKind: discounts.ChatWarehouse}
	if err := uc.repo.SaveDigest(ctx, record, items); err != nil {
		return err
	}

	digest := discounts.BuildDigest(uc.windowRows(pairs, plan.slot), uc.WindowCap())
	digest.Date = now
	text := digest.Text()
	if uc.warehouse != nil {
		if err := uc.warehouse.NotifyWarehouse(text); err != nil {
			return err
		}
	} else {
		slog.Info(fmt.Sprintf("discounts: план слота (канал склада не подключён): %s", text))
	}

	// Маркер отправки — только после успешной отправки: иначе подъём 16:00
	// считал бы несобранную рассылку отправленной.
	return uc.repo.MarkDigestSent(ctx, discounts.ChatWarehouse, beginningOfDay(now), now)
}

// windowRows — строки сообщения складу: активные пары окна (как в дайджесте), а
// позициям плана дня подставляется скидка дня (у добора из избытка она выше
// расчётной ступени) и план продаж; метка канала — по признаку записи в
// ТГ-колонку (позиция плана, которой ТГ-колонку не пишут — ручная, — остаётся
// со своим источником).
func (uc *UseCase) windowRows(pairs []PairState, slot []slotPosition) []discounts.Row {
	byKey := make(map[discounts.LotKey]slotPosition, len(slot))
	for _, s := range slot {
		byKey[s.pair.Key] = s
	}

	rows := make([]discounts.Row, 0, len(pairs))
	for _, p := range pairs {
		percent, _ := p.Desired()
		if percent == nil || *percent <= 0 {
			continue
		}
		row := p.Row()
		if s, ok := byKey[p.Key]; ok {
			row.Percent = s.percent
			row.Telegram = row.Telegram || s.writeTelegram
			if s.planQty != nil {
				row.Qty = *s.planQty
			}
		}
		rows = append(rows, row)
	}

	return rows
}

// replaceRegistry — положить в реестр свежий снапшот и вернуть изменения
// эффективной скидки канала сайта (уведомления). Товары, оставшиеся без скидки,
// тоже проходят через реестр — их исчезновение даёт уведомление «убрать».
func (uc *UseCase) replaceRegistry(pairs []PairState, _ time.Time) []Change {
	return uc.reg.Replace(pairs)
}

// slotEligible — пара вообще может попасть в план: остаток дней до срока не
// меньше двух (позиция успевает продаться), пару не заморозили ручным нулём
// (0 % — лестница по ней не работает, решение владельца, сентябрь 2026) и была
// ли она в прошлой рассылке.
func slotEligible(p PairState, prev map[discounts.LotKey]struct{}) bool {
	if p.Frozen() {
		return false
	}
	if p.DaysLeft < slotMinDays {
		return false
	}
	if _, ok := prev[p.Key]; ok {
		return false
	}
	return true
}

// sortSlot — порядок плана: тот же порядок строк, что в отчёте (правило —
// `discounts.Less`), чтобы список читался как дайджест.
func sortSlot(slot []slotPosition) {
	rows := make([]discounts.Row, len(slot))
	for i, s := range slot {
		rows[i] = s.pair.Row()
	}
	order := make([]int, len(slot))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return discounts.Compare(rows[order[a]], rows[order[b]])
	})
	sorted := make([]slotPosition, len(slot))
	for i, idx := range order {
		sorted[i] = slot[idx]
	}
	copy(slot, sorted)
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

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
// подписчику ничего не даёт. Исключение — ручная ТГ-скидка (> 0): менеджер
// поставил её руками именно для ТГ, поэтому пара идёт в рассылку принудительно
// своим значением (решение владельца 19.10.2026, см. buildDayPlan). Свежая
// ступень ровно 10 % идёт в рассылку только через добор — под скидку дня (20 %),
// как есть она подписчику тоже ничего не даёт (решение владельца 29.09.2026,
// см. buildDayPlan).

// Ёмкость слота и пороги отбора (решение владельца, §13 черновика).
const (
	// slotMainPercent — скидка дня, с которой позиция идёт в план сразу.
	slotMainPercent = 20
	// slotBasePercent — ступень, которую добор поднимает до slotMainPercent
	// (на сайте её поднимут до скидки дня). Свежая такая ступень (сайт без
	// скидки) местом в слоте не пользуется — она сама идёт в добор.
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
	// writeTelegram — ставить ли значение в ТГ-колонку: у ручной скидки сайта оно
	// уже на сайте, у ручной ТГ — уже в ТГ-колонке, писать нечего.
	writeTelegram bool
	// tgManual — значение позиции взято из ручной ТГ-скидки: пара в рассылке
	// стоит своим ТГ-значением, метка канала в сообщении — (ТГ).
	tgManual bool
}

// buildDayPlan — план дня из состояний пар: ручные скидки дня и сегодняшние
// повышения по сроку в порядке приоритета отчёта (ручные → срок ↑) идут в слот
// до заполнения ёмкости, не влезшие повышения — в лишние (скидка сайта сразу).
// Недобранный слот добирается (см. pickFill).
//
// Ручная ТГ-скидка (> 0) идёт в рассылку ПРИНУДИТЕЛЬНО и своим значением
// (решение владельца 19.10.2026): её ставит менеджер именно для ТГ-канала, и
// подписчик должен увидеть ровно её. Такие позиции не снимаются ни ёмкостью
// слота, ни антидублем прошлой рассылки, ни порогом «успеет продаться» —
// «принудительно» и значит принудительно. Остальное (заморозка ручным нулём
// сайта) важнее: движок такую пару не трогает вовсе.
//
// Свежая ступень ровно 10 % (сайт без скидки) места в слоте не занимает: она
// сама — материал добора, в рассылке такая пара идёт под скидку дня (20 %), а не
// под свою ступень (решение владельца 29.09.2026): 10 % сайт получит и так, как
// есть подписчику оно ничего не даёт. Не дошёл добор до пары (слот уже набран до
// fill) — скидка сайта ставится сразу её ступенью, как у лишних повышений.
func buildDayPlan(pairs []PairState, prev map[discounts.LotKey]struct{}, capacity int) dayPlan {
	// forced — позиции ручной ТГ-скидки: идут в слот первыми и не вытесняются.
	forced := make([]slotPosition, 0, capacity)
	manual := make([]slotPosition, 0, capacity)
	raises := make([]slotPosition, 0, capacity)
	// weak — пары со СВЕЖЕЙ ступенью ровно 10 % (site 0 → 10): материал добора,
	// местом в слоте они не пользуются.
	weak := make([]PairState, 0, capacity)
	// prevSeen — пары, бывшие в прошлой рассылке: повторно не публикуем, но
	// свежую ступень дня отдаём сайту (см. ниже).
	prevSeen := make([]PairState, 0, capacity)
	for _, p := range pairs {
		if p.Frozen() {
			continue // ручной ноль сайта замораживает пару целиком
		}
		if p.TelegramActive() {
			// Ручная ТГ важнее расчётного кандидата: в рассылке пара стоит
			// своим ТГ-значением (оно уже в ТГ-колонке — писать нечего).
			forced = append(forced, slotPosition{
				pair:     p,
				percent:  *p.TelegramManual,
				reason:   discounts.ReasonManual,
				tgManual: true,
			})
			continue
		}
		if _, seen := prev[p.Key]; seen {
			// Пара была в прошлой рассылке: в слот её не берём (антидубль), но
			// ступень дня не теряем — она уходит на сайт сразу, как у «лишних»
			// (решение владельца 20.10.2026). До этого такая пара выпадала из
			// плана целиком, и лесенка в ТГ-день по ней не срабатывала вовсе.
			prevSeen = append(prevSeen, p)
			continue
		}
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
			if *p.Expiry == slotBasePercent {
				weak = append(weak, p)
				continue
			}
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
	sortSlot(forced)
	sortSlot(manual)
	sortSlot(raises)

	// Ручная ТГ — первой и целиком: ёмкость её не режет (владелец поставил
	// скидку руками, значит позиция должна быть в рассылке). Ручные сайта идут
	// следом в пределах остатка ёмкости, лишние просто не публикуем — их
	// значение уже на сайте, терять нечего.
	slot := make([]slotPosition, 0, len(forced)+len(manual)+len(raises))
	slot = append(slot, forced...)
	if room := capacity - len(forced); room > 0 {
		if len(manual) > room {
			manual = manual[:room]
		}
		slot = append(slot, manual...)
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
	// Свежая 10 %-я ступень, до которой добор не дошёл, получает скидку сайта
	// сразу — своей ступенью. Без этой ветки пара осталась бы без скидки вовсе:
	// в ТГ-дни утренний пересчёт ступень не пишет, а следующая возможность —
	// ближайший КТ-день.
	for _, p := range weak {
		if slotHasPair(slot, p.Key) {
			continue
		}
		percent := *p.Expiry
		extra = append(extra, slotPosition{
			pair:    p,
			percent: percent,
			reason:  discounts.ReasonExpiry,
		})
	}
	// Ступень дня по паре из прошлой рассылки: в рассылке её нет, значит сайт
	// должен получить повышение сегодня, а не в 16:00 и не в следующий КТ-день.
	for _, p := range prevSeen {
		if p.Manual != nil || p.Expiry == nil || *p.Expiry <= appliedTop(p) {
			continue // сегодняшнего повышения по сроку нет — писать нечего
		}
		extra = append(extra, slotPosition{
			pair:    p,
			percent: *p.Expiry,
			reason:  discounts.ReasonExpiry,
		})
	}

	return dayPlan{slot: slot, extra: extra}
}

// slotHasPair — пара уже попала в позиции слота (её мог забрать добор).
func slotHasPair(slot []slotPosition, key discounts.LotKey) bool {
	for i := range slot {
		if slot[i].pair.Key == key {
			return true
		}
	}
	return false
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

// slotWrites — ТГ-колонка позиций слота: её пишет план, сайт остаётся как есть до
// подъёма 16:00.
func slotWrites(plan dayPlan) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(plan.slot))
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
		// Владелец ТГ-значения — ТГ-день: выход эскалации снимает и колонку
		// сайта, поднятую под 16:00 (решение владельца 02.10.2026).
		w.TelegramOwner = discounts.OwnerEscalation.String()
		writes = append(writes, w)
	}

	return writes
}

// extraWrites — скидка канала сайта «лишним» позициям: они не влезли в слот, и
// сайт получает их повышение сразу. Живое письмо пишет их один раз — на
// финальной сборке 14:45.
func extraWrites(plan dayPlan) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(plan.extra))
	for _, s := range plan.extra {
		percent := s.percent
		w := writeFor(s.pair)
		w.General = &percent
		w.Source = s.reason
		// Лишние позиции ставят скидку канала сайта сразу: ступень по сроку —
		// срок, избыток — движок, ручная — человек.
		w.GeneralOwner = ownerForReason(s.reason).String()
		writes = append(writes, w)
	}

	return writes
}

// ownerForReason — владелец значения по причине позиции плана: ступень по сроку —
// срок, избыток — движок, ручная — человек. Неизвестная причина → владельца нет:
// такое значение расчёт не понижает.
func ownerForReason(reason string) discounts.Owner {
	switch reason {
	case discounts.ReasonExpiry:
		return discounts.OwnerExpiry
	case discounts.ReasonSurplus:
		return discounts.OwnerSurplus
	case discounts.ReasonManual:
		return discounts.OwnerManual
	}

	return discounts.OwnerNone
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
// Пару с ручной ТГ-скидкой подъём поднимает и вне отправленной рассылки: её
// поставили уже после составления списка (решение владельца 29.09.2026).
//
// Что значит «не продано», решает контроль позиции (SlotItem): у добора из
// избытка план продаж по паре — пара продана, если её остаток опустился до
// initialQty−planQty или ниже; у сроковых позиций контроль проще — пара не должна
// обнулиться, а распроданной пары в снапшоте остатков уже нет. Пары правятся по
// индексу, а не по копии: в реестр должен уйти снапшот с поднятым значением —
// иначе уведомление о подъёме не уйдёт, а на следующем часу придёт ложное
// «поднять скидку» (расчёт перечитает БД и увидит рост).
func raiseWrites(pairs []PairState, plan []discounts.SlotItem) ([]discounts.LotKey, []discounts.DiscountWrite) {
	byKey := make(map[discounts.LotKey]int, len(pairs))
	for i := range pairs {
		byKey[pairs[i].Key] = i
	}
	inPlan := make(map[discounts.LotKey]struct{}, len(plan))
	for _, item := range plan {
		inPlan[item.LotKey] = struct{}{}
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
		// Цель — эффективная скидка ТГ-колонки: ручная ТГ важнее плана (её ставит
		// менеджер, а не движок — решение владельца 29.09.2026).
		owner := discounts.OwnerEscalation
		// Пара БЕЗ плана продаж (сроковая ступень, добор без избытка): подъём
		// ПЕРЕСТАВЛЯЕТ её ступень по сроку, а не ставит «эскалацию» ТГ-дня
		// (решение владельца 06.10.2026). Иначе значение гаснет первым же
		// часовым пересчётом: escalationOver для пары без избытка = true
		// (recalc.go), и выход снимает его до расчётной ступени. Прод
		// 06.10.2026: Монреальская смесь специй получила 20 % добором в
		// рассылку и через час стояла на 10 % («Понизить скидку до 10 %»).
		// Пара ДОБОРА ИЗ ИЗБЫТКА (есть план продаж) остаётся «эскалацией»: по
		// этому владельцу работает снятие, когда план выполнен.
		if item.InitialQty == nil {
			owner = discounts.OwnerExpiry
		}
		if tg := p.telegramValue(); tg != nil && *tg > target {
			target = *tg
		}
		// Число назначил человек, а не план дня: пара держится за ручную ТГ,
		// и выход эскалации её не понижает (решение владельца 02.10.2026).
		// Ручная ТГ может и СОВПАДАТЬ с планом: позиция рассылки теперь может
		// стоять ровно её значением (принудительная рассылка, 19.10.2026), и
		// владельцем такого значения остаётся человек, а не срок.
		if tm := p.TelegramManual; tm != nil && *tm > 0 && *tm >= target {
			owner = discounts.OwnerManual
		}
		if discountPercent(p.Applied) >= target {
			continue // уже не ниже цели: не понижаем
		}
		general := target
		p.AppliedPlain = &general
		p.SourceRaw = item.Reason
		p.GeneralOwner = owner
		raised = append(raised, p.Key)
		writes = append(writes, writeFor(*p))
	}

	// Ручная ТГ вне отправленной рассылки: её поставили уже ПОСЛЕ составления
	// списка, поэтому в плане дня её нет — но подъём обязан довести её до сайта
	// теми же правилами (решение владельца 29.09.2026). Распродано и заморожено
	// ручным нулём не поднимаем, как и плановые пары.
	for i := range pairs {
		p := &pairs[i]
		if !p.TelegramActive() {
			continue
		}
		if _, ok := inPlan[p.Key]; ok {
			continue // плановые подняты выше своим кругом
		}
		if p.Manual != nil {
			continue // ручная скидка сайта важнее (в том числе 0 % — заморозка)
		}
		target := *p.TelegramManual
		if discountPercent(p.Applied) >= target {
			continue // уже не ниже цели: не понижаем
		}
		general := target
		p.AppliedPlain = &general
		p.SourceRaw = discounts.ReasonManual
		// Число поставил человек — владелец значения тоже он: движок понизит
		// его только когда ручная ТГ будет снята.
		p.GeneralOwner = discounts.OwnerManual
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

// windowRows — строки сообщения складу: активные пары окна (как в дайджесте), а
// позициям плана дня подставляется скидка дня (у добора из избытка она выше) и
// план продаж; метка канала — по признаку записи в ТГ-колонку (позиция плана,
// которой ТГ-колонку не пишут — ручная сайта, — остаётся со своим источником).
//
// Позиция плана печатается всегда, даже если расчётного кандидата у пары нет:
// ручная ТГ-скидка идёт в рассылку принудительно, и складу надо её увидеть (метка
// (ТГ), значение — из ТГ-колонки).
func (uc *UseCase) windowRows(pairs []PairState, slot []slotPosition) []discounts.Row {
	byKey := make(map[discounts.LotKey]slotPosition, len(slot))
	for _, s := range slot {
		byKey[s.pair.Key] = s
	}

	rows := make([]discounts.Row, 0, len(pairs))
	for _, p := range pairs {
		s, inPlan := byKey[p.Key]
		percent, _ := p.Desired()
		if !inPlan && (percent == nil || *percent <= 0) {
			continue
		}
		row := p.Row()
		if inPlan {
			row.Percent = s.percent
			row.Telegram = row.Telegram || s.writeTelegram || s.tgManual
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
		// Владельцы едут вместе со значениями: без них расчёт не знает, что
		// снимать при уходе основания (решение владельца 02.10.2026).
		GeneralOwner:  p.GeneralOwner.String(),
		TelegramOwner: p.TelegramOwner.String(),
	}
}

// Расчётный цикл модуля: утренний пересмотр лестницы по сроку годности
// (вт/чт/сб, «только вверх»), часовой пересчёт избытка (RecalcSurplus) и
// пересчёт по событиям стока (RecalcAffected — приёмка, расформирование
// заказа, ручная правка: полное решение по затронутым товарам в ближайшую
// минуту, ступень по сроку — только на пустое место).
//
// Все шаги идут по одному снапшоту входа: состояния пар считает evaluate.go,
// значения пишет шов стока (DiscountWriter), снапшот расчёта и изменения
// эффективной скидки держит реестр (registry.go), про изменения людям сообщает
// notify.go. Своих часов и соединений пакет не заводит: часы — uc.now, данные —
// швы ports.go.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"warehouseHelper/internal/discounts"
)

// expiryDay — день пересмотра лестницы по сроку: вторник, четверг, суббота
// (КТ-дни склада). В остальные дни автоматика ступени не двигает.
func expiryDay(t time.Time) bool {
	wd := t.Weekday()
	return wd == time.Tuesday || wd == time.Thursday || wd == time.Saturday
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
	// ТГ-день (вт/чт): утром лестница только СЧИТАЕТСЯ — повышения этого дня
	// применяет план дня в 14:00 (решение владельца 24.09.2026): сайт должен
	// получить скидку слота не раньше, чем её увидят подписчики. Маркер дня
	// ставит он же (FlagExpiry), поэтому догон после сна не теряется.
	if isTelegramDay(now) {
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

	// Правки по сроку + снятие запрета менеджера (ручная 0): запрет обязан
	// убрать с сайта ступень, поставленную движком до него. Выход скидок по
	// владельцу значения — тот же общий шаг: утренний пересмотр идёт по всем
	// парам, и снятие не должно ждать часового тика (решение владельца
	// 02.10.2026: снимаем мгновенно).
	writes := append(expiryWrites(pairs), ownerWrites(pairs, uc.planQty(ctx, pairs))...)
	writes = append(writes, banWrites(pairs)...)

	if err := uc.writeAndRegister(ctx, pairs, writes); err != nil {
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
		maps.Copy(rates, fresh)
		pairs = Evaluate(inputs, rates, today)
	}

	if err := uc.writeAndRegister(ctx, pairs, ownerWrites(pairs, uc.planQty(ctx, pairs))); err != nil {
		return err
	}

	if err := uc.repo.MarkDayFlag(ctx, today, discounts.FlagSurplus); err != nil {
		return fmt.Errorf("маркер дня пересчёта избытка: %w", err)
	}
	return nil
}

// RecalcAffected — пересчёт по событиям стока: приёмка, расформирование
// заказа, ручная правка. Полное решение (лестница по сроку + избыток) только
// по затронутым товарам: расформированный лот вернулся в остатки, и скидка по
// сроку должна вернуться на табличку в ту же минуту, а не в ближайший КТ-день.
// Лестница по остальным товарам не трогается: вне КТ-дней она не пересматривается.
//
// Почему ступень по затронутым товарам пишется и вне КТ-дней (решения владельца
// 14.09.2026 и 28.09.2026): «вне КТ-дней ступень не пересматривается» — правило
// ПЛАНОВОГО пересмотра окна (RecalcExpiry): раз в день-два автоматика проходит
// по ВСЕМ товарам сразу и поднимает стоящие значения. Здесь пересматриваются
// только товары с событиями стока — приёмка подняла накопленный остаток,
// расформирование вернуло лот в остатки, человек поправил скидку руками, — и
// ступень им пишется ТОЛЬКО НА ПУСТОЕ МЕСТО (expiryWritesOnEmpty): вернуть
// скидку паре, которая осталась без неё, событие обязано, а поднять стоящее
// значение — нет, это работа планового пересмотра. Иначе подбор заказа,
// списавший часть остатка, тянул бы ступень за собой в любой день недели, и сайт
// получил бы её раньше плана. Правило КТ-дней этим не нарушается: остальное
// окно не тронуто, а строку товара события держит в порядке тот, кто его
// изменил. Понижений автоматика не делает ни в КТ-день, ни вне его.
//
// Ошибка Averages тик не выполняет (без оборота избытки выглядели бы снятыми,
// а это запись); ошибка RefreshCurrent тик не роняет — считаем по сохранённому
// обороту. Маркеры дня не трогаются: они про расписание, не про события.
// Оборот берём у Averages, а не только по метке: этим расчётом заменяется
// снапшот реестра, и без оборота из него выпали бы избыточные пары (пустая
// очередь на странице, «Позиции с избытком: нет» в дайджесте) — урок ревью
// 14.09 про шаги, заменяющие снапшот.
// events — товары события стока и признак роста по каждому (true — остаток
// вырос): рост даёт право заполнить пустое место ступенью по сроку вне КТ-дней.
func (uc *UseCase) RecalcAffected(ctx context.Context, now time.Time, events map[string]bool) error {
	// Точечный пересчёт сериализован: сюда приходят и тик расписания, и ручная
	// правка скидки (OnManualDiscountChanged) — оба пишут снапшот реестра.
	uc.recalcMu.Lock()
	defer uc.recalcMu.Unlock()

	today := beginningOfDay(now)

	// Товары события: affected — кому расчёт вправе писать вообще, grown — у кого
	// остаток вырос (только им событие ставит ступень по сроку).
	productIDs := make([]string, 0, len(events))
	affected := make(map[string]struct{}, len(events))
	grown := make(map[string]struct{}, len(events))
	for pid, grew := range events {
		productIDs = append(productIDs, pid)
		affected[pid] = struct{}{}
		if grew {
			grown[pid] = struct{}{}
		}
	}

	inputs, err := uc.loadInputs(ctx, today)
	if err != nil {
		return err
	}

	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return fmt.Errorf("оборот товаров расчёта: %w", err)
	}

	// Свежий оборот — только по товарам события: остальным хватает
	// сохранённого (свежие цифры по ним догонит часовой пересчёт).
	if fresh := uc.refreshTurnoverByIDs(ctx, productIDs); fresh != nil {
		maps.Copy(rates, fresh)
	}
	pairs := Evaluate(inputs, rates, today)

	writes := ownerWrites(pairs, uc.planQty(ctx, pairs))
	// Ступень по сроку — только у затронутых товаров, у которых событие ПРИБАВИЛО
	// остаток (MarkGrown: приёмка, возврат лота в остатки), только на пустое место
	// (решение владельца 28.09.2026) и только вне ТГ-дня: в ТГ-дни (вт/чт)
	// ступень двигают план дня (14:00) и подъём (16:00) — иначе событие стока
	// подняло бы скидку сайта раньше рассылки, и подписчики увидели бы не то, о
	// чём договорились (правило владельца 24.09.2026: до подъёма действуют старые
	// значения). Подбор остаток списывает — роста нет, и ступень за ним не идёт:
	// раньше он тоже заполнял пустое место, и кладовщик, подобравший заказ,
	// ставил скидку на сайте в любой день недели (жалоба владельца 30.09.2026:
	// среда, подбор соуса, «Поставить скидку 40%» без повода).
	if !isTelegramDay(now) {
		writes = append(writes, writesForProducts(expiryWritesOnEmpty(pairs), grown)...)
	}
	// Снятие по запрету менеджера — тоже только по товару события: ручную 0
	// ставят через страницу «Сроки», а её запись дёргает пересчёт товара.
	writes = append(writes, writesForProducts(banWrites(pairs), affected)...)

	return uc.writeAndRegister(ctx, pairs, writes)
}

// writesForProducts — правки только по указанным товарам: событие стока
// пересчитывает полное решение пары, но писать вправе лишь то, что касается
// товара события. Ступень по остальным товарам ставит плановый пересмотр.
func writesForProducts(writes []discounts.DiscountWrite, ids map[string]struct{}) []discounts.DiscountWrite {
	out := make([]discounts.DiscountWrite, 0, len(writes))
	for _, w := range writes {
		if _, ok := ids[w.ProductID]; !ok {
			continue
		}
		out = append(out, w)
	}
	return out
}

// expiryWrites — правки по сроку: только рост. Ступень идёт в правки, если она
// строго выше того, что уже стоит у пары (эффективной скидки канала и значения
// plain-колонки): равенство — уже применено, меньше — понижение, которого
// автоматика не делает. Ручная скидка тоже входит в «уже стоит», поэтому рост
// под ней поднимает только колонку движка, а на сайте остаётся ручная.
// Пара, замороженная ручным нулём (0 %), из правок выпадает целиком: лестница
// по ней не работает и plain не пишется — решение владельца, сентябрь 2026.
func expiryWrites(pairs []PairState) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(pairs))
	for _, p := range pairs {
		if p.Frozen() {
			continue // ручная 0 % — пара заморожена
		}
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
			// Ступень по сроку — её и значение: владелец нужен, чтобы расчёт
			// знал, что понижать нельзя (срок важнее избытка).
			GeneralOwner: discounts.OwnerExpiry.String(),
		})
	}
	return writes
}

// expiryWritesOnEmpty — ступень по сроку только на ПУСТОЕ место пары: скидки в
// канале сайта у неё нет вовсе (appliedTop == 0). Правки для пересчёта по
// событиям стока (решение владельца 28.09.2026, см. RecalcAffected): вернуть
// скидку паре, которая осталась без неё, событие обязано, а поднять стоящее
// значение — нет, это работа планового пересмотра. Остальные правила те же, что
// у expiryWrites: пара, замороженная ручным нулём, и пара без ступени на сегодня
// в правки не попадают.
func expiryWritesOnEmpty(pairs []PairState) []discounts.DiscountWrite {
	empty := make([]PairState, 0, len(pairs))
	for _, p := range pairs {
		if appliedTop(p) == 0 {
			empty = append(empty, p)
		}
	}
	return expiryWrites(empty)
}

// ownerWrites — правки расчёта по значениям, которые поставил движок (решение
// владельца 02.10.2026): вход скидки на пустое место и ВЫХОД по владельцу
// значения. Метка источника говорит «почему скидка», владелец
// (product_stock.discount_*_owner) — «кто поставил стоящее значение»; снятие
// работает по владельцу, а не по числу (раньше снималось только ровно 10 %).
//
//   - вход: избыток на пустом месте (места не занято, ручной и ступени нет) —
//     десятка с владельцем «избыток»;
//   - владелец «избыток»: избытка в группе больше нет (раскрытие на более
//     близкие сроки уже посчитано applySurplusGroup) — значение понижаем до
//     расчётного: ступень по сроку, иначе избыток, иначе снятие;
//   - владелец «эскалация» (ТГ-день): избыток ушёл ИЛИ выполнен план продаж
//     добора (заданный рассылкой) — то же понижение, плюс пустеет ТГ-колонка:
//     её значение — то же обещание рассылки, и без снятия оно всплывает в
//     отчёте меткой (ТГ);
//   - владелец «ступень по сроку» и «владельца нет» — не трогаем: срок важнее
//     избытка, а чужое/неизвестное значение понижать нельзя (строки до
//     появления колонок владельца).
//
// Ручная важнее нашего значения: уступая ей, движок освобождает своё место —
// кроме случая, когда то же значение держит ступень по сроку (тогда значение
// остаётся за сроком).
//
// Пара, замороженная ручным нулём (0 %), из работы движка исключена совсем:
// ни десятки, ни снятия по ней не пишем — решение владельца, сентябрь 2026.
func ownerWrites(pairs []PairState, plans map[discounts.LotKey]discounts.LotPlan) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(pairs))
	for _, p := range pairs {
		if p.Frozen() {
			continue // ручная 0 % — пара заморожена
		}
		if w, ok := surplusEntry(p, plans); ok {
			writes = append(writes, w)
			continue
		}
		if w, ok := ownerExit(p, plans); ok {
			writes = append(writes, w)
			continue
		}
		// ТГ-колонка живёт своей жизнью: её значение остаётся и тогда, когда
		// колонка сайта не наша или пуста (пара без скидки сайта, ручная сайта).
		// Чистим её по её собственному владельцу — иначе зависшая 20 % всплывает
		// в отчёте меткой (ТГ) (дефект 02.10.2026, ближний лот мясника 12.10).
		if p.TelegramPlain != nil && clearTelegram(p, plans) {
			w := writeFor(p)
			w.Telegram = nil
			writes = append(writes, w)
		}
	}
	return writes
}

// surplusEntry — вход скидки на избыток: место пары пустое (ни ручной, ни
// ступени по сроку, ни стоящего значения) и избыток жив. Правка — десятка с
// владельцем «избыток». ok = false — входа нет.
func surplusEntry(p PairState, plans map[discounts.LotKey]discounts.LotPlan) (discounts.DiscountWrite, bool) {
	if p.Manual != nil || p.Expiry != nil || !p.HasSurplus || appliedTop(p) != 0 {
		return discounts.DiscountWrite{}, false
	}

	percent := discounts.SurplusPercent()
	w := writeFor(p)
	w.General = &percent
	w.GeneralOwner = discounts.OwnerSurplus.String()
	w.Source = discounts.SourceSurplus.String()
	if clearTelegram(p, plans) {
		w.Telegram = nil
	}
	return w, true
}

// ownerExit — выход значения по его владельцу: правка есть, когда основание ушло.
// ok = false — значение стоит по делу либо оно не наше (ступень по сроку,
// владельца нет) — такие не понижаем.
func ownerExit(p PairState, plans map[discounts.LotKey]discounts.LotPlan) (discounts.DiscountWrite, bool) {
	if !valueGone(p, plans) {
		return discounts.DiscountWrite{}, false
	}

	percent, owner := basisValue(p)
	if p.Manual != nil && owner != discounts.OwnerExpiry {
		// Ручная важнее нашего значения — место освобождаем (своё значение
		// ручной стоит в своей колонке).
		percent, owner = nil, discounts.OwnerNone
	}
	w := valueWrite(p, percent, owner)
	if clearTelegram(p, plans) {
		w.Telegram = nil
	}
	return w, true
}

// valueGone — основание нашего значения ушло, пора снимать: владелец «избыток» —
// избытка в группе больше нет (раскрытие влево посчитано applySurplusGroup);
// владелец «эскалация» (ТГ-день) — рассылка не в силе (план добора выполнен или
// избыток ушёл) и пару не ведёт ручная ТГ; владелец «человек» — человек снял свою
// ручную ТГ. Ступень по сроку и значение без владельца не снимаем: срок важнее
// избытка, а чужое/неизвестное трогать опаснее, чем оставить как есть.
func valueGone(p PairState, plans map[discounts.LotKey]discounts.LotPlan) bool {
	switch p.GeneralOwner {
	case discounts.OwnerManual:
		return p.TelegramManual == nil
	case discounts.OwnerSurplus, discounts.OwnerEscalation:
		return !holdsValue(p, plans)
	case discounts.OwnerNone, discounts.OwnerExpiry:
		return false
	}
	return false // недостижимо: все владельцы перечислены выше
}

// holdsValue — значение стоит по делу: избыток жив (владелец «избыток») либо
// рассылка в силе (владелец «эскалация»: план добора не выполнен и пару не ведёт
// ручная ТГ). Живая ручная ТГ важнее выхода: пару ведёт человек, движок её скидку
// не понижает (решение владельца 02.10.2026) — иначе сайт падал бы ниже просьбы
// человека и поднимался заново на следующем ТГ-дне (мигание раз в день).
func holdsValue(p PairState, plans map[discounts.LotKey]discounts.LotPlan) bool {
	if p.Manual != nil {
		return false // ручная сайта перекрыла значение — своё место освобождаем
	}

	switch p.GeneralOwner {
	case discounts.OwnerSurplus:
		return p.HasSurplus
	case discounts.OwnerEscalation:
		return p.TelegramActive() || !escalationOver(p, plans)
	case discounts.OwnerNone, discounts.OwnerExpiry, discounts.OwnerManual:
		return false
	}
	return false
}

// basisValue — что паре положено по расчёту сегодня (кандидат без ручной:
// ступень по сроку → избыток → ничего) и кто владелец этого значения.
func basisValue(p PairState) (*int16, discounts.Owner) {
	switch {
	case p.Expiry != nil && *p.Expiry > 0:
		return copyDiscount(p.Expiry), discounts.OwnerExpiry
	case p.Surplus != nil && *p.Surplus > 0:
		return copyDiscount(p.Surplus), discounts.OwnerSurplus
	}
	return nil, discounts.OwnerNone
}

// valueWrite — правка колонки сайта: значение, метка источника и владелец едут
// вместе (снятие — nil и пустая метка). ТГ-колонку правка несёт как есть: её
// ведёт ТГ-день, обнулить её можно только явно (см. clearTelegram).
func valueWrite(p PairState, percent *int16, owner discounts.Owner) discounts.DiscountWrite {
	w := writeFor(p)
	w.General = percent
	w.GeneralOwner = owner.String()
	w.Source = sourceMark(owner)
	return w
}

// sourceMark — метка источника значения по его владельцу: у ступени по сроку и
// избытка владелец и метка совпадают по имени, остальным метку не пишем (снятие,
// значение человека, значение ТГ-дня — его метку несёт причина позиции плана).
func sourceMark(owner discounts.Owner) string {
	switch owner {
	case discounts.OwnerExpiry:
		return discounts.SourceExpiry.String()
	case discounts.OwnerSurplus:
		return discounts.SourceSurplus.String()
	case discounts.OwnerNone, discounts.OwnerEscalation, discounts.OwnerManual:
		return ""
	}
	return "" // недостижимо: все владельцы перечислены выше
}

// escalationOver — основание скидки, поставленной ТГ-днём, кончилось: избытка
// в группе больше нет либо выполнен план продаж добора (план задан рассылкой:
// остаток опустился до initial − plan). Плана по паре нет — смотрим избыток.
func escalationOver(p PairState, plans map[discounts.LotKey]discounts.LotPlan) bool {
	if !p.HasSurplus {
		return true
	}
	plan, ok := plans[p.Key]

	return ok && plan.Done(p.Qty)
}

// clearTelegram — ТГ-колонку чистим, когда её значение поставил ТГ-день, а
// основание кончилось: иначе зависшая скидка рассылки всплывёт в отчёте меткой
// (ТГ) при пустой колонке сайта (дефект 02.10.2026).
func clearTelegram(p PairState, plans map[discounts.LotKey]discounts.LotPlan) bool {
	return p.TelegramOwner == discounts.OwnerEscalation && escalationOver(p, plans)
}

// planQty — план продаж добора по парам (шов репозитория): нужен только выходу
// скидки, поставленной ТГ-днём, поэтому спрашиваем его, лишь когда такие пары
// есть. Ошибка запроса тик не роняет: без плана не сработает только выход «план
// выполнен» (снятие значения — запись, на неполных данных её не делаем), а
// выход по ушедшему избытку работает и без плана.
func (uc *UseCase) planQty(ctx context.Context, pairs []PairState) map[discounts.LotKey]discounts.LotPlan {
	if !hasEscalation(pairs) {
		return nil
	}
	plans, err := uc.repo.LastPlanQty(ctx)
	if err != nil {
		slog.Info(fmt.Sprintf("discounts: план продаж добора: %v", err))
		return nil
	}

	return plans
}

// hasEscalation — есть ли среди пар значения, поставленные ТГ-днём.
func hasEscalation(pairs []PairState) bool {
	for _, p := range pairs {
		if p.GeneralOwner == discounts.OwnerEscalation || p.TelegramOwner == discounts.OwnerEscalation {
			return true
		}
	}

	return false
}

// banWrites — снятие скидки с пар, накрытых запретом менеджера (ручная 0 на
// паре с не меньшим сроком): «0 блокирует все сроки дальше того, на который
// поставлен» (решение владельца, 15.09.2026). Ступень, поставленная движком до
// запрета, обязана уйти с сайта — единственное место, где автоматика снимает
// своё значение: это не понижение расчёта, а исполнение запрета человека.
// Колонку ТГ ведёт ТГ-день: её значение отдаём как есть.
func banWrites(pairs []PairState) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0, len(pairs))
	for _, p := range pairs {
		if !p.Frozen() || p.AppliedPlain == nil {
			continue
		}
		writes = append(writes, discounts.DiscountWrite{
			ProductID:  p.ProductID,
			BestBefore: p.BestBefore,
			General:    nil, // снятие: движок пишет NULL, а не 0
			Telegram:   p.TelegramPlain,
		})
	}
	return writes
}

// freshTurnover — свежий оборот по товарам, где решение может уйти: события
// стока с прошлого тика (MarkDirty) и пары с избытком сейчас. Пустой список —
// шва не касаемся (nil-карта: расчёт остаётся на сохранённом обороте).
func (uc *UseCase) freshTurnover(ctx context.Context, pairs []PairState) map[string]float64 {
	return uc.refreshTurnoverByIDs(ctx, uc.freshIDs(pairs))
}

// refreshTurnoverByIDs — свежий оборот по перечисленным товарам (пачкой).
// Пустой список — шва не касаемся (nil-карта). Ошибка шва — в лог и та же
// nil-карта: тик из-за недоступного МС пропускать нельзя, расчёт продолжается
// на сохранённом обороте (его уже дал Averages), свежий догонит ближайший тик.
func (uc *UseCase) refreshTurnoverByIDs(ctx context.Context, ids []string) map[string]float64 {
	if len(ids) == 0 {
		return nil
	}

	rates, err := uc.turnover.RefreshCurrent(ctx, ids)
	if err != nil {
		slog.Info(fmt.Sprintf("discounts: свежий оборот (%d товаров): %v", len(ids), err))
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
	dirty := uc.takeDirty()
	ids := make([]string, 0, len(dirty))
	seen := make(map[string]struct{}, len(dirty))
	// Метки событий стока: очередь сохраняет порядок карты (какой есть), рост
	// товара здесь не важен — свежий оборот нужен всем одинаково.
	for pid := range dirty {
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		ids = append(ids, pid)
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
// эффективная скидка (ручная перекрывает plain как есть, включая 0) и отдельно
// значение plain-колонки. 0 — скидки не стоит. Автоматика пишет только выше
// границы. У замороженной пары (ручная 0, PairState.Frozen) границу считать
// незачем: правок по ней нет вовсе.
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
// скидка канала — ручная (если она есть, включая 0), иначе записанное значение;
// снятое значение (nil) убирает и plain, и эффективную. Без этого реестр
// сравнивал бы новые значения со старыми из БД и не видел бы изменений.
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
		// Владельцы значений — часть состояния пары: следующие шаги того же тика
		// и снапшот реестра должны видеть, чьё значение стоит.
		pairs[i].GeneralOwner = discounts.ParseOwner(w.GeneralOwner)
		pairs[i].TelegramOwner = discounts.ParseOwner(w.TelegramOwner)
		// План ТГ-колонки — часть состояния пары: по нему строка помечается
		// каналом (ТГ) в сообщении складу, а 16:00 берёт его же целью подъёма.
		pairs[i].TelegramPlain = copyDiscount(w.Telegram)
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

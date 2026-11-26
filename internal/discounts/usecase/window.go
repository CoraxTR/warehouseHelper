// Окно ёмкости (site window) — граница канала сайта: колонка general
// (product_stock.discount_general) существует только у пар окна, не больше
// `APP_DISCOUNT_WINDOW_CAP` позиций по приоритету отчёта (по умолчанию 12).
//
// За окном значение живёт только как кандидат дня — «скидка в памяти» (решение
// владельца 19.11.2026): его видно в дайджесте и в `/скидки` (секция «Доступно
// для допродажи»), но на сайте, в таблице «Сроки» и в уведомлениях его нет.
// Менеджеры ставят на сайт ровно окно; остальное — устная скидка.
//
// Из этого следуют три правила записи, которые держит этот файл:
//   - значение пишется только паре окна (`windowedWrites`);
//   - пара ВОШЛА в окно — пустое место заполняется её кандидатом дня, каким бы
//     ни был день недели: скидка «зафиксирована» в день лесенки и ждала места
//     (решение владельца 19.11.2026, уведомление даёт тот же diff реестра);
//   - пара ВЫШЛА из окна — наше значение снимается (NULL): на сайте скидки вне
//     окна быть не должно.
//
// Вход считается по ПЕРЕХОДУ (окно прошлого прохода → окно этого), а не по
// «дырке»: пара, которая всегда была в окне и просто ещё не получила ступень
// (ТГ-день до подъёма 16:00), значения не получает — её публикует план дня.
// Иначе подъём сайта обгонял бы рассылку (правило 24.09.2026).
package usecase

import (
	"context"

	"warehouseHelper/internal/discounts"
)

// windowRows — строки, которые занимают место в окне: кандидат дня (ручная
// сайта, ручная ТГ, ступень по сроку, избыток), а если кандидата нет — стоящее
// значение пары (эскалация ТГ-дня, значение, чьё основание уже ушло). По этим
// строкам считаются окно страницы «Скидки», очередь допродажи, подсветка
// «Сроков» и граница записи канала сайта — правило приоритета одно
// (discounts.Sort), разойтись они не могут.
func windowRows(pairs []PairState) []discounts.Row {
	rows := make([]discounts.Row, 0, len(pairs))
	for _, p := range pairs {
		if percent, _ := p.windowResolve(); percent != nil && *percent > 0 {
			rows = append(rows, p.Row())
			continue
		}
		if p.Frozen() {
			continue // ручная 0 %: в окне и отчёте её нет
		}
		if row := p.AppliedRow(); row.Source != discounts.SourceNone && row.Percent > 0 {
			rows = append(rows, row)
		}
	}
	discounts.Sort(rows)

	return rows
}

// windowKeys — ключи пар окна ёмкости capacity: строки окна (не больше capacity
// позиций, постоянные скидки места не занимают и едут сверх окна), группа
// избытка раскрывается во все свои сроки. capacity <= 0 — окно не ограничено.
func windowKeys(pairs []PairState, capacity int) map[discounts.LotKey]struct{} {
	lots := activeLots(windowRows(pairs), capacity)
	keys := make(map[discounts.LotKey]struct{}, len(lots))
	for _, key := range lots {
		keys[key] = struct{}{}
	}

	return keys
}

// windowedWrites — правки расчёта, приведённые к окну ёмкости: единственная
// точка, где решается «что из расчётных правок увидит сайт».
//
//   - значение (General != nil) проходит только для пары окна; постоянная
//     скидка поставщика — исключение, она места в окне не занимает;
//   - снятие (General == nil) проходит всегда: убрать наше значение движок
//     вправе и за окном;
//   - пара, вошедшая в окно, получает своего кандидата (ступень по сроку →
//     избыток) на пустое место — вне зависимости от дня недели и роста остатка;
//     стоящее значение не поднимается: подъём — дело планового пересмотра;
//   - пара, вышедшая из окна, теряет наше значение.
//
// Первая правка по паре в батче главнее: запись, уже сделанная шагом расчёта
// (запрет менеджера, выход по владельцу, ступень по сроку), не переписывается.
func (uc *UseCase) windowedWrites(ctx context.Context, pairs []PairState, writes []discounts.DiscountWrite) []discounts.DiscountWrite {
	win := windowKeys(pairs, uc.WindowCap())
	prevWin, seeded := uc.windowState(ctx)
	// Вход разрешён — запоминаем окно этого прохода как «прошлое». Вход подавлен
	// (ТГ-день до финальной сборки 14:45) — НЕ запоминаем: иначе пары, которым
	// скидку не выдали, станут «уже в окне» и значения не получат никогда
	// (инцидент 26.11.2026: в плане 12 позиций, на сайте 9 значений — Фриголар
	// и Кресуд попали в сохранённое окно в подавленном проходе).
	entryOK := seeded && uc.entryAllowed(ctx)
	if entryOK || !seeded {
		uc.storeWindow(ctx, win)
	}

	seen := make(map[discounts.LotKey]struct{}, len(writes))
	out := make([]discounts.DiscountWrite, 0, len(writes))
	for _, w := range writes {
		key := lotKey(w.ProductID, w.BestBefore)
		if w.General != nil && !permanentWrite(w) {
			if _, ok := win[key]; !ok {
				continue // пара за окном: значение остаётся кандидатом, не записью
			}
		}
		out = append(out, w)
		seen[key] = struct{}{}
	}

	out = append(out, windowExitWrites(pairs, win, seen)...)
	// Вход в окно — пустое место заполняется кандидатом дня. На ТГ-дне (вт/чт)
	// сайт получает скидку не раньше рассылки: 14:00 план → 16:00 подъём
	// (решение владельца 24.09.2026). Поэтому вход на ТГ-дне здесь не пишется —
	// значения ставит план дня. 26.11.2026: без этого гейта утренний проход
	// четверга поднял сайт сразу — после перехода окна на счёт по товарам
	// «входящих» пар стало намного больше, и в актуальных оказались ступени,
	// которые должны были уйти подписчикам.
	if entryOK {
		out = append(out, windowEntryWrites(pairs, win, prevWin, seen)...)
	}

	return out
}

// permanentWrite — правка постоянной скидки поставщика: такие строки места в окне
// не занимают (discounts.Row.Permanent), поэтому фильтр окна их не режет.
func permanentWrite(w discounts.DiscountWrite) bool {
	return w.General != nil && w.Source == discounts.SourcePermanent.String()
}

// windowExitWrites — снятие нашего значения у пар, оставшихся за окном: на сайте
// скидки вне окна быть не должно. Снимаем только plain-колонку и только своё
// значение (владелец «срок», «избыток», «эскалация», «человек»): чужое и
// значение без владельца расчёт не понижает, как и везде.
func windowExitWrites(pairs []PairState, win, seen map[discounts.LotKey]struct{}) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0)
	for _, p := range pairs {
		if _, ok := win[p.Key]; ok {
			continue
		}
		if _, ok := seen[p.Key]; ok {
			continue
		}
		if p.AppliedPlain == nil || p.Permanent != nil || !ourOwner(p.GeneralOwner) {
			continue // снимать нечего либо значение не наше
		}
		w := writeFor(p)
		w.General = nil // снятие: движок пишет NULL, а не 0
		w.GeneralOwner = discounts.OwnerNone.String()
		w.Source = ""
		writes = append(writes, w)
		seen[p.Key] = struct{}{}
	}

	return writes
}

// ourOwner — значение поставлено расчётом (или перенесено расчётом с ручной ТГ):
// только такое движок вправе снять.
func ourOwner(owner discounts.Owner) bool {
	switch owner {
	case discounts.OwnerExpiry, discounts.OwnerSurplus, discounts.OwnerEscalation, discounts.OwnerManual:
		return true
	case discounts.OwnerNone, discounts.OwnerPermanent:
		return false
	}

	return false
}

// windowEntryWrites — пары, вошедшие в окно с прошлого прохода: пустое место
// заполняется кандидатом дня (ступень по сроку → избыток). День недели и рост
// остатка значения не имеют — скидка уже «зафиксирована» в день лесенки и ждала
// места в окне; как только место появилось, сайт получает её, а человек —
// уведомление из diff реестра.
func windowEntryWrites(pairs []PairState, win, prevWin, seen map[discounts.LotKey]struct{}) []discounts.DiscountWrite {
	writes := make([]discounts.DiscountWrite, 0)
	for _, p := range pairs {
		if _, ok := win[p.Key]; !ok {
			continue
		}
		// В окне и в прошлом проходе — не вход.
		if _, ok := prevWin[p.Key]; ok {
			continue
		}
		if _, ok := seen[p.Key]; ok {
			continue
		}
		if p.Frozen() || p.Manual != nil || appliedTop(p) != 0 {
			continue
		}
		percent, owner := basisValue(p)
		if percent == nil || *percent <= 0 {
			continue
		}
		writes = append(writes, valueWrite(p, percent, owner))
		seen[p.Key] = struct{}{}
	}

	return writes
}

// raisedWrites — сроки подъёма, которые действительно записаны батчем: пара за
// окном значения не получает, и маркер подъёма ей ставить незачем.
func raisedWrites(writes []discounts.DiscountWrite, keys []discounts.LotKey) []discounts.LotKey {
	written := make(map[discounts.LotKey]struct{}, len(writes))
	for _, w := range writes {
		if w.General != nil {
			written[lotKey(w.ProductID, w.BestBefore)] = struct{}{}
		}
	}
	out := make([]discounts.LotKey, 0, len(keys))
	for _, key := range keys {
		if _, ok := written[key]; ok {
			out = append(out, key)
		}
	}

	return out
}

// entryAllowed — писать ли вход в окно на этом проходе. Не-ТГ-день — пишем
// всегда. ТГ-день (вт/чт): до финальной сборки 14:45 сайт молчит — новый
// список ещё не применён, и скидка сайта обогнала бы рассылку (решение
// владельца 24.09.2026); после сборки список работает на дополнение окна в
// пределах ёмкости, как и в остальные дни (решение владельца 26.11.2026).
// Маркер дня, а не часы: шаг мог не выполниться, и тогда вход запрещён.
func (uc *UseCase) entryAllowed(ctx context.Context) bool {
	if !isTelegramDay(uc.Now()) {
		return true
	}

	if uc.repo == nil {
		return false // сказать нечего — молчим, как до сборки
	}

	done, err := uc.repo.DayFlagDone(ctx, beginningOfDay(uc.Now()), discounts.FlagExtras)

	return err == nil && done
}

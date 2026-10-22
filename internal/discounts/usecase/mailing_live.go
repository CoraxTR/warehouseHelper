// Живое письмо складу (решение владельца 18.10.2026): в 14:00 письмо — только
// прикидка. До 14:45 оно «живёт»: каждые 5 минут план дня пересобирается по
// текущим остаткам, и правится ТО ЖЕ сообщение (id в истории рассылки), а не
// шлётся новое. В 14:45 — финальная сборка: снимок блоков (block_qty/stock_qty),
// «лишние» позиции получают скидку сайта и уходят запросы фото (по одному на
// лот). С 14:45 до 15:00 состав не меняется: окно меняет только количества —
// продажа уменьшает блок, возврат возвращает, 0 убирает блок с возможностью
// возврата; избыток в эти 15 минут не пересчитывается.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"warehouseHelper/internal/discounts"
)

// mailingPass — какой проход живого письма выполняется: пересборка
// (14:05–14:40) или финальная сборка 14:45. Отдельный тип вместо bool: проход —
// состояние шага, а не «флажок поведения» (revive flag-parameter).
type mailingPass int

const (
	mailingRebuild mailingPass = iota
	mailingFreeze
)

const (
	// MailingSkipButton — кнопка под письмом: рассылки не будет, скидки уходят
	// на сайт сразу, позиции остаются доступными следующей рассылке.
	MailingSkipButton = "Пропустить рассылку"
	// MailingSkipCallback — данные кнопки (префикс ds: — модуль скидок).
	MailingSkipCallback = "ds:skip"
	// photoRedoPrefix — данные кнопки «Переделать фото» под подтверждением
	// принятого фото: фото пришло не то (размытое, не тот лот) — склад просит
	// новое, альбом собирается заново (решение владельца 22.10.2026).
	// Формат: ds:ph:<product_id>@<срок годности, 2006-01-02> — 53 байта, предел
	// callback_data 64.
	photoRedoPrefix = "ds:ph:"

	// mailingRefreshStep — такт пересборки письма до финальной сборки. Сетка
	// жёсткая, по часам: 14:05, 14:10 … 14:45; пропущенный такт не копится.
	mailingRefreshStep = 5 * time.Minute
	// mailingFreezeAfter — финальная сборка: 45 минут после плана.
	mailingFreezeAfter = 45 * time.Minute
	// mailingPublishHour — через час после плана рассылка публикуется (следующая
	// фича). После этого письмо не правится, кнопка пропуска не работает.
	mailingPublishHour = time.Hour
)

// lotData — данные карточки лота для пересборки текста письма: название, цена,
// единица измерения и адрес. Нужны и при пересборке (пары расчёта), и в окне
// количеств (снапшот остатков), поэтому собираются в общую карту.
type lotData struct {
	Name      string
	UOM       string
	SalePrice *int64
	SiteURL   string
	Qty       int64
}

// lotKey — ключ лота: срок нормализуется до дня, как в расчёте.
func lotKey(productID string, bestBefore time.Time) discounts.LotKey {
	return discounts.LotKey{ProductID: productID, BestBefore: beginningOfDay(bestBefore)}
}

// lotDataFromInputs — данные лотов из снапшота остатков.
func lotDataFromInputs(inputs []discounts.Input) map[discounts.LotKey]lotData {
	out := make(map[discounts.LotKey]lotData, len(inputs))
	for _, in := range inputs {
		out[lotKey(in.ProductID, in.BestBefore)] = lotData{
			Name:      in.Name,
			UOM:       in.UOM,
			SalePrice: in.SalePrice,
			SiteURL:   in.SiteURL,
			Qty:       in.Qty,
		}
	}

	return out
}

// lotDataFromPairs — те же данные из состояния пар (пересборка плана).
func lotDataFromPairs(pairs []PairState) map[discounts.LotKey]lotData {
	out := make(map[discounts.LotKey]lotData, len(pairs))
	for _, p := range pairs {
		out[p.Key] = lotData{
			Name:      p.Name,
			UOM:       p.UOM,
			SalePrice: p.SalePrice,
			SiteURL:   p.SiteURL,
			Qty:       p.Qty,
		}
	}

	return out
}

// digestItems — позиции рассылки из плана дня: блок — число, которое печатает
// письмо (план продаж добора из избытка, у сроковых и ручных — остаток пары),
// остаток — остаток пары на этот момент. Позиция с нулевым блоком в рассылку не
// идёт: печатать «0 шт.» нечего.
func digestItems(plan dayPlan) []discounts.DigestItem {
	items := make([]discounts.DigestItem, 0, len(plan.slot))
	for _, s := range plan.slot {
		block := mailingQty(s)
		if block <= 0 {
			continue
		}
		items = append(items, discounts.DigestItem{
			ProductID:  s.pair.ProductID,
			BestBefore: s.pair.BestBefore,
			Percent:    s.percent,
			Reason:     s.reason,
			InitialQty: s.initialQty,
			PlanQty:    s.planQty,
			BlockQty:   block,
			StockQty:   s.pair.Qty,
		})
	}

	return items
}

// mailingText — текст письма по позициям с заданными числами блоков. Пустой
// список (всё распродано) — пустая строка.
func mailingText(items []discounts.DigestItem, qty map[discounts.LotKey]int64, data map[discounts.LotKey]lotData) string {
	mailing := make([]discounts.MailingItem, 0, len(items))
	for _, it := range items {
		block := qty[lotKey(it.ProductID, it.BestBefore)]
		if block <= 0 {
			continue
		}
		d := data[lotKey(it.ProductID, it.BestBefore)]
		mailing = append(mailing, discounts.MailingItem{
			Name:       d.Name,
			SiteURL:    d.SiteURL,
			UOM:        d.UOM,
			SalePrice:  d.SalePrice,
			Percent:    it.Percent,
			Qty:        block,
			BestBefore: it.BestBefore,
		})
	}

	return discounts.BuildMailing(mailing)
}

// planDay собирает план дня по текущему снапшоту остатков: вход, оборот,
// состояния пар и раскладка слота. Оборот берётся из снимка первого расчёта дня
// (ratesFor), чтобы числа плана менялись только от остатков, а не от обновления
// текущего периода оборота другими шагами.
func (uc *UseCase) planDay(ctx context.Context, day time.Time, capacity int) ([]PairState, dayPlan, error) {
	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return nil, dayPlan{}, err
	}
	rates, err := uc.ratesFor(ctx, day, inputProductIDs(inputs))
	if err != nil {
		return nil, dayPlan{}, fmt.Errorf("живое письмо: оборот: %w", err)
	}
	pairs := Evaluate(inputs, rates, day)
	// before = день плана: сегодняшний план в истории уже есть (sent_at), но
	// «предыдущая рассылка» для антидубля — вчерашняя, иначе пересборка
	// вычёркивает из слота все позиции дня (инцидент 20.10.2026).
	prev, err := uc.repo.LastDigestPairs(ctx, day)
	if err != nil {
		return nil, dayPlan{}, err
	}

	return pairs, buildDayPlan(pairs, prev, capacity), nil
}

// RunSlotPlan — план дня (14:00): применить ТГ-колонку слота, отправить письмо
// складу кнопкой «Пропустить рассылку» и отметить маркеры дня.
//
// Скидка сайта «лишним» позициям на этом шаге НЕ пишется: живое письмо
// переносит её на финальную сборку 14:45 (решение владельца 18.10.2026) —
// иначе позиция, попавшая в слот следующим пересчётом, оставила бы на сайте
// скидку «лишней».
func (uc *UseCase) RunSlotPlan(ctx context.Context, now time.Time, capacity int) error {
	day := beginningOfDay(now)

	done, err := uc.repo.DayFlagDone(ctx, day, discounts.FlagPlan)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	pairs, plan, err := uc.planDay(ctx, day, capacity)
	if err != nil {
		return err
	}

	// Позициям слота — ТГ-колонка. «Лишние» ждут 14:45 ВСЕГДА: до финальной
	// сборки позиция может уйти из слота или встать в него, и скидка сайта,
	// выданная заранее, сделала бы возврат в слот бессмысленным (решение
	// владельца 18.10.2026).
	writes := slotWrites(plan)
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("план дня: %w", err)
		}
	}
	applyWrites(pairs, writes)
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

// writeSlot — сохранить план рассылки и отправить письмо складу. История —
// позиции плана (по ним работают антидубль, правка письма и подъём 16:00);
// первое сообщение — окно активных скидок с метками канала (снимок 14:00, живое
// письмо его не трогает). Пустой слот — молчание.
func (uc *UseCase) writeSlot(ctx context.Context, pairs []PairState, plan dayPlan, now time.Time) error {
	if len(plan.slot) == 0 {
		return nil
	}

	day := beginningOfDay(now)
	items := digestItems(plan)
	if err := uc.repo.SaveDigest(ctx, discounts.DigestRecord{PlannedAt: day, ChatKind: discounts.ChatWarehouse}, items); err != nil {
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

	uc.resetMailing()
	if uc.warehouse == nil {
		slog.Info(fmt.Sprintf("discounts: письмо складу (канал не подключён): %s",
			mailingText(items, blockQty(items), lotDataFromPairs(pairs))))

		return uc.repo.MarkDigestSent(ctx, discounts.ChatWarehouse, day, now)
	}

	// Письмо — с кнопкой пропуска: id сообщения живёт в истории, по нему письмо
	// правится каждый пересчёт (одно сообщение вместо пяти копий).
	data := lotDataFromPairs(pairs)
	messageID, err := uc.warehouse.NotifyWarehouseHTMLButton(
		mailingText(items, blockQty(items), data), MailingSkipButton, MailingSkipCallback)
	if err != nil {
		return fmt.Errorf("письмо складу: %w", err)
	}
	if messageID != 0 {
		if err := uc.repo.SetDigestMessage(ctx, discounts.ChatWarehouse, day, messageID); err != nil {
			return err
		}
	}
	uc.setMailingText(mailingText(items, blockQty(items), data))

	// Маркер отправки — только после успешной отправки: иначе подъём 16:00
	// считал бы несобранную рассылку отправленной.
	if err := uc.repo.MarkDigestSent(ctx, discounts.ChatWarehouse, day, now); err != nil {
		return err
	}
	uc.setPlanTime(now)

	return nil
}

// blockQty — числа блоков из снимка позиций.
func blockQty(items []discounts.DigestItem) map[discounts.LotKey]int64 {
	out := make(map[discounts.LotKey]int64, len(items))
	for _, it := range items {
		out[lotKey(it.ProductID, it.BestBefore)] = it.BlockQty
	}

	return out
}

// refreshMailing — один такт живого письма: пересобрать план, привести
// ТГ-колонку и состав истории к новому плану и отредактировать ТО ЖЕ сообщение.
// final — финальная сборка 14:45: пишется скидка сайта «лишним», фиксируется
// снимок блоков и уходят запросы фото.
//
// Ничего не делает, если рассылки нет, она пропущена или уже зафиксирована.
func (uc *UseCase) refreshMailing(ctx context.Context, now time.Time, capacity int, pass mailingPass) error {
	day := beginningOfDay(now)

	st, err := uc.repo.TodayDigest(ctx, day, discounts.ChatWarehouse)
	if err != nil {
		return err
	}
	if st == nil || st.SentAt == nil || st.MessageID == 0 || st.FrozenAt != nil {
		return nil
	}

	pairs, plan, err := uc.planDay(ctx, day, capacity)
	if err != nil {
		return err
	}

	// ТГ-колонка: позиции, вошедшие в слот, получают скидку дня; вышедшие — 0,
	// если значение ставил план (ручную ТГ менеджера не трогаем).
	writes := liveTelegramWrites(pairs, plan, st.Items)
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("живое письмо: ТГ-колонка: %w", err)
		}
		applyWrites(pairs, writes)
	}

	items := digestItems(plan)
	if err := uc.repo.ReplaceDigestItems(ctx, discounts.ChatWarehouse, day, items); err != nil {
		return err
	}

	data := lotDataFromPairs(pairs)
	qty := blockQty(items)
	text := mailingText(items, qty, data)
	if text == "" {
		text = discounts.MailingSoldOutText
	}
	if text != uc.mailingTextValue() {
		if err := uc.warehouse.EditWarehouseHTML(st.MessageID, text, MailingSkipButton, MailingSkipCallback); err != nil {
			return fmt.Errorf("живое письмо: правка: %w", err)
		}
		uc.setMailingText(text)
	}

	if pass != mailingFreeze {
		return nil
	}

	// 14:45: «лишние» получают скидку сайта — один раз, здесь.
	if err := uc.applyExtras(ctx, now, capacity); err != nil {
		return err
	}

	if err := uc.repo.MarkDigestFrozen(ctx, discounts.ChatWarehouse, day, now); err != nil {
		return err
	}
	if err := uc.requestPhotos(ctx, day, items, data); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("discounts: письмо складу зафиксировано (%d позиций)", len(items)))

	return nil
}

// applyExtras — скидка сайта «лишним» позициям дня. Единственная точка
// правила: 14:45, финальная сборка. Работает и БЕЗ письма (пустой слот или
// рассылка выключена ёмкостью 0): иначе повышениям дня некуда деться, а
// выдавать их раньше нельзя — до 14:45 позиция может вернуться в слот.
func (uc *UseCase) applyExtras(ctx context.Context, now time.Time, capacity int) error {
	day := beginningOfDay(now)

	pairs, plan, err := uc.planDay(ctx, day, capacity)
	if err != nil {
		return err
	}
	extra := extraWrites(plan)
	if len(extra) > 0 {
		if err := uc.writer.SetDiscounts(ctx, extra); err != nil {
			return fmt.Errorf("финальная сборка: скидка лишним: %w", err)
		}
		applyWrites(pairs, extra)
	}
	changes := uc.replaceRegistry(pairs, now)
	if len(changes) > 0 {
		uc.notifyChanges(ctx, changes)
	}

	return nil
}

// liveTelegramWrites — правки ТГ-колонки при пересборке: вошедшим в слот —
// скидка дня, вышедшим — снятие (0), но только если значение ставил план
// (владелец escalation). Ручную ТГ менеджера не трогаем: её ставит человек.
func liveTelegramWrites(pairs []PairState, plan dayPlan, old []discounts.DigestItem) []discounts.DiscountWrite {
	writes := slotWrites(plan)

	byKey := make(map[discounts.LotKey]int, len(pairs))
	for i := range pairs {
		byKey[pairs[i].Key] = i
	}
	inPlan := make(map[discounts.LotKey]struct{}, len(plan.slot))
	for _, s := range plan.slot {
		inPlan[s.pair.Key] = struct{}{}
	}

	for _, it := range old {
		key := lotKey(it.ProductID, it.BestBefore)
		if _, ok := inPlan[key]; ok {
			continue
		}
		idx, ok := byKey[key]
		if !ok {
			continue // лота больше нет в снапшоте: снимать не с чего
		}
		p := &pairs[idx]
		if p.TelegramOwner != discounts.OwnerEscalation {
			continue
		}
		if discountPercent(p.TelegramPlain) == 0 {
			continue
		}
		zero := int16(0)
		p.TelegramPlain = &zero
		p.TelegramOwner = discounts.OwnerNone
		writes = append(writes, writeFor(*p))
	}

	return writes
}

// requestPhotos — запросы фото по видимым блокам: одно фото на лот, поэтому на
// блок уходит одно сообщение с force_reply (фото придёт ответом на него).
// Запрос не повторяется, если он уже есть в чате (id в истории) или фото уже
// прислали (память процесса).
func (uc *UseCase) requestPhotos(ctx context.Context, day time.Time, items []discounts.DigestItem, data map[discounts.LotKey]lotData) error {
	for _, it := range items {
		key := lotKey(it.ProductID, it.BestBefore)
		if it.BlockQty <= 0 || it.MessageID != 0 || uc.photo(key) != "" {
			continue
		}
		name := data[key].Name
		messageID, err := uc.warehouse.NotifyWarehouseReplyRequest(discounts.PhotoRequest(name, it.BestBefore))
		if err != nil {
			return fmt.Errorf("запрос фото %s %s: %w", it.ProductID, it.BestBefore.Format(time.DateOnly), err)
		}
		if messageID != 0 {
			if err := uc.repo.SetDigestItemMessage(ctx, discounts.ChatWarehouse, day, key, messageID); err != nil {
				return err
			}
		}
		uc.setPhotoName(key, name)
	}

	return nil
}

// syncMailingQty — окно 14:45–15:00: состав письма зафиксирован, меняются только
// количества. Продажа уменьшает блок, возврат возвращает (не выше значения на
// 14:45), ноль убирает блок и его запрос фото, возврат блока запрашивает фото
// заново. Избыток здесь НЕ пересчитывается: блок — доля плана, а не остаток.
func (uc *UseCase) syncMailingQty(ctx context.Context, now, planAt time.Time) error {
	day := beginningOfDay(now)

	st, err := uc.repo.TodayDigest(ctx, day, discounts.ChatWarehouse)
	if err != nil {
		return err
	}
	if st == nil || st.SentAt == nil || st.MessageID == 0 || len(st.Items) == 0 {
		return nil
	}
	if !now.Before(planAt.Add(mailingPublishHour)) {
		return nil // 15:00 — рассылка ушла, письмо не правим
	}

	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return err
	}
	data := lotDataFromInputs(inputs)
	qty := displayedQty(st.Items, data)

	text := mailingText(st.Items, qty, data)
	button, callback := MailingSkipButton, MailingSkipCallback
	if text == "" {
		text = discounts.MailingSoldOutText
		button, callback = "", ""
	}

	if text != uc.mailingTextValue() {
		if err := uc.warehouse.EditWarehouseHTML(st.MessageID, text, button, callback); err != nil {
			return fmt.Errorf("окно 14:45–15:00: правка письма: %w", err)
		}
		uc.setMailingText(text)
	}

	return uc.syncPhotoRequests(ctx, day, st.Items, qty, data)
}

// syncPhotoRequests — сообщения-запросы фото под текущие блоки: блок ушёл —
// запрос удаляем (id в истории обнуляем), блок вернулся — запрашиваем заново,
// если фото ещё нет.
func (uc *UseCase) syncPhotoRequests(ctx context.Context, day time.Time, items []discounts.DigestItem, qty map[discounts.LotKey]int64, data map[discounts.LotKey]lotData) error {
	for _, it := range items {
		key := lotKey(it.ProductID, it.BestBefore)
		visible := qty[key] > 0
		switch {
		case visible && it.MessageID == 0 && uc.photo(key) == "":
			name := data[key].Name
			messageID, err := uc.warehouse.NotifyWarehouseReplyRequest(discounts.PhotoRequest(name, it.BestBefore))
			if err != nil {
				return fmt.Errorf("запрос фото %s %s: %w", it.ProductID, it.BestBefore.Format(time.DateOnly), err)
			}
			if messageID != 0 {
				if err := uc.repo.SetDigestItemMessage(ctx, discounts.ChatWarehouse, day, key, messageID); err != nil {
					return err
				}
			}
			uc.setPhotoName(key, name)
		case !visible && it.MessageID != 0:
			if err := uc.warehouse.DeleteWarehouseReply(it.MessageID); err != nil {
				slog.Info(fmt.Sprintf("discounts: удаление запроса фото: %v", err))
			}
			if err := uc.repo.SetDigestItemMessage(ctx, discounts.ChatWarehouse, day, key, 0); err != nil {
				return err
			}
		default:
			// фото и запрос уже на месте — делать нечего
		}
	}

	return nil
}

// displayedQty — числа блоков в окне 14:45–15:00. Считаются дельты от снимка
// 14:45: блоки «избытка» одной группы показывают её общий объём (продажа любой
// пары уменьшает группу), поэтому сначала считается итог группы, затем он
// раскладывается FIFO — ближний срок забирает первым, обрезка идёт с дальнего
// конца. Возврат поднимает итог, но не выше снимка.
func displayedQty(items []discounts.DigestItem, data map[discounts.LotKey]lotData) map[discounts.LotKey]int64 {
	out := make(map[discounts.LotKey]int64, len(items))
	for _, it := range items {
		key := lotKey(it.ProductID, it.BestBefore)
		// Дельта остатка ЛОТА от снимка 14:45: продажа уменьшает блок, возврат
		// увеличивает, но не выше записанного числа. Пересчёта избытка в окне
		// нет — блок избытка это доля плана, а не остаток пары (решение
		// владельца 18.10.2026).
		block := it.BlockQty - (it.StockQty - data[key].Qty)
		block = min(max(block, 0), it.BlockQty)
		out[key] = block
	}

	return out
}

// SkipMailing — кнопка «Пропустить рассылку»: рассылки не будет, поэтому
//   - поднимаем на сайт своё повышение по сроку и ручную ТГ (решение владельца
//     18.10.2026); «форс до 20 %» (добор, избыток) НЕ поднимаем — рассылки не было;
//   - ТГ-колонку снимаем: её ставил план под рассылку;
//   - письмо заменяем сообщением о пропуске, кнопку снимаем, запросы фото удаляем;
//   - историю рассылки удаляем: лоты не считаются «бывшими в ТГ» и попадут в
//     следующую, а 16:00 поднимать нечего (маркер подъёма ставим).
func (uc *UseCase) SkipMailing(ctx context.Context, callbackQueryID string, _, messageID int64) error {
	if err := uc.warehouse.WarehouseCallbackAck(callbackQueryID); err != nil {
		slog.Info(fmt.Sprintf("discounts: ответ на кнопку пропуска: %v", err))
	}

	now := uc.now()
	day := beginningOfDay(now)

	st, err := uc.repo.TodayDigest(ctx, day, discounts.ChatWarehouse)
	if err != nil {
		uc.alert(callbackQueryID, "Не удалось прочитать рассылку")

		return err
	}
	if st == nil || st.MessageID != messageID {
		uc.alert(callbackQueryID, "Активной рассылки нет")

		return nil
	}
	if st.SentAt == nil {
		uc.alert(callbackQueryID, "Рассылка уже пропущена")

		return nil
	}
	if planAt, ok := uc.planTime(); ok && !now.Before(planAt.Add(mailingPublishHour)) {
		uc.alert(callbackQueryID, "Рассылка уже опубликована")

		return nil
	}

	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return err
	}
	rates, err := uc.ratesFor(ctx, day, inputProductIDs(inputs))
	if err != nil {
		return fmt.Errorf("пропуск рассылки: оборот: %w", err)
	}
	pairs := Evaluate(inputs, rates, day)

	writes := skipWrites(pairs, st.Items)
	if len(writes) > 0 {
		if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
			return fmt.Errorf("пропуск рассылки: подъём скидок: %w", err)
		}
		applyWrites(pairs, writes)
		uc.notifyChanges(ctx, uc.replaceRegistry(pairs, now))
	}

	for _, it := range st.Items {
		if it.MessageID == 0 {
			continue
		}
		if err := uc.warehouse.DeleteWarehouseReply(it.MessageID); err != nil {
			slog.Info(fmt.Sprintf("discounts: удаление запроса фото: %v", err))
		}
	}

	if err := uc.warehouse.EditWarehouseHTML(messageID, discounts.MailingSkippedText, "", ""); err != nil {
		slog.Info(fmt.Sprintf("discounts: письмо о пропуске: %v", err))
	}

	// Рассылки не было: история удаляется целиком (антидубль и план продаж её не
	// видят), маркер подъёма ставится — 16:00 молчит.
	if err := uc.repo.DeleteDigest(ctx, discounts.ChatWarehouse, day); err != nil {
		return err
	}
	if err := uc.repo.MarkDayFlag(ctx, day, discounts.FlagRaise); err != nil {
		return err
	}
	uc.resetMailing()
	slog.Info("discounts: рассылка пропущена складом — скидки подняты, позиции вернутся в следующую")

	return nil
}

// skipWrites — что поднять на сайт при пропуске рассылки: своя ступень по сроку
// (её позиция получила бы сегодня в любом случае) и ручная ТГ менеджера.
// Форс-повышение (добор до 20 %, избыток) остаётся невыполненным: рассылки не
// было, и сайту оно не положено.
func skipWrites(pairs []PairState, items []discounts.DigestItem) []discounts.DiscountWrite {
	byKey := make(map[discounts.LotKey]int, len(pairs))
	for i := range pairs {
		byKey[pairs[i].Key] = i
	}

	inPlan := make(map[discounts.LotKey]struct{}, len(items))
	writes := make([]discounts.DiscountWrite, 0, len(items))
	for _, it := range items {
		key := lotKey(it.ProductID, it.BestBefore)
		idx, ok := byKey[key]
		if !ok {
			continue // пара распродана: остатка нет, поднимать и снимать нечего
		}
		inPlan[key] = struct{}{}
		p := &pairs[idx]

		raised := false
		if target, owner, ok := skipTarget(*p, it); ok && discountPercent(p.Applied) < target {
			general := target
			p.AppliedPlain = &general
			p.SourceRaw = it.Reason
			p.GeneralOwner = owner
			raised = true
		}

		// ТГ-колонку снимаем у всех позиций рассылки: её ставил план.
		cleared := false
		if p.TelegramOwner == discounts.OwnerEscalation && discountPercent(p.TelegramPlain) > 0 {
			zero := int16(0)
			p.TelegramPlain = &zero
			p.TelegramOwner = discounts.OwnerNone
			cleared = true
		}
		if raised || cleared {
			writes = append(writes, writeFor(*p))
		}
	}

	// Ручная ТГ вне отправленной рассылки: её поставили уже после составления
	// списка — подъём 16:00 доводит её до сайта теми же правилами, и пропуск
	// рассылки тоже (решение владельца 29.09.2026).
	for i := range pairs {
		p := &pairs[i]
		if _, ok := inPlan[p.Key]; ok {
			continue
		}
		if !p.TelegramActive() || p.Manual != nil {
			continue
		}
		target := *p.TelegramManual
		if discountPercent(p.Applied) >= target {
			continue
		}
		general := target
		p.AppliedPlain = &general
		p.SourceRaw = discounts.ReasonManual
		p.GeneralOwner = discounts.OwnerManual
		writes = append(writes, writeFor(*p))
	}

	return writes
}

// skipTarget — что из плана позиции переживает пропуск рассылки: своя ступень по
// сроку (сайт получил бы её и без рассылки) и ручная ТГ-скидка (решение
// человека). Добор до скидки дня и избыток живут только рассылкой — им цели нет.
func skipTarget(p PairState, it discounts.DigestItem) (int16, discounts.Owner, bool) {
	if p.Manual != nil {
		return 0, discounts.OwnerNone, false // ручная сайта важнее (в том числе 0 %)
	}
	if p.TelegramManual != nil && *p.TelegramManual > 0 {
		return *p.TelegramManual, discounts.OwnerManual, true
	}
	if it.Reason == discounts.ReasonExpiry && p.Expiry != nil && *p.Expiry == it.Percent {
		return *p.Expiry, discounts.OwnerExpiry, true
	}

	return 0, discounts.OwnerNone, false
}

// HandlePhoto — фото от склада: пришло ОТВЕТОМ на запрос позиции, по
// reply_to_message находим блок и держим file_id в памяти (фото нужно только до
// публикации рассылки, в БД его не кладём). Фото без ответа на наш запрос
// игнорируется: привязать его к позиции нельзя.
func (uc *UseCase) HandlePhoto(ctx context.Context, _, replyToMessageID int64, fileID string) error {
	if fileID == "" || replyToMessageID == 0 {
		return nil
	}

	day := beginningOfDay(uc.now())
	st, err := uc.repo.TodayDigest(ctx, day, discounts.ChatWarehouse)
	if err != nil || st == nil || st.SentAt == nil {
		return err
	}

	for _, it := range st.Items {
		if it.MessageID == 0 || it.MessageID != replyToMessageID {
			continue
		}
		key := lotKey(it.ProductID, it.BestBefore)
		uc.setPhoto(key, fileID)
		name := uc.photoName(key)
		if name == "" {
			name = it.ProductID
		}

		// Подтверждение с кнопкой «Переделать фото»: фото могло прийти не то
		// (размытое, не тот лот) — склад просит новое, не дожидаясь публикации
		// (решение владельца 22.10.2026).
		if _, err := uc.warehouse.NotifyWarehouseHTMLButton(
			discounts.PhotoAccepted(name, it.BestBefore),
			discounts.PhotoRedoButton,
			PhotoRedoCallback(key),
		); err != nil {
			return fmt.Errorf("подтверждение фото %s: %w", it.ProductID, err)
		}

		return nil
	}

	return nil
}

// PhotoRedoCallback — данные кнопки «Переделать фото» под подтверждением
// принятого фото: лот, к которому оно относится.
func PhotoRedoCallback(key discounts.LotKey) string {
	return photoRedoPrefix + key.ProductID + "@" + key.BestBefore.Format(time.DateOnly)
}

// ParsePhotoRedoCallback — разбор данных кнопки: лот, чьё фото переделываем.
// false — данные не наши (кнопка другого модуля или чужая разметка).
func ParsePhotoRedoCallback(data string) (discounts.LotKey, bool) {
	rest, ok := strings.CutPrefix(data, photoRedoPrefix)
	if !ok {
		return discounts.LotKey{}, false
	}
	productID, date, ok := strings.Cut(rest, "@")
	if !ok || productID == "" {
		return discounts.LotKey{}, false
	}
	bestBefore, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return discounts.LotKey{}, false
	}

	return lotKey(productID, bestBefore), true
}

// RedoPhoto — кнопка «Переделать фото» под подтверждением: склад просит новое
// фото вместо принятого (размытое, не тот лот). Старое фото забываем, сообщение
// с кнопкой убираем, запрос уходит заново — новое фото придёт ответом на новый
// запрос и заменит старое в альбоме (решение владельца 22.10.2026).
//
// Ответ на нажатие даём ОДИН раз: сначала отказы (alert), в конце «часики».
// Порядок как в Telegram: на один callback_query уходит один ответ, поэтому
// ack в начале съел бы причину отказа.
func (uc *UseCase) RedoPhoto(ctx context.Context, callbackQueryID string, _, messageID int64, callbackData string) error {
	key, ok := ParsePhotoRedoCallback(callbackData)
	if !ok {
		return uc.warehouse.WarehouseCallbackAck(callbackQueryID)
	}

	day := beginningOfDay(uc.now())
	st, err := uc.repo.TodayDigest(ctx, day, discounts.ChatWarehouse)
	if err != nil {
		uc.alert(callbackQueryID, "Не удалось прочитать рассылку")

		return err
	}
	if st == nil || st.SentAt == nil {
		uc.alert(callbackQueryID, "Активной рассылки нет")

		return nil
	}

	var item *discounts.DigestItem
	for i := range st.Items {
		if lotKey(st.Items[i].ProductID, st.Items[i].BestBefore) == key {
			item = &st.Items[i]

			break
		}
	}
	if item == nil {
		uc.alert(callbackQueryID, "Позиции уже нет в рассылке")

		return nil
	}
	if st.ShopMessageID != 0 {
		uc.alert(callbackQueryID, "Пост в чате скидок уже отправлен: фото в альбоме не заменить")

		return nil
	}

	// Старое фото забываем: иначе проход фото решит, что оно уже есть, и нового
	// запроса не будет.
	uc.clearPhoto(key)
	if messageID != 0 {
		if err := uc.warehouse.DeleteWarehouseReply(messageID); err != nil {
			slog.Info(fmt.Sprintf("discounts: удаление подтверждения фото: %v", err))
		}
	}
	uc.askPhoto(ctx, day, *item, uc.photoLotName(ctx, day, key))

	return uc.warehouse.WarehouseCallbackAck(callbackQueryID)
}

// photoLotName — название лота для запроса фото: память процесса, а после
// рестарта (её нет) — карточка из снапшота остатков. Совсем не нашли — id
// товара: запрос без названия хуже, чем запрос с id.
func (uc *UseCase) photoLotName(ctx context.Context, day time.Time, key discounts.LotKey) string {
	if name := uc.photoName(key); name != "" {
		return name
	}
	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return key.ProductID
	}
	for _, in := range inputs {
		if lotKey(in.ProductID, in.BestBefore) == key {
			return in.Name
		}
	}

	return key.ProductID
}

// alert — всплывающее окно с причиной отказа на нажатие кнопки.
func (uc *UseCase) alert(callbackQueryID, text string) {
	if err := uc.warehouse.WarehouseCallbackAlert(callbackQueryID, text); err != nil {
		slog.Info(fmt.Sprintf("discounts: окно отказа кнопки: %v", err))
	}
}

// ratesFor — действующий оборот товаров на день: снимок первого расчёта дня.
// Второй и следующие пересчёты берут его же, поэтому числа плана меняются
// только от остатков (решение владельца 18.10.2026). После рестарта снимка нет —
// берём сохранённый оборот из БД (Averages), он за день не меняется.
func (uc *UseCase) ratesFor(ctx context.Context, day time.Time, ids []string) (map[string]float64, error) {
	uc.mailMu.Lock()
	rates, dayOK := uc.rates, uc.ratesDay.Equal(day)
	uc.mailMu.Unlock()
	if dayOK && rates != nil {
		return rates, nil
	}

	rates, err := uc.turnover.Averages(ctx, ids)
	if err != nil {
		return nil, err
	}

	uc.mailMu.Lock()
	uc.rates, uc.ratesDay = rates, day
	uc.mailMu.Unlock()

	return rates, nil
}

// --- состояние живого письма (память процесса) ---

// resetMailing — новый день плана: живое состояние письма и фото сбрасывается.
func (uc *UseCase) resetMailing() {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	uc.photos = make(map[discounts.LotKey]string)
	uc.photoNames = make(map[discounts.LotKey]string)
	uc.mailText = ""
	uc.lastMailPass = time.Time{}
}

func (uc *UseCase) setPlanTime(at time.Time) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	uc.planAt = at
}

func (uc *UseCase) planTime() (time.Time, bool) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	return uc.planAt, !uc.planAt.IsZero()
}

// mailPassDone — такт живого письма уже отработал (сетка 5 минут).
func (uc *UseCase) mailPassDone(slot time.Time) bool {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	return uc.lastMailPass.Equal(slot)
}

func (uc *UseCase) markMailPass(slot time.Time) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	uc.lastMailPass = slot
}

func (uc *UseCase) mailingTextValue() string {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	return uc.mailText
}

func (uc *UseCase) shopTextValue() string {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	return uc.shopPost
}

func (uc *UseCase) setShopText(text string) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	uc.shopPost = text
}

func (uc *UseCase) setMailingText(text string) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	uc.mailText = text
}

func (uc *UseCase) photo(key discounts.LotKey) string {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	return uc.photos[key]
}

func (uc *UseCase) setPhoto(key discounts.LotKey, fileID string) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	uc.photos[key] = fileID
}

// clearPhoto — фото лота забыто: склад нажал «Переделать фото» (фото пришло не
// то) или блок ушёл в ноль. Следующий проход фото запросит его заново.
func (uc *UseCase) clearPhoto(key discounts.LotKey) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	delete(uc.photos, key)
}

func (uc *UseCase) setPhotoName(key discounts.LotKey, name string) {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	uc.photoNames[key] = name
}

func (uc *UseCase) photoName(key discounts.LotKey) string {
	uc.mailMu.Lock()
	defer uc.mailMu.Unlock()

	return uc.photoNames[key]
}

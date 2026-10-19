// Пост в чат скидок (решение владельца 18.10.2026): в 15:00 бот отправляет
// альбом фото позиций рассылки и копию письма склада, а дальше правит этот пост
// до публикации следующего: продажа уменьшает остаток по акции, ноль помечает
// позицию «ПРОДАНО», возврат возвращает её. Избыточная позиция возвращается
// только если избыток по ней ещё есть; сроковая — сразу со скидкой на сайт и в
// ТГ-колонку (её скидку читатели уже видели в посте).
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"warehouseHelper/internal/discounts"
)

// shopPublishWindow — сколько ждём успешной публикации после 15:00: сбой
// отправки не должен съесть рассылку, но и вечно висеть нельзя.
const shopPublishWindow = 30 * time.Minute

// Пост в чат скидок (решение владельца 18.10.2026): в 15:00 бот отправляет
// поста каждую минуту. Правка идёт и в не-ТГ дни: пост живёт до следующей
// рассылки, читатель должен видеть актуальный остаток.
func (uc *UseCase) runShop(ctx context.Context, now time.Time, s Schedule) error {
	if isTelegramDay(now) {
		planAt := beginningOfDay(now).Add(s.Plan)
		from, to := planAt.Add(mailingPublishHour), planAt.Add(mailingPublishHour+shopPublishWindow)
		if !now.Before(from) && now.Before(to) {
			if err := uc.publishShop(ctx, now); err != nil {
				return err
			}
		}
	}

	return uc.syncShopPost(ctx, now)
}

// publishShop — альбом фото позиций и пост-копия письма склада. Состав — то,
// что зафиксировано в 14:45; числа — текущие (с 14:45 до 15:00 письмо склада
// тоже правилось количествами). Позиции, распроданные к 15:00, в пост не идут:
// рекламировать нечего, появятся только при возврате.
func (uc *UseCase) publishShop(ctx context.Context, now time.Time) error {
	day := beginningOfDay(now)

	st, err := uc.repo.TodayDigest(ctx, day, discounts.ChatWarehouse)
	if err != nil {
		return err
	}
	if st == nil || st.FrozenAt == nil || st.MessageID == 0 || st.ShopMessageID != 0 || len(st.Items) == 0 {
		return nil // нечего публиковать или уже опубликовано
	}

	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return err
	}
	data := lotDataFromInputs(inputs)

	items := make([]discounts.DigestItem, len(st.Items))
	copy(items, st.Items)
	photos := make([]string, 0, len(items))
	for i := range items {
		it := &items[i]
		key := lotKey(it.ProductID, it.BestBefore)
		it.ShopQty = max(0, min(it.BlockQty, data[key].Qty))
		it.Listed = it.ShopQty > 0
		it.ShopAvailable = it.ShopQty
		if !it.Listed {
			continue
		}
		if id := uc.photo(key); id != "" {
			photos = append(photos, id)

			continue
		}
		// Фото нет (рестарт потерял память) — просим заново, пост не ждёт.
		uc.askPhoto(ctx, day, *it, data[key].Name)
	}

	// Прошлую рассылку «подчищаем» перед отправкой новой: все блоки в ноль и с
	// меткой «ПРОДАНО» — читатель видит, что акция кончилась (18.10.2026).
	if err := uc.closeShopPost(ctx, day, data); err != nil {
		return err
	}

	text := shopText(items, data)
	if text == "" {
		return nil
	}

	if len(photos) > 0 {
		if err := uc.warehouse.SendShopPhotos(photos); err != nil {
			return err
		}
	}
	messageID, err := uc.warehouse.NotifyShopHTML(text)
	if err != nil {
		return fmt.Errorf("пост в чат скидок: %w", err)
	}
	if messageID != 0 {
		if err := uc.repo.SetDigestShopMessage(ctx, discounts.ChatWarehouse, day, messageID); err != nil {
			return err
		}
	}
	for _, it := range items {
		key := lotKey(it.ProductID, it.BestBefore)
		if err := uc.repo.UpdateDigestItemShop(ctx, day, key, it.ShopQty, it.ShopAvailable, it.Listed); err != nil {
			return err
		}
	}
	uc.setShopText(text)
	slog.Info(fmt.Sprintf("discounts: пост в чат скидок (%d позиций, %d фото)", len(items), len(photos)))

	return nil
}

// closeShopPost — подчистка предыдущей рассылки перед публикацией новой: все
// блоки получают 0 в «Остаток по акции» и метку ❗️ПРОДАНО❗️ перед названием
// (решение владельца 18.10.2026). Пост остаётся в чате честной историей: акция
// кончилась. Ноль пишем и в историю — чтобы повторный заход публикации не правил
// сообщение второй раз.
func (uc *UseCase) closeShopPost(ctx context.Context, day time.Time, data map[discounts.LotKey]lotData) error {
	prev, err := uc.repo.LastShopDigest(ctx)
	if err != nil || prev == nil || prev.ShopMessageID == 0 || len(prev.Items) == 0 {
		return err
	}
	if beginningOfDay(prev.PlannedAt).Equal(day) {
		return nil // сегодняшний пост ещё живой: его закрывать рано
	}

	items := make([]discounts.DigestItem, len(prev.Items))
	copy(items, prev.Items)
	closed := true
	for i := range items {
		if items[i].Listed && items[i].ShopQty > 0 {
			closed = false
		}
		items[i].ShopQty = 0
	}
	if closed {
		return nil // уже подчищен
	}

	text := shopText(items, data)
	if text == "" {
		return nil
	}
	if err := uc.warehouse.EditShopHTML(prev.ShopMessageID, text); err != nil {
		return fmt.Errorf("подчистка прошлой рассылки: %w", err)
	}
	for _, it := range items {
		if !it.Listed {
			continue
		}
		key := lotKey(it.ProductID, it.BestBefore)
		if err := uc.repo.UpdateDigestItemShop(ctx, prev.PlannedAt, key, 0, it.ShopAvailable, true); err != nil {
			return err
		}
	}

	return nil
}

// syncShopPost — правка опубликованного поста: количества и метка «продано».
// Возврат сроковой позиции дополнительно выставляет скидку на сайт и в ТГ.
func (uc *UseCase) syncShopPost(ctx context.Context, now time.Time) error {
	st, err := uc.repo.LastShopDigest(ctx)
	if err != nil || st == nil || len(st.Items) == 0 || st.ShopMessageID == 0 {
		return err
	}

	day := beginningOfDay(now)
	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return err
	}
	data := lotDataFromInputs(inputs)

	// Пары нужны только для проверки избытка: остальные позиции считаются по
	// остаткам, оборот им не нужен.
	var pairs []PairState
	if shopNeedsSurplusCheck(st.Items, data) {
		rates, err := uc.ratesFor(ctx, day, inputProductIDs(inputs))
		if err != nil {
			return err
		}
		pairs = Evaluate(inputs, rates, day)
	}

	changed := false
	for i := range st.Items {
		it := &st.Items[i]
		key := lotKey(it.ProductID, it.BestBefore)
		dec := shopDecide(*it, data[key].Qty, pairs, key)
		if dec.skip || (it.ShopQty == dec.qty && it.Listed == dec.listed) {
			continue
		}
		it.ShopQty, it.Listed = dec.qty, dec.listed
		if it.ShopAvailable == 0 && dec.qty > 0 {
			it.ShopAvailable = dec.qty
		}
		changed = true
		if err := uc.repo.UpdateDigestItemShop(ctx, st.PlannedAt, key, it.ShopQty, it.ShopAvailable, it.Listed); err != nil {
			return err
		}
		if dec.restore {
			if err := uc.restoreShopDiscount(ctx, *it); err != nil {
				return err
			}
		}
	}
	if !changed {
		return nil
	}

	text := shopText(st.Items, data)
	if text == "" || text == uc.shopTextValue() {
		return nil
	}
	if err := uc.warehouse.EditShopHTML(st.ShopMessageID, text); err != nil {
		return fmt.Errorf("правка поста в чате скидок: %w", err)
	}
	uc.setShopText(text)

	return nil
}

// shopText — текст поста: блоки как в письме склада, но распроданные позиции
// остаются с меткой «ПРОДАНО» и нулём (складской вариант их убирает).
func shopText(items []discounts.DigestItem, data map[discounts.LotKey]lotData) string {
	mailing := make([]discounts.MailingItem, 0, len(items))
	for _, it := range items {
		if !it.Listed {
			continue
		}
		d := data[lotKey(it.ProductID, it.BestBefore)]
		available := it.ShopAvailable
		mailing = append(mailing, discounts.MailingItem{
			Name:       d.Name,
			SiteURL:    d.SiteURL,
			UOM:        d.UOM,
			SalePrice:  d.SalePrice,
			Percent:    it.Percent,
			Qty:        it.ShopQty,
			Available:  &available,
			BestBefore: it.BestBefore,
			Sold:       it.ShopQty == 0,
		})
	}

	return discounts.BuildMailing(mailing)
}

// shopNeedsSurplusCheck — есть ли распроданная избыточная позиция, у которой
// снова появился остаток: только для неё нужен пересчёт избытка.
func shopNeedsSurplusCheck(items []discounts.DigestItem, data map[discounts.LotKey]lotData) bool {
	for _, it := range items {
		if it.Reason != discounts.ReasonSurplus || it.ShopQty != 0 {
			continue
		}
		if data[lotKey(it.ProductID, it.BestBefore)].Qty > 0 {
			return true
		}
	}

	return false
}

// surplusNow — есть ли избыток по паре сейчас (перепроверка возврата, п. 4.3).
func surplusNow(pairs []PairState, key discounts.LotKey) bool {
	for i := range pairs {
		if pairs[i].Key == key {
			return pairs[i].Surplus != nil
		}
	}

	return false
}

// restoreShopDiscount — возврат сроковой позиции: скидка из поста уходит на сайт
// и в ТГ-колонку сразу (п. 4.4): читатели её уже видели, держать её «в столе»
// нельзя. Владелец — эскалация: дальше значение живёт общими правилами.
func (uc *UseCase) restoreShopDiscount(ctx context.Context, it discounts.DigestItem) error {
	percent := it.Percent
	writes := []discounts.DiscountWrite{{
		ProductID:     it.ProductID,
		BestBefore:    it.BestBefore,
		General:       &percent,
		Telegram:      &percent,
		Source:        it.Reason,
		GeneralOwner:  discounts.OwnerEscalation.String(),
		TelegramOwner: discounts.OwnerEscalation.String(),
	}}
	if err := uc.writer.SetDiscounts(ctx, writes); err != nil {
		return fmt.Errorf("возврат позиции поста: скидка на сайт: %w", err)
	}

	return nil
}

// askPhoto — повторный запрос фото у склада: данных в памяти нет (рестарт),
// позиция нужна в посте (решение владельца 18.10.2026 — проверяем и просим).
func (uc *UseCase) askPhoto(ctx context.Context, day time.Time, it discounts.DigestItem, name string) {
	messageID, err := uc.warehouse.NotifyWarehouseReplyRequest(discounts.PhotoRequest(name, it.BestBefore))
	if err != nil {
		slog.Info(fmt.Sprintf("discounts: повторный запрос фото: %v", err))

		return
	}
	key := lotKey(it.ProductID, it.BestBefore)
	if messageID != 0 {
		if err := uc.repo.SetDigestItemMessage(ctx, discounts.ChatWarehouse, day, key, messageID); err != nil {
			slog.Info(fmt.Sprintf("discounts: повторный запрос фото: %v", err))
		}
	}
	uc.setPhotoName(key, name)
}

// shopDecide — что делать с позицией поста по текущему остатку: сколько
// показывать, остаётся ли она в посте и надо ли вернуть ей скидку. Возврат
// избыточной позиции без избытка не возвращаем (решение владельца 18.10.2026).
func shopDecide(it discounts.DigestItem, stock int64, pairs []PairState, key discounts.LotKey) shopDecideResult {
	qty := max(0, min(it.BlockQty, stock))
	returned := it.ShopQty == 0 && qty > 0
	if returned && it.Reason == discounts.ReasonSurplus && !surplusNow(pairs, key) {
		return shopDecideResult{skip: true}
	}

	return shopDecideResult{
		qty:     qty,
		listed:  it.Listed || qty > 0,
		restore: returned && it.Reason != discounts.ReasonSurplus,
	}
}

// shopDecideResult — результат shopDecide: skip — позицию не трогаем.
type shopDecideResult struct {
	qty     int64
	listed  bool
	restore bool
	skip    bool
}

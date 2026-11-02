// Пост в чат скидок (решение владельца 18.10.2026): в 15:00 бот отправляет
// альбом фото позиций рассылки и копию письма склада, а дальше правит этот пост
// до публикации следующего: продажа уменьшает остаток по акции, ноль помечает
// позицию «ПРОДАНО», возврат возвращает её. Правит по событию стока (подбор,
// приёмка, замена остатков) и подтверждением раз в 5 минут. Избыточная позиция
// возвращается только если избыток по ней ещё есть; сроковая — сразу со скидкой
// на сайт и в ТГ-колонку (её скидку читатели уже видели в посте).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"warehouseHelper/internal/discounts"
)

// shopPublishWindow — сколько ждём успешной публикации после 15:00: сбой
// отправки не должен съесть рассылку, но и вечно висеть нельзя.
const shopPublishWindow = 30 * time.Minute

// shopPostGoneNotice — что склад читает, когда пост в канале пропал и бот
// публикует новый (решение владельца 30.10.2026).
const shopPostGoneNotice = "Пост в чате скидок не найден: его удалили или бот больше не в этом канале. " +
	"Публикую новый пост с текущими остатками — правок в прежнем не будет."

// shopConfirmInterval — подтверждение актуальности поста по таймеру: основной
// путь правки — событие стока (подбор, приёмка, замена остатков), таймер лишь
// страхует, если событие потерялось (рестарт, ошибка Telegram).
// shopKickPause — склейка пачки событий: приёмка и подбор пишут десятки лотов
// подряд, на каждый лот пост не правим.
const (
	shopConfirmInterval = 5 * time.Minute
	shopKickPause       = 2 * time.Second
)

// kickShop — событие стока: пост пора пересмотреть. Канал на один сигнал:
// пачка событий склеивается в одну правку, а запись остатков не ждёт ни БД, ни
// Telegram (обработчик шва зовут прямо из записи остатков).
func (uc *UseCase) kickShop() {
	select {
	case uc.shopKick <- struct{}{}:
	default:
	}
}

// runShopPost — правка поста чата скидок: по событию стока сразу и раз в 5
// минут подтверждением, что пост сходится с остатками. Ошибки логируются и
// цикл не роняют: следующее событие или подтверждение повторит.
func (uc *UseCase) runShopPost(ctx context.Context) {
	ticker := time.NewTicker(shopConfirmInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-uc.shopKick:
			if !uc.waitKickPause(ctx) {
				return
			}
			uc.drainShopKick()
		case <-ticker.C:
		}

		if err := uc.syncShopPost(ctx, uc.now()); err != nil {
			slog.Info(fmt.Sprintf("discounts: правка поста в чате скидок: %v", err))
		}
	}
}

// waitKickPause — пауза склейки пачки событий; false — цикл останавливают.
func (uc *UseCase) waitKickPause(ctx context.Context) bool {
	timer := time.NewTimer(shopKickPause)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// drainShopKick — сигналы, накопившиеся за паузу: правка всё равно читает все
// остатки сразу, повторять её на каждый сигнал незачем.
func (uc *UseCase) drainShopKick() {
	select {
	case <-uc.shopKick:
	default:
	}
}

// runShop — окно публикации поста в чат скидок: с 15:00 бот отправляет пост и
// повторяет попытку каждую минуту, пока Telegram не примет (30 минут). Саму
// правку опубликованного поста делает runShopPost — по событию стока и
// подтверждением раз в 5 минут; минутный тик её не дублирует.
func (uc *UseCase) runShop(ctx context.Context, now time.Time, s Schedule) error {
	if !isTelegramDay(now) {
		return nil
	}
	planAt := beginningOfDay(now).Add(s.Plan)
	from, to := planAt.Add(mailingPublishHour), planAt.Add(mailingPublishHour+shopPublishWindow)
	if now.Before(from) || !now.Before(to) {
		return nil
	}

	return uc.publishShop(ctx, now)
}

// shopPhotos — file_id присланных фото в порядке блоков поста. Фото нет (память
// потеряна рестартом) — просим заново, пост не ждёт: альбом уйдёт без него,
// следующая публикация возьмёт присланное.
func (uc *UseCase) shopPhotos(ctx context.Context, day time.Time, items []discounts.DigestItem, data map[discounts.LotKey]lotData) []string {
	photos := make([]string, 0, len(items))
	for i := range items {
		it := &items[i]
		if !it.Listed {
			continue
		}
		// file_id берём из истории рассылки: после рестарта память пуста, а
		// фото уже принято — альбом обязан собраться.
		if it.PhotoFileID != "" {
			photos = append(photos, it.PhotoFileID)

			continue
		}
		key := lotKey(it.ProductID, it.BestBefore)
		if id := uc.photo(key); id != "" {
			photos = append(photos, id)

			continue
		}
		uc.askPhoto(ctx, day, *it, data[key].Name)
	}

	return photos
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
	for i := range items {
		it := &items[i]
		key := lotKey(it.ProductID, it.BestBefore)
		it.ShopQty = max(0, min(it.BlockQty, data[key].Qty))
		it.Listed = it.ShopQty > 0
		it.ShopAvailable = it.ShopQty
	}
	photos := uc.shopPhotos(ctx, day, items, data)

	// Прошлую рассылку «подчищаем» перед отправкой новой: все блоки в ноль и с
	// меткой «ПРОДАНО» — читатель видит, что акция кончилась (18.10.2026).
	// Подчистка идёт ДО снимка карточек нового поста: у закрытого поста свои
	// карточки в истории (см. shopCards).
	if err := uc.closeShopPost(ctx, day, data); err != nil {
		return err
	}

	data, err = uc.shopCards(ctx, day, items, data)
	if err != nil {
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

	data, err = uc.shopCards(ctx, prev.PlannedAt, items, data)
	if err != nil {
		return err
	}

	text := shopText(items, data)
	if text == "" {
		return nil
	}
	if err := uc.warehouse.EditShopHTML(prev.ShopMessageID, text); err != nil {
		// Прошлого поста уже нет или править его нельзя: подчищать нечего, и это
		// не повод не публиковать сегодняшнюю рассылку. Раньше ошибка выходила
		// наружу ДО отправки нового поста — удалённый пост вчерашней рассылки
		// съедал весь следующий день (выкатка на канал 30.10.2026).
		if shopPostGone(err) {
			slog.Info(fmt.Sprintf("discounts: прошлый пост не подчищён (%v) — публикую новую рассылку", err))

			return nil
		}

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
	// Карточки блоков: у распроданного лота остатков уже нет — берём снимок из
	// истории рассылки.
	data, err = uc.shopCards(ctx, st.PlannedAt, st.Items, data)
	if err != nil {
		return err
	}

	// Пары нужны только для проверки избытка: остальные позиции считаются по
	// остаткам, оборот им не нужен.
	pairs, err := uc.shopSurplusPairs(ctx, day, st.Items, data, inputs)
	if err != nil {
		return err
	}

	changed := false
	for i := range st.Items {
		it := &st.Items[i]
		key := lotKey(it.ProductID, it.BestBefore)
		dec := shopDecide(*it, data[key].Qty, pairs, key)
		if dec.skip || (it.ShopQty == dec.qty && it.Listed == dec.listed) {
			continue
		}
		if err := uc.applyShopDecision(ctx, st.PlannedAt, it, dec); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}

	text := shopText(st.Items, data)
	if text == "" || text == uc.shopTextValue() {
		return nil
	}
	if err := uc.warehouse.EditShopHTML(st.ShopMessageID, text); err != nil {
		// Пост из канала удалили: править нечего, но и молчать нельзя — нового
		// поста в канале нет, а акция идёт. Склад узнаёт, читатель получает
		// новый пост с текущими остатками (решение владельца 30.10.2026: не
		// восстанавливать правкой существующее, а делать новый пост).
		if errors.Is(err, discounts.ErrShopPostGone) {
			return uc.republishShopPost(ctx, st, text)
		}

		return fmt.Errorf("правка поста в чате скидок: %w", err)
	}
	uc.setShopText(text)

	return nil
}

// shopPostGone — править нечего: сообщения нет или оно нередактируемо. Оба
// отказа означают «пост не закроется», но только первый даёт право
// опубликовать новый пост без риска дубля.
func shopPostGone(err error) bool {
	return errors.Is(err, discounts.ErrShopPostGone) || errors.Is(err, discounts.ErrShopPostNotEditable)
}

// republishShopPost — пост чата скидок пропал из канала: правок больше нет.
// Склад получает уведомление, в канал уходит новый пост с текущими числами и
// становится «тем самым» постом дня (альбом фото не переотправляем: фото уже
// в чате, а пост-копия самодостаточна).
func (uc *UseCase) republishShopPost(ctx context.Context, st *discounts.DigestState, text string) error {
	if err := uc.warehouse.NotifyWarehouse(shopPostGoneNotice); err != nil {
		slog.Info(fmt.Sprintf("discounts: уведомление склада о пропавшем посте: %v", err))
	}

	messageID, err := uc.warehouse.NotifyShopHTML(text)
	if err != nil {
		return fmt.Errorf("повторная публикация поста в чате скидок: %w", err)
	}
	if messageID != 0 {
		if err := uc.repo.SetDigestShopMessage(ctx, discounts.ChatWarehouse, st.PlannedAt, messageID); err != nil {
			return err
		}
	}
	uc.setShopText(text)
	slog.Info(fmt.Sprintf("discounts: пост в чате скидок опубликован заново (id %d): прежний удалён", messageID))

	return nil
}

// shopCards — данные блоков поста: живые остатки, а у лота, которого в остатках
// уже нет, — карточка из истории рассылки (`Shop`). Проданный до нуля лот сток
// УДАЛЯЕТ (`DELETE FROM product_stock` в списании подбора): в снапшоте остатков
// у него нет ни названия, ни цены, а блок в посте остаётся — читатель должен
// видеть, ЧТО продано (инцидент 22.10.2026: пост печатал
// «1. ❗️ПРОДАНО❗️  | 30%», метка на месте, названия нет).
//
// Снимок живёт в строке позиции рассылки (shop_name/shop_uom/shop_sale_price/
// shop_site_url), а не в памяти процесса: пост в чате висит до следующей
// рассылки, рестарт память стирает, а история — нет. Пока лот жив, снимок
// обновляется (переименовали товар в МС, сменили цену) — пишем только при
// изменении, иначе событийная правка дёргала бы БД на каждом проходе.
func (uc *UseCase) shopCards(ctx context.Context, plannedAt time.Time,
	items []discounts.DigestItem, data map[discounts.LotKey]lotData) (map[discounts.LotKey]lotData, error) {
	for _, it := range items {
		if !it.Listed {
			continue
		}
		key := lotKey(it.ProductID, it.BestBefore)
		d := data[key]
		if d.Name == "" {
			// Лота в остатках нет: подставляем снимок из истории.
			if it.Shop.Name == "" {
				continue
			}
			d.Name, d.UOM, d.SalePrice, d.SiteURL = it.Shop.Name, it.Shop.UOM, it.Shop.SalePrice, it.Shop.SiteURL
			data[key] = d

			continue
		}
		// Лот жив: остатки — источник истины, снимок подтягиваем к ним.
		if d.Name == it.Shop.Name && d.UOM == it.Shop.UOM && d.SiteURL == it.Shop.SiteURL &&
			samePrice(d.SalePrice, it.Shop.SalePrice) {
			continue
		}
		card := discounts.ShopCard{Name: d.Name, UOM: d.UOM, SalePrice: d.SalePrice, SiteURL: d.SiteURL}
		if err := uc.repo.UpdateDigestItemShopCard(ctx, plannedAt, key, card); err != nil {
			return nil, fmt.Errorf("снимок карточки блока %s: %w", it.ProductID, err)
		}
	}

	return data, nil
}

// samePrice — цены совпадают (nil — «МС не отдала цену»).
func samePrice(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return *a == *b
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

// applyShopDecision — записать решение по позиции поста: числа и признак показа
// в историю рассылки, а вернувшейся сроковой позиции — ещё и скидку на сайт с
// ТГ-колонкой (читатели её уже видели в посте).
func (uc *UseCase) applyShopDecision(ctx context.Context, plannedAt time.Time, it *discounts.DigestItem, dec shopDecideResult) error {
	it.ShopQty, it.Listed = dec.qty, dec.listed
	if it.ShopAvailable == 0 && dec.qty > 0 {
		it.ShopAvailable = dec.qty
	}
	key := lotKey(it.ProductID, it.BestBefore)
	if err := uc.repo.UpdateDigestItemShop(ctx, plannedAt, key, it.ShopQty, it.ShopAvailable, it.Listed); err != nil {
		return err
	}
	if !dec.restore {
		return nil
	}

	return uc.restoreShopDiscount(ctx, *it)
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

// shopSurplusPairs — пары дня для перепроверки избытка: считаем их только когда
// в посте есть проданная избыточная позиция с вернувшимся остатком, иначе
// лишний расчёт на каждой минуте.
func (uc *UseCase) shopSurplusPairs(ctx context.Context, day time.Time, items []discounts.DigestItem,
	data map[discounts.LotKey]lotData, inputs []discounts.Input) ([]PairState, error) {
	if !shopNeedsSurplusCheck(items, data) {
		return nil, nil
	}
	rates, err := uc.ratesFor(ctx, day, inputProductIDs(inputs))
	if err != nil {
		return nil, err
	}

	return Evaluate(inputs, rates, day), nil
}

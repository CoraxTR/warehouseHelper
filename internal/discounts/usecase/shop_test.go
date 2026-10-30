package usecase

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// publishHarness — рассылка, зафиксированная в 14:45, с фото по первой позиции.
func publishHarness(t *testing.T, inputs ...discounts.Input) *slotHarness {
	t.Helper()

	h := newSlotHarness(recalcNow(1), inputs...)
	ctx := context.Background()
	if err := h.uc.RunSlotPlan(ctx, h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	h.finalPass(t, 4)

	if len(h.repo.digest.Items) == 0 {
		t.Fatal("в рассылке нет позиций")
	}
	reqID := h.repo.digest.Items[0].MessageID
	if reqID == 0 {
		t.Fatal("запрос фото не сохранён")
	}
	if err := h.uc.HandlePhoto(ctx, -1005, reqID, "file-1"); err != nil {
		t.Fatalf("фото: %v", err)
	}

	return h
}

// 15:00: альбом фото одним сообщением и пост-копия письма склада; id поста и
// числа по акции ложатся в историю.
func TestPublishShopSendsPhotosAndPost(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}

	if len(h.chat.shopPhotos) != 1 || len(h.chat.shopPhotos[0]) != 1 || h.chat.shopPhotos[0][0] != "file-1" {
		t.Fatalf("альбом фото: %+v, want [file-1]", h.chat.shopPhotos)
	}
	if len(h.chat.shopTexts) != 1 {
		t.Fatalf("постов %d, want один", len(h.chat.shopTexts))
	}
	text := h.chat.shopTexts[0]
	if !strings.Contains(text, "Колбаса") || !strings.Contains(text, "Доступно по акции: 100 шт.") {
		t.Errorf("пост: %q", text)
	}
	if h.repo.digest.ShopMessageID == 0 {
		t.Error("id поста не сохранён")
	}
	if it := h.repo.digest.Items[0]; !it.Listed || it.ShopQty != 100 {
		t.Errorf("позиция поста %+v, want listed и 100 шт.", it)
	}

	// Повторный проход в окне публикации второй пост не шлёт.
	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("повторная публикация: %v", err)
	}
	if len(h.chat.shopTexts) != 1 {
		t.Errorf("постов %d, want один (повтор запрещён)", len(h.chat.shopTexts))
	}
}

// Продажа уменьшает остаток по акции, ноль ставит метку «ПРОДАНО» перед
// названием, возврат её снимает.
func TestSyncShopPostSaleAndSoldMark(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}

	h.repo.inputs[0].Qty = 97
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("правка: %v", err)
	}
	if last := h.chat.shopEdits[len(h.chat.shopEdits)-1]; !strings.Contains(last.Text, "97 шт.") {
		t.Errorf("пост после продажи: %q, want 97 шт.", last.Text)
	} else if !strings.Contains(last.Text, "Доступно по акции: 100 шт.") {
		t.Errorf("продажа сдвинула «доступно»: %q", last.Text)
	}

	h.repo.inputs[0].Qty = 0
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+2*time.Minute)); err != nil {
		t.Fatalf("правка (ноль): %v", err)
	}
	last := h.chat.shopEdits[len(h.chat.shopEdits)-1]
	if !strings.Contains(last.Text, discounts.ShopSoldMark) {
		t.Errorf("пост без метки продано: %q", last.Text)
	}
	if !strings.Contains(last.Text, "Остаток по акции: 0 шт.") {
		t.Errorf("пост без нулевого остатка: %q", last.Text)
	}
	if !strings.Contains(last.Text, "Доступно по акции: 100 шт.") {
		t.Errorf("ноль сдвинул «доступно»: %q", last.Text)
	}

	h.repo.inputs[0].Qty = 4
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+3*time.Minute)); err != nil {
		t.Fatalf("правка (возврат): %v", err)
	}
	last = h.chat.shopEdits[len(h.chat.shopEdits)-1]
	if strings.Contains(last.Text, discounts.ShopSoldMark) || !strings.Contains(last.Text, "Остаток по акции: 4 шт.") {
		t.Errorf("пост после возврата: %q, want остаток 4 шт. без метки", last.Text)
	}
	if !strings.Contains(last.Text, "Доступно по акции: 100 шт.") {
		t.Errorf("возврат сдвинул «доступно»: %q", last.Text)
	}
}

// Возврат сроковой позиции: скидка из поста сразу уходит на сайт и в ТГ-колонку
// (п. 4.4) — читатели её уже видели.
func TestSyncShopReturnRestoresExpiryDiscount(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26))) // 20 %
	ctx := context.Background()

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	h.repo.inputs[0].Qty = 0
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("правка (продано): %v", err)
	}

	before := len(h.writer.batches)
	h.repo.inputs[0].Qty = 5
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+2*time.Minute)); err != nil {
		t.Fatalf("правка (возврат): %v", err)
	}

	if len(h.writer.batches) != before+1 {
		t.Fatalf("батчей правок %d, want %d", len(h.writer.batches), before+1)
	}
	w := h.writer.batches[len(h.writer.batches)-1][0]
	if w.General == nil || *w.General != 20 || w.Telegram == nil || *w.Telegram != 20 {
		t.Errorf("возврат: %+v, want general=20 и telegram=20 (скидка из поста)", w)
	}
	if w.GeneralOwner != discounts.OwnerEscalation.String() {
		t.Errorf("владелец %q, want %q", w.GeneralOwner, discounts.OwnerEscalation)
	}
}

// Возврат избыточной позиции возвращается в пост только если избыток ещё есть:
// без оборота избытка нет — позиция остаётся «ПРОДАНО» и на сайт не пишется.
func TestSyncShopSurplusReturnNeedsSurplus(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
		lotInput("p2", "Сыр", day(11), 100), // избыток
	)
	// Оборот ДО плана: избыточная позиция ищется при сборке рассылки.
	h.turn.rates = map[string]float64{"p2": 150}
	ctx := context.Background()

	if err := h.uc.RunSlotPlan(ctx, h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	h.finalPass(t, 4)
	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	var surplus *discounts.DigestItem
	for i := range h.repo.digest.Items {
		if h.repo.digest.Items[i].ProductID == "p2" {
			surplus = &h.repo.digest.Items[i]
		}
	}
	if surplus == nil || surplus.Reason != discounts.ReasonSurplus {
		t.Fatalf("позиции избытка нет в посте: %+v", h.repo.digest.Items)
	}

	// Продали всё, потом вернулось 3 шт — но оборота больше нет (избытка нет).
	for i := range h.repo.inputs {
		if h.repo.inputs[i].ProductID == "p2" {
			h.repo.inputs[i].Qty = 0
		}
	}
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("правка (продано): %v", err)
	}
	h.turn.rates = map[string]float64{}
	for i := range h.repo.inputs {
		if h.repo.inputs[i].ProductID == "p2" {
			h.repo.inputs[i].Qty = 3
		}
	}
	before := len(h.writer.batches)
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+2*time.Minute)); err != nil {
		t.Fatalf("правка (возврат): %v", err)
	}

	for i := range h.repo.digest.Items {
		if it := h.repo.digest.Items[i]; it.ProductID == "p2" && it.ShopQty != 0 {
			t.Errorf("избыточная позиция вернулась без избытка: %+v", it)
		}
	}
	if len(h.writer.batches) != before {
		t.Errorf("на сайт ушла правка по избытку: %+v", h.writer.batches[before:])
	}
}

// Перед публикацией новой рассылки прошлый пост «подчищается»: все блоки в ноль
// и с меткой «ПРОДАНО», числа нуля пишутся и в историю.
func TestPublishShopClosesPreviousPost(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	const prevID int64 = 77
	h.repo.prevShop = &discounts.DigestState{
		PlannedAt:     h.now.AddDate(0, 0, -2),
		ChatKind:      discounts.ChatWarehouse,
		ShopMessageID: prevID,
		Items: []discounts.DigestItem{{
			ProductID:     "p1",
			BestBefore:    day(9),
			Percent:       20,
			Reason:        discounts.ReasonExpiry,
			BlockQty:      100,
			StockQty:      100,
			ShopQty:       4,
			ShopAvailable: 4,
			Listed:        true,
		}},
	}

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}

	var closed *fakeEdit
	for i := range h.chat.shopEdits {
		if h.chat.shopEdits[i].ID == prevID {
			closed = &h.chat.shopEdits[i]
		}
	}
	if closed == nil {
		t.Fatalf("прошлый пост не подчищен: правки %+v", h.chat.shopEdits)
	}
	if !strings.Contains(closed.Text, discounts.ShopSoldMark) {
		t.Errorf("подчистка без метки продано: %q", closed.Text)
	}
	if !strings.Contains(closed.Text, "Остаток по акции: 0 шт.") {
		t.Errorf("подчистка без нуля: %q", closed.Text)
	}
	if !strings.Contains(closed.Text, "Доступно по акции: 4 шт.") {
		t.Errorf("подчистка изменила «доступно»: %q", closed.Text)
	}
	if h.repo.prevShop.Items[0].ShopQty != 0 {
		t.Errorf("ноль не записан в историю: %+v", h.repo.prevShop.Items[0])
	}
	if len(h.chat.shopTexts) != 1 {
		t.Errorf("новый пост не отправлен: %d", len(h.chat.shopTexts))
	}
}

// Лот, распроданный до нуля, сток УДАЛЯЕТ (`DELETE FROM product_stock` в
// списании подбора): строки в снапшоте остатков нет. Блок поста при этом обязан
// остаться с названием, единицей и ценой — «ПРОДАНО» ставится ПЕРЕД названием,
// а не вместо него (инцидент 22.10.2026: «1. ❗️ПРОДАНО❗️  | 30%»).
func TestSyncShopPostKeepsNameWhenLotDeleted(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Стейк Филе-Миньон", day(9), 3, shelfLifeInput(26),
		catalogInput(529000, "шт", "")))
	ctx := context.Background()

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}

	h.repo.inputs = nil // подбор списал лот до нуля — пары в остатках больше нет
	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("правка: %v", err)
	}

	if len(h.chat.shopEdits) == 0 {
		t.Fatal("пост не отредактирован: продажа не замечена")
	}
	last := h.chat.shopEdits[len(h.chat.shopEdits)-1].Text
	if !strings.Contains(last, discounts.ShopSoldMark+" Стейк Филе-Миньон") {
		t.Errorf("метка «продано» не перед названием: %q", last)
	}
	if !strings.Contains(last, "5290,00 руб./шт.") {
		t.Errorf("в блоке нет цены: %q", last)
	}
	if !strings.Contains(last, "Остаток по акции: 0 шт.") {
		t.Errorf("нет нулевого остатка: %q", last)
	}
}

// Событие стока будит правку поста: подбор обнулил лот — пост правится сразу, а
// не минутным тиком. Пачка событий не копится (канал на один сигнал), и запись
// остатков на сигнале не блокируется.
func TestOnLotsChangedKicksShopPost(t *testing.T) {
	h := newSlotHarness(recalcNow(1), lotInput("p1", "Колбаса", day(9), 10, shelfLifeInput(26)))

	if err := h.uc.OnLotsChanged(context.Background(), "p1", nil); err != nil {
		t.Fatalf("событие стока: %v", err)
	}
	select {
	case <-h.uc.shopKick:
	default:
		t.Fatal("событие стока не разбудило правку поста")
	}

	for range 5 {
		if err := h.uc.OnLotsChanged(context.Background(), "p1", nil); err != nil {
			t.Fatalf("событие стока: %v", err)
		}
	}
}

// Снимок карточки блока живёт в истории рассылки, а не в памяти процесса: после
// рестарта (новый UseCase над тем же репозиторием) блок проданного лота
// сохраняет название, единицу и цену. Ровно ради этого карточка переехала из
// памяти в таблицу (решение владельца 22.10.2026).
func TestSyncShopPostKeepsNameAfterRestart(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Стейк Филе-Миньон", day(9), 3, shelfLifeInput(26),
		catalogInput(529000, "шт", "")))
	ctx := context.Background()

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	card := h.repo.digest.Items[0].Shop
	if card.Name != "Стейк Филе-Миньон" || card.UOM != "шт" || card.SalePrice == nil || *card.SalePrice != 529000 {
		t.Fatalf("карточка не сохранена в истории рассылки: %+v", card)
	}

	// Рестарт: память процесса новая (снимка карточек в ней нет), история — та же.
	restarted := NewUseCase(h.repo, h.turn, h.writer, h.common, h.tasks, h.chat, func() time.Time { return h.now })
	h.repo.inputs = nil // подбор списал лот до нуля: пара удалена из остатков
	if err := restarted.syncShopPost(ctx, h.now.Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("правка после рестарта: %v", err)
	}

	if len(h.chat.shopEdits) == 0 {
		t.Fatal("пост не отредактирован после рестарта")
	}
	last := h.chat.shopEdits[len(h.chat.shopEdits)-1].Text
	if !strings.Contains(last, discounts.ShopSoldMark+" Стейк Филе-Миньон") {
		t.Errorf("после рестарта блок без названия: %q", last)
	}
	if !strings.Contains(last, "5290,00 руб./шт.") {
		t.Errorf("после рестарта блок без цены: %q", last)
	}
}

// Пост старой рассылки удалили из канала: подчищать нечего, но и это не
// повод не публиковать сегодняшнюю — раньше ошибка правки выходила наружу ДО
// отправки нового поста, и день оставался без поста (выкатка на канал 30.10.2026).
func TestPublishShopSurvivesGonePreviousPost(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	h.repo.prevShop = &discounts.DigestState{
		PlannedAt:     h.now.AddDate(0, 0, -2),
		ChatKind:      discounts.ChatWarehouse,
		ShopMessageID: 77,
		Items: []discounts.DigestItem{{
			ProductID:     "p1",
			BestBefore:    day(9),
			Percent:       20,
			Reason:        discounts.ReasonExpiry,
			BlockQty:      100,
			StockQty:      100,
			ShopQty:       4,
			ShopAvailable: 4,
			Listed:        true,
		}},
	}
	h.chat.shopEditErr = fmt.Errorf("%w: Bad Request: message to edit not found", discounts.ErrShopPostGone)

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация при удалённом прошлом посте: %v", err)
	}
	if len(h.chat.shopTexts) != 1 {
		t.Fatalf("постов %d, want один: удалённый прошлый съел рассылку", len(h.chat.shopTexts))
	}
	if h.repo.digest.ShopMessageID == 0 {
		t.Error("id нового поста не сохранён")
	}
}

// Пост удалили из канала, а остатки изменились: правок в мёртвый id больше нет.
// Склад получает уведомление, в канал уходит НОВЫЙ пост с текущими числами и
// становится постом дня; альбом фото не переотправляется (решение владельца
// 30.10.2026 — не восстанавливать прежнее сообщение, а делать новый пост).
func TestSyncShopPostRepublishesGonePost(t *testing.T) {
	h := publishHarness(t, lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	if err := h.uc.publishShop(ctx, h.now.Add(time.Hour)); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	first := h.repo.digest.ShopMessageID

	h.repo.inputs[0].Qty = 97
	h.chat.shopEditErr = fmt.Errorf("%w: Bad Request: message to edit not found", discounts.ErrShopPostGone)

	if err := h.uc.syncShopPost(ctx, h.now.Add(time.Hour+time.Minute)); err != nil {
		t.Fatalf("правка при удалённом посте: %v", err)
	}
	if len(h.chat.shopTexts) != 2 {
		t.Fatalf("новый пост не опубликован: постов %d", len(h.chat.shopTexts))
	}
	if !strings.Contains(h.chat.shopTexts[1], "97 шт.") {
		t.Errorf("новый пост без свежих чисел: %q", h.chat.shopTexts[1])
	}
	if h.repo.digest.ShopMessageID == first || h.repo.digest.ShopMessageID == 0 {
		t.Errorf("id поста не переписан: был %d, стал %d", first, h.repo.digest.ShopMessageID)
	}
	notified := false
	for _, text := range h.chat.texts {
		if strings.Contains(text, "не найден") {
			notified = true
		}
	}
	if !notified {
		t.Errorf("склад не уведомлён: %q", h.chat.texts)
	}
	if len(h.chat.shopPhotos) != 1 {
		t.Errorf("альбом переотправлен: %d", len(h.chat.shopPhotos))
	}
}

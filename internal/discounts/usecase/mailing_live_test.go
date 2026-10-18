package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// ---- окно 14:45–15:00: дельты по снимку блока ----

// Сценарии владельца по хлебу (5 шт до ближнего срока + 1 шт до дальнего):
// пересчёта избытка в окне нет, есть только дельты остатка группы. Ближний блок
// забирает объём первым, поэтому обрезка идёт с дальнего конца.
func TestDisplayedQtySurplusScenarios(t *testing.T) {
	items := []discounts.DigestItem{
		{ProductID: "bread", BestBefore: day(9), Reason: discounts.ReasonSurplus, BlockQty: 5, StockQty: 5},
		{ProductID: "bread", BestBefore: day(10), Reason: discounts.ReasonSurplus, BlockQty: 1, StockQty: 1},
	}
	key29 := lotKey("bread", day(9))
	key30 := lotKey("bread", day(10))

	tests := []struct {
		title          string
		qty29, qty30   int64
		want29, want30 int64
	}{
		{"14:45", 5, 1, 5, 1},
		{"С1: -2 у ближнего", 3, 1, 3, 1},
		{"С2: -1 у дальнего", 5, 0, 5, 0},
		{"С3: С2 + возврат", 5, 1, 5, 1},
		{"ближний распродан", 0, 1, 0, 1},
		{"всё распродано", 0, 0, 0, 0},
		{"возврат выше снимка", 0, 2, 0, 1},
		{"приёмка выше снимка", 7, 1, 5, 1},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			data := map[discounts.LotKey]lotData{
				key29: {Qty: tt.qty29},
				key30: {Qty: tt.qty30},
			}

			got := displayedQty(items, data)
			if got[key29] != tt.want29 || got[key30] != tt.want30 {
				t.Errorf("блоки %d/%d, want %d/%d", got[key29], got[key30], tt.want29, tt.want30)
			}
		})
	}
}

// Возврат не поднимает блок выше снимка 14:45: больше, чем объявлено в рассылке,
// в окне не появляется.
func TestDisplayedQtyReturnDoesNotGrowAboveFrozen(t *testing.T) {
	key := lotKey("p1", day(9))
	items := []discounts.DigestItem{{ProductID: "p1", BestBefore: day(9), Reason: discounts.ReasonExpiry, BlockQty: 10, StockQty: 10}}

	got := displayedQty(items, map[discounts.LotKey]lotData{key: {Qty: 25}})
	if got[key] != 10 {
		t.Errorf("блок %d, want 10 (не больше снимка 14:45)", got[key])
	}
}

// ---- пересборка письма ----

// Пересборка правит ТО ЖЕ сообщение: позиция, выбывшая из плана, пропадает из
// текста, а её ТГ-колонка снимается (иначе 16:00 поднял бы сайт по позиции,
// которой в рассылке нет).
func TestRefreshMailingEditsSameMessageAndClearsTG(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26)),
	)
	ctx := context.Background()

	if err := h.uc.RunSlotPlan(ctx, h.now, 2); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	msgID := h.repo.digest.MessageID
	if msgID == 0 {
		t.Fatal("id письма не сохранён в истории")
	}

	// Менеджер поставил p1 скидку сайта 20 %: повышения больше нет — позиция
	// выбывает из плана.
	twenty := int16(20)
	h.repo.inputs[0].GeneralPlain = &twenty

	if err := h.uc.refreshMailing(ctx, h.now.Add(5*time.Minute), 2, false); err != nil {
		t.Fatalf("пересборка: %v", err)
	}

	if len(h.chat.edits) != 1 || h.chat.edits[0].ID != msgID {
		t.Fatalf("правки %+v, want одну правку сообщения %d", h.chat.edits, msgID)
	}
	if strings.Contains(h.chat.edits[0].Text, "Колбаса") {
		t.Errorf("выбывшая позиция осталась в письме: %q", h.chat.edits[0].Text)
	}
	if !strings.Contains(h.chat.edits[0].Text, "Сыр") {
		t.Errorf("письмо без оставшейся позиции: %q", h.chat.edits[0].Text)
	}

	var cleared bool
	for _, w := range h.writer.batches[len(h.writer.batches)-1] {
		if w.ProductID == "p1" && w.Telegram != nil && *w.Telegram == 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("ТГ-колонка выбывшей позиции не снята: %+v", h.writer.batches)
	}
}

// ---- окно количеств ----

// Продажа уменьшает блоки, ноль убирает блок и его запрос фото, возврат
// возвращает блок и запрос.
func TestSyncMailingQtyWindow(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
	)
	ctx := context.Background()

	if err := h.uc.RunSlotPlan(ctx, h.now, 2); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	h.finalPass(t, 2)

	if len(h.chat.requests) != 1 {
		t.Fatalf("запросы фото %q, want один", h.chat.requests)
	}
	reqID := h.repo.digest.Items[0].MessageID
	if reqID == 0 {
		t.Fatal("id запроса фото не сохранён")
	}

	// Продали 1 шт: блок уменьшился, запрос фото на месте.
	h.repo.inputs[0].Qty = 99
	if err := h.uc.syncMailingQty(ctx, h.now.Add(46*time.Minute), h.now); err != nil {
		t.Fatalf("окно количеств: %v", err)
	}
	if last := h.chat.edits[len(h.chat.edits)-1]; !strings.Contains(last.Text, "99 шт.") {
		t.Errorf("письмо после продажи: %q, want 99 шт.", last.Text)
	}
	if len(h.chat.deleted) != 0 {
		t.Errorf("запрос фото удалён при живой позиции: %v", h.chat.deleted)
	}

	// Продали всё: блок уходит, запрос фото удаляется.
	h.repo.inputs[0].Qty = 0
	if err := h.uc.syncMailingQty(ctx, h.now.Add(47*time.Minute), h.now); err != nil {
		t.Fatalf("окно количеств (ноль): %v", err)
	}
	if last := h.chat.edits[len(h.chat.edits)-1]; strings.Contains(last.Text, "Колбаса") {
		t.Errorf("распроданный блок остался в письме: %q", last.Text)
	}
	if len(h.chat.deleted) != 1 || h.chat.deleted[0] != reqID {
		t.Errorf("удаления %v, want запрос %d", h.chat.deleted, reqID)
	}

	// Вернули 3 шт: блок и запрос фото вернулись.
	h.repo.inputs[0].Qty = 3
	if err := h.uc.syncMailingQty(ctx, h.now.Add(48*time.Minute), h.now); err != nil {
		t.Fatalf("окно количеств (возврат): %v", err)
	}
	if last := h.chat.edits[len(h.chat.edits)-1]; !strings.Contains(last.Text, "3 шт.") {
		t.Errorf("письмо после возврата: %q, want 3 шт.", last.Text)
	}
	if len(h.chat.requests) != 2 {
		t.Errorf("запросы фото %d, want два (возврат блока — новый запрос)", len(h.chat.requests))
	}
}

// ---- пропуск рассылки ----

// Пропуск поднимает на сайт только то, что живёт без рассылки: свою ступень по
// сроку и ручную ТГ. Добор до скидки дня и избыток — нет. ТГ-колонка снимается,
// история рассылки удаляется (лоты остаются доступны следующей рассылке).
func TestSkipMailingRaisesOnlyOwnStepAndManualTG(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)), // своя ступень 20 %
		lotInput("p2", "Сыр", day(11), 100),                        // избыток (добор)
		lotInput("p3", "Творог", day(20), 10, func(in *discounts.Input) { // ручная ТГ вне плана
			tg := int16(25)
			in.TelegramManual = &tg
		}),
	)
	h.turn.rates = map[string]float64{"p2": 150} // 5 шт/день → избыток
	ctx := context.Background()

	if err := h.uc.RunSlotPlan(ctx, h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	msgID := h.repo.digest.MessageID

	if err := h.uc.SkipMailing(ctx, "cb-1", -1005, msgID); err != nil {
		t.Fatalf("пропуск: %v", err)
	}

	if h.chat.acked != 1 || len(h.chat.alerts) != 0 {
		t.Errorf("кнопка: ack=%d, алерты %v", h.chat.acked, h.chat.alerts)
	}
	if h.repo.digest != nil {
		t.Errorf("история рассылки не удалена: %+v", h.repo.digest)
	}
	if last := h.chat.edits[len(h.chat.edits)-1]; last.Text != discounts.MailingSkippedText || last.Label != "" {
		t.Errorf("письмо после пропуска: %q кнопка %q", last.Text, last.Label)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagRaise)] {
		t.Error("маркер подъёма не поставлен: 16:00 повторит работу")
	}

	generals := map[string]int16{}
	for _, w := range h.writer.batches[len(h.writer.batches)-1] {
		if w.General != nil {
			generals[w.ProductID] = *w.General
		}
	}
	if got, ok := generals["p1"]; !ok || got != 20 {
		t.Errorf("своя ступень p1: general=%d (есть %v), want 20", got, ok)
	}
	if _, ok := generals["p2"]; ok {
		t.Errorf("избыток p2 поднят на сайт: %v", generals)
	}
	if got, ok := generals["p3"]; !ok || got != 25 {
		t.Errorf("ручная ТГ p3: general=%d (есть %v), want 25", got, ok)
	}
}

// Пропуск после публикации (15:00) не проходит: кнопка отвечает причиной отказа.
func TestSkipMailingAfterPublishRefused(t *testing.T) {
	h := newSlotHarness(recalcNow(1), lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	if err := h.uc.RunSlotPlan(ctx, h.now, 2); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	msgID := h.repo.digest.MessageID

	if err := h.uc.SkipMailing(ctx, "cb-1", -1005, msgID); err != nil {
		t.Fatalf("пропуск: %v", err)
	}
	h.uc.setPlanTime(h.now.Add(-2 * time.Hour)) // имитация: рассылка уже ушла

	if err := h.uc.SkipMailing(ctx, "cb-2", -1005, msgID); err != nil {
		t.Fatalf("повторный пропуск: %v", err)
	}
	if len(h.chat.alerts) == 0 {
		t.Error("отказ после публикации не показан")
	}
}

// ---- фото ----

// Фото приходит ответом на сообщение-запрос: по reply_to находим позицию и
// держим file_id в памяти. Чужой reply — не наше фото, молчание.
func TestHandlePhotoByReply(t *testing.T) {
	h := newSlotHarness(recalcNow(1), lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	if err := h.uc.RunSlotPlan(ctx, h.now, 2); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	h.finalPass(t, 2)

	reqID := h.repo.digest.Items[0].MessageID
	if reqID == 0 {
		t.Fatal("запрос фото не сохранён в истории")
	}

	if err := h.uc.HandlePhoto(ctx, -1005, reqID, "file-1"); err != nil {
		t.Fatalf("фото: %v", err)
	}
	if got := h.uc.photo(lotKey(h.repo.digest.Items[0].ProductID, h.repo.digest.Items[0].BestBefore)); got != "file-1" {
		t.Errorf("file_id %q, want file-1", got)
	}
	if len(h.chat.texts) == 0 || !strings.Contains(h.chat.texts[len(h.chat.texts)-1], "принято") {
		t.Errorf("подтверждение не отправлено: %v", h.chat.texts)
	}

	if err := h.uc.HandlePhoto(ctx, -1005, 999999, "file-2"); err != nil {
		t.Fatalf("чужой reply: %v", err)
	}
	if got := h.uc.photo(lotKey(h.repo.digest.Items[0].ProductID, h.repo.digest.Items[0].BestBefore)); got != "file-1" {
		t.Errorf("чужой reply перезаписал фото: %q", got)
	}
}

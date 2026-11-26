package usecase

import (
	"context"
	"strings"
	"testing"

	"warehouseHelper/internal/discounts"
)

// Подтверждение фото после рестарта: кэш имён в памяти пуст, история рассылки
// осталась в БД — в чат уходит название товара, а не его id (26.11.2026).
func TestPhotoAcceptedNamesProductAfterRestart(t *testing.T) {
	h := newSlotHarness(recalcNow(1), lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)))
	ctx := context.Background()

	if err := h.uc.RunSlotPlan(ctx, h.now, 2); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	h.finalPass(t, 2)

	it := h.repo.digest.Items[0]
	// Рестарт: кэш имён пуст, история и лоты — на месте.
	h.uc.mailMu.Lock()
	h.uc.photoNames = make(map[discounts.LotKey]string)
	h.uc.mailMu.Unlock()

	if err := h.uc.HandlePhoto(ctx, -1005, it.MessageID, "file-1"); err != nil {
		t.Fatalf("фото: %v", err)
	}

	last := h.chat.mailing[len(h.chat.mailing)-1]
	if !strings.Contains(last, "Колбаса") {
		t.Errorf("в подтверждении нет названия: %q", last)
	}
	if strings.Contains(last, it.ProductID) {
		t.Errorf("в подтверждении id вместо названия: %q", last)
	}
}

package usecase

import (
	"context"
	"strings"
	"testing"
)

// Предпросмотр плана 14:00 (страница «Скидки»): текст тот же, что уйдёт складу в
// рассылке, но расчёт ничего не пишет — ни скидок, ни истории, ни маркеров дня.
func TestPlanPreviewDoesNotWrite(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Томагавк", day(13), 4, shelfLifeInput(40)), // D=12 → ступень 10 %, сайт пуст
	)

	prev, err := h.uc.PlanPreview(context.Background(), 10)
	if err != nil {
		t.Fatalf("PlanPreview: %v", err)
	}
	if prev.SlotCount != 1 || prev.ExtraCount != 0 {
		t.Errorf("раскладка плана: в слот %d, сразу на сайт %d, want 1 и 0", prev.SlotCount, prev.ExtraCount)
	}
	if !strings.Contains(prev.Text, "(ТГ) Томагавк (4 шт до 27.09) — 10%") {
		t.Errorf("текст предпросмотра:\n%s", prev.Text)
	}

	// Ничего не произошло: ни записи скидок, ни рассылки, ни маркеров дня.
	if len(h.writer.batches) != 0 {
		t.Errorf("предпросмотр записал скидки: %+v", h.writer.batches)
	}
	if len(h.repo.digests) != 0 || len(h.chat.texts) != 0 || h.repo.sent != 0 {
		t.Errorf("предпросмотр отправил рассылку: истории %d, сообщения %q, отметок %d",
			len(h.repo.digests), h.chat.texts, h.repo.sent)
	}
	if len(h.repo.flags) != 0 {
		t.Errorf("предпросмотр поставил маркеры дня: %+v", h.repo.flags)
	}
	if len(h.tasks.texts) != 0 {
		t.Errorf("предпросмотр открыл задачи: %q", h.tasks.texts)
	}
}

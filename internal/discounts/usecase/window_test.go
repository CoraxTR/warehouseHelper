package usecase

import (
	"context"
	"strings"
	"testing"
	"warehouseHelper/internal/discounts"
)

// flatWrites — все правки обоих проходов одним срезом (пустой батч в шов
// не уходит, поэтому батчи смотрим целиком).
func flatWrites(batches [][]discounts.DiscountWrite) []discounts.DiscountWrite {
	var out []discounts.DiscountWrite
	for _, b := range batches {
		out = append(out, b...)
	}
	return out
}

// hasTask — открыта ли задача с подстрокой текста.
func hasTask(texts []string, substr string) bool {
	for _, txt := range texts {
		if strings.Contains(txt, substr) {
			return true
		}
	}
	return false
}

// Окно ёмкости — граница канала сайта: ступень по сроку пишется только парам
// окна, остальные остаются кандидатами («скидка в памяти»): в БД у них ничего
// нет, но в дайджесте и /скидки они видны в «Доступно для допродажи».
func TestWindowExpiryWritesOnlyWindowPairs(t *testing.T) {
	now := recalcNow(5) // суббота: КТ-день, не ТГ-день
	h := newRecalcHarness(now,
		lotInput("near", "Ближний", day(9), 10, shelfLifeInput(26)),
		lotInput("mid", "Средний", day(11), 10, shelfLifeInput(26)),
		lotInput("far", "Дальний", day(13), 10, shelfLifeInput(26)),
	)
	h.uc.SetWindowCap(1)

	if err := h.uc.RecalcExpiry(context.Background(), now); err != nil {
		t.Fatalf("пересчёт по сроку: %v", err)
	}

	writes := flatWrites(h.batches())
	if len(writes) != 1 {
		t.Fatalf("правок %d, want 1 (только пара окна): %+v", len(writes), writes)
	}
	if writes[0].ProductID != "near" {
		t.Errorf("значение получила пара %q, want near: окно — первый по сроку", writes[0].ProductID)
	}
	if writes[0].General == nil || *writes[0].General <= 0 {
		t.Fatalf("значение окна %v, want ступень по сроку", writes[0].General)
	}

	// За окном значения в БД нет, но позиции видны в «Доступно для допродажи».
	d := h.uc.Digest(h.uc.WindowCap())
	if len(d.Discounts) != 1 {
		t.Fatalf("активных строк %d, want 1: %+v", len(d.Discounts), d.Discounts)
	}
	if len(d.Surplus) != 2 {
		t.Fatalf("строк допродажи %d, want 2: %+v", len(d.Surplus), d.Surplus)
	}
	for _, row := range d.Surplus {
		if row.Percent <= 0 {
			t.Errorf("допродажа %q без скидки: %+v — кандидат должен быть виден", row.Name, row)
		}
	}
}

// Пара ВОШЛА в окно не в КТ-день: сайт получает её ступень сразу (скидка
// «зафиксирована» в день лесенки и ждала места), человек — уведомление.
func TestWindowEntryOutsideExpiryDayWritesAndNotifies(t *testing.T) {
	now := recalcNow(9) // среда: не КТ-день, не ТГ-день
	h := newRecalcHarness(now,
		lotInput("a", "Ближний", day(11), 10, shelfLifeInput(26)),
		lotInput("b", "Дальний", day(13), 10, shelfLifeInput(26)),
	)
	h.uc.SetWindowCap(1)

	// Первый проход — база сравнения и окно {a}: значения не пишутся.
	if err := h.uc.RecalcSurplus(context.Background(), now); err != nil {
		t.Fatalf("часовой пересчёт (наполнение): %v", err)
	}
	h.writer.batches, h.tasks.texts = nil, nil

	// Ближний лот распродан: пара ушла из снапшота, место в окне освободилось.
	h.repo.inputs = h.repo.inputs[1:]
	if err := h.uc.RecalcSurplus(context.Background(), now); err != nil {
		t.Fatalf("часовой пересчёт (вход в окно): %v", err)
	}

	writes := flatWrites(h.batches())
	if len(writes) != 1 {
		t.Fatalf("правок %d, want 1 (ступень вошедшей в окно пары): %+v", len(writes), writes)
	}
	if writes[0].ProductID != "b" || writes[0].General == nil {
		t.Fatalf("правка %+v, want значение пары b: вход в окно даёт ступень", writes[0])
	}
	if writes[0].GeneralOwner != discounts.OwnerExpiry.String() {
		t.Errorf("владелец %q, want %q", writes[0].GeneralOwner, discounts.OwnerExpiry.String())
	}
	if !hasTask(h.tasks.texts, "Поставить скидку") {
		t.Errorf("уведомления нет: %v, want «Поставить скидку» на вошедшую в окно позицию", h.tasks.texts)
	}
}

// Пара ВЫШЛА из окна (её вытеснила ручная скидка другого товара): наше значение
// снимается с сайта, человеку уходит «Убрать скидку».
func TestWindowExitRemovesValueAndNotifies(t *testing.T) {
	now := recalcNow(5) // суббота: КТ-день
	h := newRecalcHarness(now,
		lotInput("a", "Первый", day(9), 10, shelfLifeInput(26)),
		lotInput("b", "Второй", day(11), 10, shelfLifeInput(26)),
	)
	h.uc.SetWindowCap(1)

	if err := h.uc.RecalcExpiry(context.Background(), now); err != nil {
		t.Fatalf("пересчёт по сроку: %v", err)
	}
	if got := flatWrites(h.batches()); len(got) != 1 || got[0].ProductID != "a" {
		t.Fatalf("наполнение: правки %+v, want значение пары a", got)
	}
	h.writer.batches, h.tasks.texts = nil, nil

	// Ручная скидка второго товара важнее ступени — первый уходит за окно.
	manual := int16(30)
	h.repo.inputs[1].GeneralManual = &manual

	if err := h.uc.RecalcSurplus(context.Background(), now); err != nil {
		t.Fatalf("часовой пересчёт (выход из окна): %v", err)
	}

	var removal *discounts.DiscountWrite
	for _, w := range flatWrites(h.batches()) {
		if w.ProductID == "a" && w.General == nil {
			removal = &w
		}
	}
	if removal == nil {
		t.Fatalf("снятия значения у вытесненной пары нет: %+v", flatWrites(h.batches()))
	}
	if !hasTask(h.tasks.texts, "Убрать скидку") {
		t.Errorf("уведомления нет: %v, want «Убрать скидку» на ушедшую из окна позицию", h.tasks.texts)
	}
}

// Изменение значения у пары за окном уведомлений не даёт: скидки на сайте там
// нет, человеку делать нечего. Значение по-прежнему видно в дайджесте.
func TestWindowQueueChangesStaySilent(t *testing.T) {
	now := recalcNow(5)
	h := newRecalcHarness(now,
		lotInput("near", "Ближний", day(9), 10, shelfLifeInput(26)),
		lotInput("far", "Дальний", day(13), 10, shelfLifeInput(26)),
	)
	h.uc.SetWindowCap(1)

	if err := h.uc.RecalcExpiry(context.Background(), now); err != nil {
		t.Fatalf("пересчёт по сроку: %v", err)
	}
	h.writer.batches, h.tasks.texts = nil, nil

	// Второй проход того же дня: у пары за окном появилось своё значение в
	// «памяти» от другого основания — сайта это не касается.
	h.repo.inputs[1].GeneralPlain = nil
	if err := h.uc.RecalcSurplus(context.Background(), now); err != nil {
		t.Fatalf("часовой пересчёт: %v", err)
	}

	if len(h.tasks.texts) != 0 {
		t.Errorf("уведомления по паре за окном: %v, want тишину", h.tasks.texts)
	}
	if len(h.batches()) != 0 {
		t.Errorf("правки по паре за окном: %+v, want пусто", flatWrites(h.batches()))
	}
	if d := h.uc.Digest(h.uc.WindowCap()); len(d.Surplus) != 1 || d.Surplus[0].Percent <= 0 {
		t.Errorf("допродажа: %+v, want пару за окном с кандидатом", d.Surplus)
	}
}

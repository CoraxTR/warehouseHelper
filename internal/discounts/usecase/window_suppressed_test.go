package usecase

import (
	"context"
	"testing"

	"warehouseHelper/internal/discounts"
)

// Инцидент 26.11.2026: подавленный вход (ТГ-день до финальной сборки) не должен
// запоминаться как «окно прошлого прохода». Иначе пара, которой скидку не
// выдали, навсегда считается «уже в окне» и значения не получает никогда:
// в плане было 12 позиций, а на сайте 9 значений.
func TestSuppressedEntryNotStoredAsWindow(t *testing.T) {
	repo := newFakeDiscountRepo()
	tg := telegramDayClock()

	uc := NewUseCase(repo, nil, nil, nil, nil, nil, tg)
	uc.SetWindowCap(12)

	a := entryPair("a", 10)
	b := entryPair("b", 20) // приоритетнее a — вытесняет его место

	// Первый проход: окно {a}, вход подавлен (ТГ-день), окно не запоминаем.
	if w := uc.windowedWrites(context.Background(), []PairState{a}, nil); len(w) != 0 {
		t.Fatalf("подавленный проход записал %d правок, want 0", len(w))
	}

	// Появляется b: он вошёл в окно, но вход всё ещё подавлен.
	if w := uc.windowedWrites(context.Background(), []PairState{a, b}, nil); len(w) != 0 {
		t.Fatalf("подавленный вход записал %d правок, want 0", len(w))
	}

	// Финальная сборка 14:45 прошла — вход открывается, b получает значение.
	if err := repo.MarkDayFlag(context.Background(), beginningOfDay(tg()), discounts.FlagExtras); err != nil {
		t.Fatalf("маркер финальной сборки: %v", err)
	}

	w := uc.windowedWrites(context.Background(), []PairState{a, b}, nil)
	var gotB bool
	for _, x := range w {
		if x.ProductID == "b" {
			gotB = true
		}
	}
	if !gotB {
		t.Fatalf("после сборки правок %+v: отложенного входа b нет", w)
	}
}

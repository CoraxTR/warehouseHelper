package usecase

import (
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// На ТГ-дне вход в окно не пишется: сайт получает скидку не раньше рассылки
// (14:00 план → 16:00 подъём). 26.11.2026: утренний проход четверга поднял
// сайт сразу, потому что вход в окно шёл вне зависимости от дня недели.
func TestWindowEntrySilentOnTelegramDay(t *testing.T) {
	sir := entryPair("sir", 12)
	other := entryPair("other", 20)

	if got := entryCounts(t, telegramDayClock(), sir, other); got != 0 {
		t.Fatalf("на ТГ-дне правок входа %d, want 0 (сайт поднимает план)", got)
	}
}

// Вне ТГ-дня вход в окно заполняет пустое место сразу — это и есть правило
// «освободилось место → скидка встаёт», ради которого вход заведён.
func TestWindowEntryWritesOutsideTelegramDay(t *testing.T) {
	sir := entryPair("sir", 12)
	other := entryPair("other", 20)

	if got := entryCounts(t, weekdayClock(), sir, other); got != 1 {
		t.Fatalf("вне ТГ-дня правок входа %d, want 1", got)
	}
}

// entryPair — пара окна без стоящего значения и со ступенью по сроку.
func entryPair(id string, days int) PairState {
	bb := testDay.AddDate(0, 0, days)

	return PairState{
		Key:        discounts.LotKey{ProductID: id, BestBefore: bb},
		ProductID:  id,
		Name:       id,
		BestBefore: bb,
		Qty:        5,
		DaysLeft:   days,
		Expiry:     new(int16(10)),
	}
}

// entryCounts — два прохода: первый запоминает окно с чужой парой, второй —
// с парой, вошедшей в окно; считаем правки входа во втором проходе.
func entryCounts(t *testing.T, now func() time.Time, sir, other PairState) int {
	t.Helper()

	uc := NewUseCase(nil, nil, nil, nil, nil, nil, now)
	uc.SetWindowCap(12)
	uc.windowedWrites([]PairState{other}, nil)
	writes := uc.windowedWrites([]PairState{other, sir}, nil)

	return len(writes)
}

// telegramDayClock — вторник: ТГ-день (вт/чт).
func telegramDayClock() func() time.Time {
	return func() time.Time { return time.Date(2026, time.November, 24, 10, 0, 0, 0, time.UTC) }
}

// weekdayClock — среда: ТГ-дня нет.
func weekdayClock() func() time.Time {
	return func() time.Time { return time.Date(2026, time.November, 25, 10, 0, 0, 0, time.UTC) }
}

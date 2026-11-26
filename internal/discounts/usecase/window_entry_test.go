package usecase

import (
	"context"
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

// Вне ТГ-дня вход в окно заполняет пустое место сразу.
func TestWindowEntryWritesOutsideTelegramDay(t *testing.T) {
	sir := entryPair("sir", 12)
	other := entryPair("other", 20)

	if got := entryCounts(t, weekdayClock(), sir, other); got != 1 {
		t.Fatalf("вне ТГ-дня правок входа %d, want 1", got)
	}
}

// Рестарт больше не теряет вход: окно прошлого прохода лежит в БД, поэтому
// пара, вошедшая в окно в момент выката, после перезапуска получает значение
// (инцидент 26.11.2026 — Сирлойн флэп ждал ступень сутки).
func TestWindowEntrySurvivesRestart(t *testing.T) {
	sir := entryPair("sir", 12)
	other := entryPair("other", 20)

	repo := newFakeDiscountRepo()
	repo.winKeys, repo.winOK = []discounts.LotKey{other.Key}, true

	uc := NewUseCase(repo, nil, nil, nil, nil, nil, weekdayClock())
	uc.SetWindowCap(12)

	writes := uc.windowedWrites(context.Background(), []PairState{other, sir}, nil)
	if len(writes) != 1 {
		t.Fatalf("после рестарта правок входа %d, want 1 (окно из БД)", len(writes))
	}
	if writes[0].ProductID != "sir" || writes[0].General == nil || *writes[0].General != 10 {
		t.Fatalf("правка не та: %+v", writes[0])
	}
}

// Свежая установка после миграции: строки в БД нет — первый проход тихий (иначе
// пачкой проставились бы ступени дня), но окно он уже сохраняет, и следующий
// проход считает вход честно.
func TestWindowEntryFreshInstallSilentThenWorks(t *testing.T) {
	sir := entryPair("sir", 12)
	other := entryPair("other", 20)

	repo := newFakeDiscountRepo()
	uc := NewUseCase(repo, nil, nil, nil, nil, nil, weekdayClock())
	uc.SetWindowCap(12)

	if writes := uc.windowedWrites(context.Background(), []PairState{other}, nil); len(writes) != 0 {
		t.Fatalf("первый проход без прошлого окна: правок %d, want 0", len(writes))
	}
	if !repo.winOK || repo.winSaves != 1 {
		t.Fatalf("окно не сохранено: ok=%v saves=%d", repo.winOK, repo.winSaves)
	}

	if writes := uc.windowedWrites(context.Background(), []PairState{other, sir}, nil); len(writes) != 1 {
		t.Fatalf("следующий проход: правок %d, want 1 (пара вошла в окно)", len(writes))
	}
}

// Окно из БД читается один раз за процесс: правка окна в памяти не сбрасывает
// прочитанное, но и не перечитывает таблицу на каждом проходе.
func TestWindowStateLoadedOnce(t *testing.T) {
	other := entryPair("other", 20)

	repo := newFakeDiscountRepo()
	repo.winKeys, repo.winOK = []discounts.LotKey{other.Key}, true

	uc := NewUseCase(repo, nil, nil, nil, nil, nil, weekdayClock())
	uc.SetWindowCap(12)

	uc.windowedWrites(context.Background(), []PairState{other}, nil)
	prev, seeded := uc.windowState(context.Background())
	if !seeded {
		t.Fatal("окно из БД не прочитано")
	}
	if _, ok := prev[other.Key]; !ok {
		t.Fatal("в прочитанном окне нет пары")
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

// entryCounts — два прохода одного процесса: первый запоминает окно с чужой
// парой, второй — с парой, вошедшей в окно; считаем правки входа.
func entryCounts(t *testing.T, now func() time.Time, sir, other PairState) int {
	t.Helper()

	uc := NewUseCase(nil, nil, nil, nil, nil, nil, now)
	uc.SetWindowCap(12)
	uc.windowedWrites(context.Background(), []PairState{other}, nil)
	writes := uc.windowedWrites(context.Background(), []PairState{other, sir}, nil)

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

// После финальной сборки 14:45 ТГ-дня окно снова добирается: освободившееся
// место занимает кандидат дня в пределах ёмкости (решение владельца 26.11.2026)
// — сайт при этом рассылку уже не обгоняет, новый список применён.
func TestWindowEntryWritesAfterExtrasOnTelegramDay(t *testing.T) {
	sir := entryPair("sir", 12)
	other := entryPair("other", 20)

	now := telegramDayClock()
	repo := newFakeDiscountRepo()
	if err := repo.MarkDayFlag(context.Background(), beginningOfDay(now()), discounts.FlagExtras); err != nil {
		t.Fatalf("маркер финальной сборки: %v", err)
	}

	uc := NewUseCase(repo, nil, nil, nil, nil, nil, now)
	uc.SetWindowCap(12)
	uc.windowedWrites(context.Background(), []PairState{other}, nil)
	writes := uc.windowedWrites(context.Background(), []PairState{other, sir}, nil)

	if len(writes) != 1 {
		t.Fatalf("после финальной сборки правок входа %d, want 1", len(writes))
	}
}

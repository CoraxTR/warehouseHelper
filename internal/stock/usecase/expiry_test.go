package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/stock"
)

// freezeLots — пары, снятые с учёта, как их ждёт склад: срок ≤ границы,
// физический остаток есть, порядок — группа/название.
func TestRemoveExpiringLots(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	pub := &mockPub{}
	notify := &mockNotifier{}
	uc := NewStockUseCase(repo, pub, nil, notify)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Граница 05.09 включающая: уходят пары 01.09, 03.09 (Молоко), 05.09;
	// 10.09 остаётся.
	frozen, err := uc.RemoveExpiringLots(context.Background(), d(2026, 9, 5))
	if err != nil {
		t.Fatalf("RemoveExpiringLots: %v", err)
	}

	if len(frozen) != 3 {
		t.Fatalf("frozen = %d, want 3: %+v", len(frozen), frozen)
	}

	// Одна запись на товар, в ней — только сроки ≤ границы.
	if len(repo.writes) != 2 {
		t.Fatalf("writes = %d, want 2: %+v", len(repo.writes), repo.writes)
	}
	for _, w := range repo.writes {
		if len(w.Upserts) != 0 {
			t.Errorf("upserts в уборке = %d, want 0", len(w.Upserts))
		}
	}

	// Кэш: пары ≤ 05.09 ушли, дальний срок 10.09 на месте.
	if got := lotQty(t, uc, "p1", d(2026, 9, 1)); got != -1 {
		t.Errorf("лот 01.09 остался в кэше (qty = %d)", got)
	}
	if got := lotQty(t, uc, "p1", d(2026, 9, 5)); got != -1 {
		t.Errorf("лот 05.09 остался в кэше (qty = %d)", got)
	}
	if got := lotQty(t, uc, "p1", d(2026, 9, 10)); got != 10 {
		t.Errorf("лот 10.09 qty = %d, want 10 (дальний срок не трогаем)", got)
	}
	if got := lotQty(t, uc, "p2", d(2026, 9, 3)); got != -1 {
		t.Errorf("лот Молоко 03.09 остался в кэше (qty = %d)", got)
	}

	// События: по одному lot_delete на пару — открытые «Сроки» обновятся сами.
	if len(pub.events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(pub.events), pub.events)
	}
	for _, e := range pub.events {
		if e.Kind != stock.EventLotDelete {
			t.Errorf("event = %+v, want lot_delete", e)
		}
	}

	// Сообщение складу: порядок группа/название, нумерация с 1, формат владельца.
	want := "Позиции убраны из сроков, нужно заморозить:\n" +
		"1. 7 шт. Молоко до 03.09.2026\n" +
		"2. 2 шт. Хлеб бородинский до 01.09.2026\n" +
		"3. 5 шт. Хлеб бородинский до 05.09.2026"
	if len(notify.texts) != 1 || notify.texts[0] != want {
		t.Errorf("notify.texts = %q\nwant %q", notify.texts, want)
	}
}

// TestRemoveExpiringLotsZeroQty — нулевой остаток убирается молча: морозить
// нечего, в список он не попадает (решение владельца 21.10.2026).
func TestRemoveExpiringLotsZeroQty(t *testing.T) {
	ttl := d(2026, 8, 1)
	repo := &mockRepo{products: []stock.Product{{
		ID: "p1", InternalCode: "10100001", Name: "Хлеб бородинский", GroupName: "Хлебобулочные",
		Lots: []stock.Lot{
			{BestBefore: d(2026, 9, 2), Qty: 0, ProducedOn: &ttl},
			{BestBefore: d(2026, 9, 30), Qty: 4},
		},
	}}}
	notify := &mockNotifier{}
	uc := NewStockUseCase(repo, &mockPub{}, nil, notify)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	frozen, err := uc.RemoveExpiringLots(context.Background(), d(2026, 9, 3))
	if err != nil {
		t.Fatalf("RemoveExpiringLots: %v", err)
	}
	if len(frozen) != 0 {
		t.Errorf("frozen = %+v, want пусто (остатка нет)", frozen)
	}
	if got := lotQty(t, uc, "p1", d(2026, 9, 2)); got != -1 {
		t.Errorf("нулевой лот 02.09 остался в кэше (qty = %d)", got)
	}
	if len(notify.texts) != 0 {
		t.Errorf("notify.texts = %q, want пусто", notify.texts)
	}
}

// TestRemoveExpiringLotsNothingDue — нечего убирать: ни записи, ни событий,
// ни сообщения (тик после 20:00 при пустых сроках — норма).
func TestRemoveExpiringLotsNothingDue(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	pub := &mockPub{}
	notify := &mockNotifier{}
	uc := NewStockUseCase(repo, pub, nil, notify)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	frozen, err := uc.RemoveExpiringLots(context.Background(), d(2026, 8, 31))
	if err != nil {
		t.Fatalf("RemoveExpiringLots: %v", err)
	}
	if len(frozen) != 0 || repo.replaceCalls != 0 || len(pub.events) != 0 || len(notify.texts) != 0 {
		t.Errorf("ничего не должно происходить: frozen=%v replaceCalls=%d events=%d texts=%q",
			frozen, repo.replaceCalls, len(pub.events), notify.texts)
	}
}

// TestRemoveExpiringLotsRepoError — БД не приняла запись: кэш и чат не трогаем,
// ошибка наружу (ретрай на следующем тике уборки).
func TestRemoveExpiringLotsRepoError(t *testing.T) {
	repo := &mockRepo{products: testStock(), writeErr: errors.New("db down")}
	notify := &mockNotifier{}
	uc := NewStockUseCase(repo, &mockPub{}, nil, notify)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	if _, err := uc.RemoveExpiringLots(context.Background(), d(2026, 9, 5)); err == nil {
		t.Fatal("ошибка записи обязана вернуться")
	}
	if got := lotQty(t, uc, "p1", d(2026, 9, 1)); got != 2 {
		t.Errorf("кэш изменён при ошибке БД: лот 01.09 qty = %d, want 2", got)
	}
	if len(notify.texts) != 0 {
		t.Errorf("notify.texts = %q, want пусто (складу нечего сообщать)", notify.texts)
	}
}

// TestFreezeNoticeRetry — Telegram не принял сообщение: оно остаётся в очереди
// и уходит на следующем тике ровно один раз (пары уже удалены, дублей нет).
func TestFreezeNoticeRetry(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	notify := &mockNotifier{err: errors.New("telegram down")}
	uc := NewStockUseCase(repo, &mockPub{}, nil, notify)
	uc.now = func() time.Time { return time.Date(2026, 9, 4, 20, 1, 0, 0, time.Local) }
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	uc.tryExpiryCleanup(context.Background(), 20*time.Hour)
	if got := lotQty(t, uc, "p1", d(2026, 9, 5)); got != -1 {
		t.Fatalf("лот 05.09 не убран из кэша (qty = %d)", got)
	}
	if len(uc.pendingFreeze) != 1 {
		t.Fatalf("pendingFreeze = %q, want 1 сообщение в очереди", uc.pendingFreeze)
	}

	// Сеть вернулась: сообщение уходит, очередь пустеет.
	notify.err = nil
	uc.tryExpiryCleanup(context.Background(), 20*time.Hour)
	if len(uc.pendingFreeze) != 0 {
		t.Errorf("pendingFreeze = %q, want пусто", uc.pendingFreeze)
	}
	if len(notify.texts) != 1 {
		t.Fatalf("notify.texts = %q, want одно сообщение", notify.texts)
	}

	// Повторный тик в тот же день новых сообщений не даёт.
	uc.tryExpiryCleanup(context.Background(), 20*time.Hour)
	if len(notify.texts) != 1 {
		t.Errorf("notify.texts = %q, want без повторов", notify.texts)
	}
}

// TestExpiryWatchOncePerDay — проход ровно один в сутки и не раньше времени:
// пары, принятые после прохода, добирает следующий день.
func TestExpiryWatchOncePerDay(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	notify := &mockNotifier{}
	uc := NewStockUseCase(repo, &mockPub{}, nil, notify)

	now := time.Date(2026, 9, 4, 19, 59, 0, 0, time.Local)
	uc.now = func() time.Time { return now }
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// До 20:00 — тишина.
	uc.tryExpiryCleanup(context.Background(), 20*time.Hour)
	if repo.replaceCalls != 0 || len(notify.texts) != 0 {
		t.Fatalf("до времени уборки ничего не происходит: replaceCalls=%d texts=%q", repo.replaceCalls, notify.texts)
	}

	// 20:01 — уборка пар ≤ 05.09 (05.09 — «завтра»).
	now = time.Date(2026, 9, 4, 20, 1, 0, 0, time.Local)
	uc.tryExpiryCleanup(context.Background(), 20*time.Hour)
	if repo.replaceCalls != 1 {
		t.Fatalf("replaceCalls = %d, want 1", repo.replaceCalls)
	}

	// Вечерняя приёмка того же срока: в тот же день проход не повторяется.
	if err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p1", BestBefore: d(2026, 9, 5), Qty: 3},
	}); err != nil {
		t.Fatalf("AcceptStock: %v", err)
	}
	now = time.Date(2026, 9, 4, 21, 30, 0, 0, time.Local)
	uc.tryExpiryCleanup(context.Background(), 20*time.Hour)
	if repo.replaceCalls != 1 {
		t.Errorf("replaceCalls = %d, want 1 (один проход в сутки)", repo.replaceCalls)
	}

	// Следующий день — проход снова, пара 05.09 (уже «сегодня») уходит.
	now = time.Date(2026, 9, 5, 20, 1, 0, 0, time.Local)
	uc.tryExpiryCleanup(context.Background(), 20*time.Hour)
	if repo.replaceCalls != 2 {
		t.Fatalf("replaceCalls = %d, want 2", repo.replaceCalls)
	}
	if got := lotQty(t, uc, "p1", d(2026, 9, 5)); got != -1 {
		t.Errorf("лот 05.09 остался в кэше (qty = %d)", got)
	}
}

// TestFreezeMessagePartsChunking — длинный список делится под лимит Telegram:
// иначе сообщение не уйдёт вовсе, а ретрай станет вечным.
func TestFreezeMessagePartsChunking(t *testing.T) {
	lots := make([]stock.ExpiringLot, 0, 400)
	for i := range 400 {
		lots = append(lots, stock.ExpiringLot{
			ProductID:  "p",
			Name:       "Стейк Рибай сухого вызревания 300 г порционный",
			Qty:        int64(i + 1),
			BestBefore: d(2026, 9, 5),
		})
	}

	parts := freezeMessageParts(lots)
	if len(parts) < 2 {
		t.Fatalf("parts = %d, want ≥ 2", len(parts))
	}
	for i, p := range parts {
		if len(p) > freezeMaxLen {
			t.Errorf("часть %d: %d символов, лимит %d", i, len(p), freezeMaxLen)
		}
		if !strings.HasPrefix(p, "Позиции убраны из сроков") {
			t.Errorf("часть %d без заголовка: %q", i, p[:40])
		}
	}
	// Нумерация сквозная: продолжение начинается со следующего номера, а не с 1.
	last := strings.Split(parts[0], "\n")
	next := strings.Split(parts[1], "\n")
	if len(last) < 2 || len(next) < 2 {
		t.Fatalf("в частях нет строк списка: %q", parts)
	}
	var prevNo, nextNo int
	if _, err := fmt.Sscanf(last[len(last)-1], "%d.", &prevNo); err != nil {
		t.Fatalf("номер последней строки части 1: %v (%q)", err, last[len(last)-1])
	}
	if _, err := fmt.Sscanf(next[1], "%d.", &nextNo); err != nil {
		t.Fatalf("номер первой строки части 2: %v (%q)", err, next[1])
	}
	if nextNo != prevNo+1 {
		t.Errorf("нумерация разрывна: часть 1 кончилась на %d, часть 2 началась с %d", prevNo, nextNo)
	}
}

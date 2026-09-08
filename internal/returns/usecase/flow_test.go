package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/returns"
)

func strptr(s string) *string { return &s }

func auditRow(source string) client.AuditRow {
	return client.AuditRow{
		ID:         auditID,
		Moment:     "2026-09-08 23:11:52.918",
		EntityType: "customerorder",
		EventType:  "update",
		Source:     strptr(source),
		UID:        "sklad@steakhome",
	}
}

func cancelledDiffJSON() string {
	return `{"state":{"oldValue":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/785d7841-1ac2-11f0-0a80-071f000f6177"},"name":"РефГо"},` +
		`"newValue":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/customerorder/metadata/states/8737d8a5-c0b9-11e3-ac8e-002590a28eca"},"name":"Отменен"}}}`
}

func pos(productID, name string, qty, reserve float64) client.MSPosition {
	return client.MSPosition{
		Assortment: client.MSAssortment{Meta: client.MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/product/" + productID}, Name: name},
		Quantity:   qty,
		Reserve:    reserve,
	}
}

// ── Поллер ─────────────────────────────────────────────────────────────────

func TestTick_FirstRunSetsCursorAndScansNothing(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if repo.cursor == nil {
		t.Fatal("курсор не установлен при первом запуске")
	}
	if len(repo.events) != 0 {
		t.Fatal("первый запуск не должен сканировать прошлое")
	}
	if audit.detailHits != 0 {
		t.Fatalf("раскрытия не должны ходить на первом тике, было %d", audit.detailHits)
	}
}

func TestTick_SkipsOurApiSource(t *testing.T) {
	repo := newStubRepo()
	repo.cursor = &[]time.Time{time.Now().Add(-time.Hour).UTC()}[0]
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify
	audit.pageRows = []client.AuditRow{auditRow("remap-1.2")} // наш PUT: полная замена positions
	audit.details[auditID] = []client.AuditEventRow{detailRow(removedDiffJSON(0.657, "кг"), "19191")}

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if audit.detailHits != 0 {
		t.Fatalf("source=remap-1.2 не должен раскрываться, hits=%d", audit.detailHits)
	}
	if len(notify.sends) != 0 {
		t.Fatal("наши API-изменения не должны уходить в чат склада")
	}
}

func TestTick_RemovedReservedSendsNotification(t *testing.T) {
	repo := newStubRepo()
	repo.cursor = &[]time.Time{time.Now().Add(-time.Hour).UTC()}[0]
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify

	audit.pageRows = []client.AuditRow{auditRow("app")}
	// Удалена отложенная позиция (quantity == reserved) — склад должен вернуть кусок.
	audit.details[auditID] = []client.AuditEventRow{detailRow(removedDiffJSON(0.657, "кг"), "19191")}

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	ev := repo.events[auditID]
	if ev == nil {
		t.Fatal("событие не создано")
	}
	if ev.Kind != returns.KindRemoved || ev.OrderName != "19191" {
		t.Errorf("event = %+v, want positions_removed 19191", ev)
	}
	if ev.Status != returns.StatusSent {
		t.Errorf("status = %s, want sent", ev.Status)
	}
	if len(notify.sends) != 1 {
		t.Fatalf("want 1 отправку, got %d", len(notify.sends))
	}
	if !strings.Contains(notify.sends[0], "Из заказа 19191 удалили:") ||
		!strings.Contains(notify.sends[0], "Чак ролл 0.657 кг") {
		t.Errorf("текст = %q", notify.sends[0])
	}
	if !strings.HasSuffix(notify.urls[0], "/goods/return?e="+auditID) {
		t.Errorf("url кнопки = %q", notify.urls[0])
	}
}

func TestTick_CancelledWithoutReserveCreatesNothing(t *testing.T) {
	repo := newStubRepo()
	repo.cursor = &[]time.Time{time.Now().Add(-time.Hour).UTC()}[0]
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify

	audit.pageRows = []client.AuditRow{auditRow("app")}
	audit.details[auditID] = []client.AuditEventRow{detailRow(cancelledDiffJSON(), "19379")}
	// Заказ 19379: позиции без резерва (ничего не отложено — возвращать нечего).
	audit.positions[orderID] = []client.MSPosition{pos(prodD, "Соус", 1, 0)}

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(repo.events) != 0 || len(notify.sends) != 0 {
		t.Fatalf("пустая отмена не должна создавать событие: events=%d sends=%d", len(repo.events), len(notify.sends))
	}
}

func TestTick_CancelledSendsNotification(t *testing.T) {
	repo := newStubRepo()
	repo.cursor = &[]time.Time{time.Now().Add(-time.Hour).UTC()}[0]
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify

	audit.pageRows = []client.AuditRow{auditRow("app")}
	audit.details[auditID] = []client.AuditEventRow{detailRow(cancelledDiffJSON(), "19379")}
	audit.positions[orderID] = []client.MSPosition{pos(prodA, "Чак ролл", 0.657, 0.657)} // отложен

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	ev := repo.events[auditID]
	if ev == nil || ev.Kind != returns.KindCancelled {
		t.Fatalf("event = %+v, want order_cancelled", ev)
	}
	if len(notify.sends) != 1 || notify.sends[0] != "Заказ 19379 был переведён в статус «Отменён»" {
		t.Errorf("текст = %q", notify.sends)
	}
}

func TestTick_AlreadyTrackedSkipped(t *testing.T) {
	repo := newStubRepo()
	repo.cursor = &[]time.Time{time.Now().Add(-time.Hour).UTC()}[0]
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify

	// Событие уже отслеживается (предыдущий тик успел обработать).
	repo.events[auditID] = &returns.ReturnEvent{ID: auditID, Kind: returns.KindRemoved, OrderID: orderID, OrderName: "19191", Status: returns.StatusSent}

	audit.pageRows = []client.AuditRow{auditRow("app")}
	audit.details[auditID] = []client.AuditEventRow{detailRow(removedDiffJSON(0.657, "кг"), "19191")}

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if audit.detailHits != 0 {
		t.Fatalf("отслеживаемое событие не должно раскрываться, hits=%d", audit.detailHits)
	}
	if len(notify.sends) != 0 {
		t.Fatal("повторная отправка не нужна")
	}
}

func TestRetryNew_ResendsAfterFailedSend(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify

	// Событие зависло в new (упали между InsertEvent и MarkSent).
	repo.events[auditID] = &returns.ReturnEvent{ID: auditID, Kind: returns.KindRemoved, OrderID: orderID, OrderName: "19191", Status: returns.StatusNew}
	audit.details[auditID] = []client.AuditEventRow{detailRow(removedDiffJSON(0.657, "кг"), "19191")}

	if err := uc.retryNew(context.Background()); err != nil {
		t.Fatalf("retryNew: %v", err)
	}
	if len(notify.sends) != 1 {
		t.Fatalf("want 1 повторную отправку, got %d", len(notify.sends))
	}
	if repo.events[auditID].Status != returns.StatusSent {
		t.Errorf("status = %s, want sent после повторной отправки", repo.events[auditID].Status)
	}
}

// ── Страница ───────────────────────────────────────────────────────────────

func TestAcceptReturn_HappyPath(t *testing.T) {
	repo := newStubRepo()
	chat, msg := int64(-100999), int64(42)
	repo.events[auditID] = &returns.ReturnEvent{
		ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, OrderName: "19379",
		Status: returns.StatusSent, ChatID: &chat, MessageID: &msg,
	}
	env := newTestEnv(repo)
	uc, audit, stockS, notify := env.uc, env.audit, env.stock, env.notify
	audit.positions[orderID] = []client.MSPosition{pos(prodA, "Чак ролл", 0.657, 0.657)}

	n, err := uc.AcceptReturn(context.Background(), auditID, []string{
		etiketa(codeA, 400),
		etiketa(codeA, 257),
	})
	if err != nil {
		t.Fatalf("AcceptReturn: %v", err)
	}
	if n != 2 {
		t.Errorf("принято единиц = %d, want 2", n)
	}
	if len(stockS.accepted) != 1 || stockS.accepted[0].ProductID != prodA || stockS.accepted[0].Qty != 2 {
		t.Fatalf("lots = %+v, want 1 лот Чак ролл qty 2 (накопление 400+257)", stockS.accepted)
	}
	if !stockS.accepted[0].BestBefore.Equal(time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("срок = %v, want 15.09.2026 из этикетки", stockS.accepted[0].BestBefore)
	}
	if repo.events[auditID].Status != returns.StatusDone {
		t.Errorf("status = %s, want done", repo.events[auditID].Status)
	}
	if len(notify.deletes) != 1 || notify.deletes[0] != [2]int64{-100999, 42} {
		t.Errorf("сообщение не удалено: %+v", notify.deletes)
	}
}

func TestAcceptReturn_AlreadyDoneRejected(t *testing.T) {
	repo := newStubRepo()
	repo.events[auditID] = &returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, Status: returns.StatusDone}
	env := newTestEnv(repo)
	uc, stockS, notify := env.uc, env.stock, env.notify

	_, err := uc.AcceptReturn(context.Background(), auditID, []string{etiketa(codeA, 657)})
	if !errors.Is(err, returns.ErrAlreadyDone) {
		t.Fatalf("want ErrAlreadyDone, got %v", err)
	}
	if len(stockS.accepted) != 0 || len(notify.deletes) != 0 {
		t.Fatal("повторный приём не должен трогать stock/сообщение")
	}
}

func TestAcceptReturn_ValidationRejected(t *testing.T) {
	repo := newStubRepo()
	chat, msg := int64(-100999), int64(42)
	repo.events[auditID] = &returns.ReturnEvent{
		ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, Status: returns.StatusSent, ChatID: &chat, MessageID: &msg,
	}
	env := newTestEnv(repo)
	uc, audit, stockS, notify := env.uc, env.audit, env.stock, env.notify
	audit.positions[orderID] = []client.MSPosition{pos(prodA, "Чак ролл", 0.657, 0.657)}

	_, err := uc.AcceptReturn(context.Background(), auditID, []string{etiketa(codeA, 654)})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(stockS.accepted) != 0 {
		t.Fatal("несошедшиеся сканы не должны попасть в stock")
	}
	if repo.events[auditID].Status != returns.StatusSent {
		t.Errorf("status = %s, want sent (событие живо до успешного приёма)", repo.events[auditID].Status)
	}
	if len(notify.deletes) != 0 {
		t.Fatal("сообщение не удаляется при ошибке валидации")
	}
}

func TestCloseManual(t *testing.T) {
	repo := newStubRepo()
	chat, msg := int64(-100999), int64(42)
	repo.events[auditID] = &returns.ReturnEvent{
		ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, OrderName: "19379",
		Status: returns.StatusSent, ChatID: &chat, MessageID: &msg,
	}
	env := newTestEnv(repo)
	uc, audit, stockS, notify := env.uc, env.audit, env.stock, env.notify
	audit.positions[orderID] = []client.MSPosition{pos(prodA, "Чак ролл", 0.657, 0.657)}

	if err := uc.CloseManual(context.Background(), auditID); err != nil {
		t.Fatalf("CloseManual: %v", err)
	}
	ev := repo.events[auditID]
	if ev.Status != returns.StatusDone || !ev.Manual {
		t.Errorf("event = %+v, want done + manual", ev)
	}
	if len(stockS.accepted) != 0 {
		t.Fatal("ручное закрытие не пишет в stock")
	}
	if len(notify.deletes) != 1 {
		t.Fatal("сообщение должно быть удалено при ручном закрытии")
	}
}

func TestEventPage_DoneDoesNotFetch(t *testing.T) {
	repo := newStubRepo()
	repo.events[auditID] = &returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, Status: returns.StatusDone}
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit

	state, err := uc.EventPage(context.Background(), auditID)
	if err != nil {
		t.Fatalf("EventPage: %v", err)
	}
	if !state.Done || state.Expected != nil {
		t.Errorf("state = %+v, want done без ожиданий", state)
	}
	if audit.posHits != 0 {
		t.Fatal("обработанное событие не должно ходить в МС")
	}
}

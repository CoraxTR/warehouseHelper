package usecase

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/collab"
)

// newUC собирает сценарии на управляемых часах: TTL заброшенных комнат
// проверяем без ожидания.
func newUC(t *testing.T) (uc *UseCase, advance func(time.Duration)) {
	t.Helper()

	now := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
	store := collab.NewStore(func() time.Time { return now })
	uc = NewUseCase(store, time.Hour)

	return uc, func(d time.Duration) { now = now.Add(d) }
}

func raw(v string) json.RawMessage {
	return json.RawMessage(`{"raw":"` + v + `"}`)
}

// TestOpenValidates — комната открывается только для известного вида работы и
// только с указанной работой; имя работы обрезается.
func TestOpenValidates(t *testing.T) {
	uc, _ := newUC(t)

	if _, err := uc.Open(collab.Kind("inventory"), "doc-1", "Инвентаризация"); !errors.Is(err, collab.ErrKind) {
		t.Errorf("неизвестный вид: %v, ожидалась ErrKind", err)
	}

	if _, err := uc.Open(collab.KindReceive, "   ", "Поставщик"); !errors.Is(err, ErrNeedRef) {
		t.Errorf("пустая работа: %v, ожидалась ErrNeedRef", err)
	}

	session, err := uc.Open(collab.KindReceive, "sup-1", "  Поставщик Ромашка  ")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if session.Title != "Поставщик Ромашка" {
		t.Errorf("имя работы = %q, обрезка не сработала", session.Title)
	}

	if session.Kind != collab.KindReceive || session.Ref != "sup-1" || session.ID == "" {
		t.Errorf("комната: %+v", session)
	}
}

// TestTTLDefault — нулевой TTL заменяется дефолтом, заданный — сохраняется.
func TestTTLDefault(t *testing.T) {
	if got := NewUseCase(collab.NewStore(nil), 0).TTL(); got != DefaultTTL {
		t.Errorf("TTL по умолчанию = %v, ожидался %v", got, DefaultTTL)
	}

	if got := NewUseCase(collab.NewStore(nil), 30*time.Minute).TTL(); got != 30*time.Minute {
		t.Errorf("TTL = %v, ожидался 30m", got)
	}
}

// TestListPurgesStale — список открытых работ не показывает заброшенные комнаты
// и попутно убирает их из памяти.
func TestListPurgesStale(t *testing.T) {
	uc, advance := newUC(t)

	live, err := uc.Open(collab.KindReceive, "sup-1", "Ромашка")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	old, err := uc.Open(collab.KindReceive, "sup-2", "Лютик")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := uc.Join(old.ID, ""); err != nil {
		t.Fatalf("Join: %v", err)
	}

	advance(3 * time.Hour)

	if _, err := uc.Touch(live.ID, ""); !errors.Is(err, collab.ErrGuestGone) {
		t.Errorf("Touch без гостя: %v, ожидалась ErrGuestGone", err)
	}

	if _, err := uc.Join(live.ID, ""); err != nil {
		t.Fatalf("Join: %v", err)
	}

	// Комната live обновлена только что, комната old молчит с создания.
	advance(30 * time.Minute)

	list := uc.List(collab.KindReceive)
	if len(list) != 1 || list[0].ID != live.ID {
		t.Errorf("список открытых работ = %+v, ожидалась только живая комната", list)
	}
}

// TestFlow — сквозной путь гостя через сценарии: подключение, ожидание, отправка
// чанка, склейка строк для сохранения.
func TestFlow(t *testing.T) {
	uc, _ := newUC(t)

	room, err := uc.Open(collab.KindReceive, "sup-1", "Ромашка")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	room, err = uc.Join(room.ID, "")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}

	guestID := room.Guests[0].ID
	if guestID == "" || room.Guests[0].Name != "Гость 1" {
		t.Fatalf("гость: %+v", room.Guests[0])
	}

	if waiting, err := uc.Waiting(room.ID); err != nil || len(waiting) != 1 {
		t.Errorf("Waiting = %v (%v), ожидался [Гость 1]", waiting, err)
	}

	if _, err := uc.Submit(room.ID, guestID, []json.RawMessage{raw("111")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if waiting, err := uc.Waiting(room.ID); err != nil || len(waiting) != 0 {
		t.Errorf("Waiting = %v (%v), ожидался пустой список", waiting, err)
	}

	if _, err := uc.Submit(room.ID, guestID, nil); !errors.Is(err, collab.ErrEmpty) {
		t.Errorf("пустой чанк: %v, ожидалась ErrEmpty", err)
	}

	if _, err := uc.SetScanning(room.ID, guestID); err != nil {
		t.Fatalf("SetScanning: %v", err)
	}

}

// TestFlowNewChunkDropAndClose — отключение гостя выбрасывает его строки, закрытие
// комнаты закрывает вход.
func TestFlowNewChunkDropAndClose(t *testing.T) {
	uc, _ := newUC(t)

	room, err := uc.Open(collab.KindReceive, "sup-1", "Ромашка")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	room, err = uc.Join(room.ID, "")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}

	guestID := room.Guests[0].ID

	if _, err := uc.Submit(room.ID, guestID, []json.RawMessage{raw("111")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	scans, err := uc.GuestScans(room.ID)
	if err != nil {
		t.Fatalf("GuestScans: %v", err)
	}

	if len(scans) != 1 {
		t.Errorf("строк для склейки %d, ожидалась 1", len(scans))
	}

	if _, err := uc.Drop(room.ID, guestID); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	scans, err = uc.GuestScans(room.ID)
	if err != nil {
		t.Fatalf("GuestScans: %v", err)
	}

	if len(scans) != 0 {
		t.Errorf("строки отключённого гостя остались: %d", len(scans))
	}

	closed, err := uc.Close(room.ID)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !closed.Closed() {
		t.Error("комната не помечена закрытой")
	}

	if _, err := uc.Join(room.ID, ""); !errors.Is(err, collab.ErrClosed) {
		t.Errorf("подключение в закрытую комнату: %v, ожидалась ErrClosed", err)
	}

	if _, err := uc.State("нет-такой"); !errors.Is(err, collab.ErrNotFound) {
		t.Errorf("состояние чужой комнаты: %v, ожидалась ErrNotFound", err)
	}
}

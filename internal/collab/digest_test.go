package collab

import (
	"errors"
	"testing"
)

// TestGuestSnapshotDigest — предпросмотр фиксирует отпечаток набора строк гостей,
// проведение его сверяет: гость, вернувшийся после предпросмотра и доливший сканы,
// не даёт собрать документ по строкам, которых хозяин не видел. При отказе комната
// обязана остаться свободной.
func TestGuestSnapshotDigest(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")
	guest := join(t, s, room.ID, "").Guests[0].ID

	submit(t, s, room.ID, guest, scan("aaa"), scan("bbb"))

	rows, digest, err := s.GuestSnapshot(room.ID)
	if err != nil || len(rows) != 2 || digest == "" {
		t.Fatalf("GuestSnapshot() = %d строк, %q, err=%v", len(rows), digest, err)
	}

	if _, again, err := s.GuestSnapshot(room.ID); err != nil || again != digest {
		t.Fatalf("отпечаток без изменений = %q (err=%v), want %q", again, err, digest)
	}

	// Гость вернулся и долил пачку — набор другой.
	submit(t, s, room.ID, guest, scan("ccc"))

	if _, err := s.ClaimSnapshot(room.ID, room.Ref, digest); !errors.Is(err, ErrGuestsChanged) {
		t.Fatalf("ClaimSnapshot со старым отпечатком: err=%v, want ErrGuestsChanged", err)
	}

	// После отказа комната свободна: свежий предпросмотр и захват проходят.
	_, fresh, err := s.GuestSnapshot(room.ID)
	if err != nil {
		t.Fatalf("GuestSnapshot после отказа: %v", err)
	}

	rows, err = s.ClaimSnapshot(room.ID, room.Ref, fresh)
	if err != nil || len(rows) != 3 {
		t.Fatalf("ClaimSnapshot со свежим отпечатком = %d строк, err=%v", len(rows), err)
	}

	// Занятая комната отвечает ErrBusy, а не «строки изменились».
	if _, err := s.ClaimSnapshot(room.ID, room.Ref, fresh); !errors.Is(err, ErrBusy) {
		t.Fatalf("повторный захват: err=%v, want ErrBusy", err)
	}
}

// TestClaimSnapshotWithoutDigest — старая страница (без guest_token) работает как
// раньше: захват без сверки набора.
func TestClaimSnapshotWithoutDigest(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-2")
	guest := join(t, s, room.ID, "").Guests[0].ID
	submit(t, s, room.ID, guest, scan("aaa"))

	rows, err := s.ClaimSnapshot(room.ID, room.Ref, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ClaimSnapshot(без отпечатка) = %d строк, err=%v", len(rows), err)
	}
}

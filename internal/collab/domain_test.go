package collab

import "testing"

// TestKindValid — точка расширения видов работы: приёмка и инвентаризация
// открываются, чужой вид — нет (владелец, 06.10.2026: отдельного «совместного»
// режима не плодим, комната заводится и для инвентаризации).
func TestKindValid(t *testing.T) {
	cases := []struct {
		kind Kind
		want bool
	}{
		{KindReceive, true},
		{KindInventory, true},
		{Kind("inventory"), true}, // совпадает с KindInventory
		{Kind(""), false},
		{Kind("bogus"), false},
	}

	for _, c := range cases {
		if got := c.kind.Valid(); got != c.want {
			t.Errorf("Kind(%q).Valid() = %v, ожидалось %v", c.kind, got, c.want)
		}
	}
}

// TestHostKeyLooksLike — роль хозяина по cookie, когда комнаты на сервере уже
// нет (рестарт/TTL/«Отменить»): ключ хозяина живёт дольше комнаты и обязан
// отличать её машину от гостя и от чужого/пустого cookie.
func TestHostKeyLooksLike(t *testing.T) {
	const roomID = "room-1"

	cases := []struct {
		name        string
		cookieName  string
		cookieValue string
		room        string
		want        bool
	}{
		{"ключ этой комнаты", HostCookieName(roomID), "secret", roomID, true},
		{"пустое значение — не ключ", HostCookieName(roomID), "", roomID, false},
		{"ключ чужой комнаты", HostCookieName("room-2"), "secret", roomID, false},
		{"не наш cookie", "wh_collab_guest_" + roomID, "secret", roomID, false},
	}

	for _, c := range cases {
		got := HostKeyLooksLike(c.cookieName, c.cookieValue, c.room)
		if got != c.want {
			t.Errorf("%s: HostKeyLooksLike(%q, %q, %q) = %v, ожидалось %v",
				c.name, c.cookieName, c.cookieValue, c.room, got, c.want)
		}
	}
}

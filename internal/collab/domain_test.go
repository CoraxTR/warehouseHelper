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

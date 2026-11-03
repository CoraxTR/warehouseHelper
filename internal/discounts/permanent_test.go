package discounts

import (
	"testing"
	"time"
)

// Постоянная скидка поставщика — пол канала сайта (решение владельца
// 03.11.2026): лесенка и избыток могут её только поднять, при равенстве
// побеждает постоянная (поднимать нечего, а место в окне она не занимает).
func TestResolvePermanent(t *testing.T) {
	tests := []struct {
		name      string
		manual    *int16
		permanent *int16
		expiry    *int16
		surplus   *int16
		want      *int16
		wantSrc   Source
	}{
		{"постоянная без кандидатов", nil, new(int16(15)), nil, nil, new(int16(15)), SourcePermanent},
		{"лесенка глубже постоянной", nil, new(int16(15)), new(int16(30)), nil, new(int16(30)), SourceExpiry},
		{"постоянная глубже лесенки", nil, new(int16(25)), new(int16(10)), nil, new(int16(25)), SourcePermanent},
		{"равенство — постоянная", nil, new(int16(20)), new(int16(20)), nil, new(int16(20)), SourcePermanent},
		{"избыток глубже постоянной", nil, new(int16(10)), nil, new(int16(10)), new(int16(10)), SourcePermanent},
		{"ручная главнее постоянной", new(int16(5)), new(int16(30)), nil, nil, new(int16(5)), SourceManual},
		{"ручная 0 — бан", new(int16(0)), new(int16(30)), new(int16(20)), nil, new(int16(0)), SourceManual},
		{"постоянной нет — как раньше", nil, nil, new(int16(20)), new(int16(10)), new(int16(20)), SourceExpiry},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, src := Resolve(tc.manual, tc.permanent, tc.expiry, tc.surplus)
			if src != tc.wantSrc {
				t.Fatalf("Resolve source = %v, want %v", src, tc.wantSrc)
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("Resolve percent = %v, want %d", got, *tc.want)
			}
		})
	}
}

// Дата окончания действует ВКЛЮЧИТЕЛЬНО и считается по московским суткам:
// сервер живёт в UTC, а «до 05.11» человек понимает как московский день.
func TestPermanentActive(t *testing.T) {
	day := time.Date(2026, time.November, 5, 1, 0, 0, 0, time.UTC) // 04:00 МСК, 5 ноября
	until := func(d time.Time) *time.Time { return &d }

	tests := []struct {
		name   string
		in     Input
		wantOK bool
	}{
		{"дата сегодня — действует", Input{Permanent: new(int16(15)), PermanentUntil: until(time.Date(2026, time.November, 5, 0, 0, 0, 0, time.UTC))}, true},
		{"дата вчера — вышла", Input{Permanent: new(int16(15)), PermanentUntil: until(time.Date(2026, time.November, 4, 0, 0, 0, 0, time.UTC))}, false},
		{"дата завтра — действует", Input{Permanent: new(int16(15)), PermanentUntil: until(time.Date(2026, time.November, 6, 0, 0, 0, 0, time.UTC))}, true},
		{"без даты — не действует", Input{Permanent: new(int16(15))}, false},
		{"без процента — не действует", Input{PermanentUntil: until(time.Date(2026, time.November, 6, 0, 0, 0, 0, time.UTC))}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.PermanentActive(day); (got != nil) != tc.wantOK {
				t.Fatalf("PermanentActive = %v, want действует=%v", got, tc.wantOK)
			}
		})
	}
}

// Постоянные скидки окно не занимают: ёмкость съедают только слотовые строки,
// постоянные едут сверх окна (решение владельца 03.11.2026).
func TestBuildDigestPermanentBeyondCapacity(t *testing.T) {
	slot := func(id string, percent int16) Row {
		return Row{ProductID: id, Percent: percent, Source: SourceExpiry, DaysLeft: 3}
	}
	perm := func(id string, percent int16) Row {
		return Row{ProductID: id, Percent: percent, Source: SourcePermanent, Permanent: true, DaysLeft: 10}
	}
	rows := []Row{slot("slot-1", 40), slot("slot-2", 30), perm("perm-1", 15), perm("perm-2", 10)}

	d := BuildDigest(rows, 1)
	if len(d.Discounts) != 3 {
		t.Fatalf("в окне %d строк, want 3 (1 слотовая + 2 постоянные): %+v", len(d.Discounts), d.Discounts)
	}
	if len(d.Surplus) != 1 || d.Surplus[0].ProductID != "slot-2" {
		t.Fatalf("за окном %+v, want вторая слотовая", d.Surplus)
	}
	got := map[string]bool{}
	for _, r := range d.Discounts {
		got[r.ProductID] = true
	}
	for _, id := range []string{"slot-1", "perm-1", "perm-2"} {
		if !got[id] {
			t.Errorf("в окне нет %s: %+v", id, d.Discounts)
		}
	}
}

package usecase

import (
	"testing"
	"time"

	"warehouseHelper/internal/msclient/client"
)

const testCanonical = "23c7d1ba-10e9-11e5-90a2-8ecb0002b4b4"

// position собирает снимок позиции заказа для диффа.
func position(href, name string, qty float64) *client.AuditPosition {
	p := &client.AuditPosition{Quantity: qty}
	p.Assortment.Name = name
	p.Assortment.Meta.HREF = href

	return p
}

func orderEvent(orderName, orderID string, src *string, changes ...client.AuditPositionChange) client.AuditEventRow {
	ev := client.AuditEventRow{Name: orderName, Source: src, UID: "sabina@steakhome"}
	ev.Entity.Meta.HREF = "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/" + orderID
	ev.Diff.Positions = changes

	return ev
}

// Удаление позиции — только OldValue; изменение и чужой товар отбрасываются.
func TestDeletions(t *testing.T) {
	t.Parallel()

	del := client.AuditPositionChange{OldValue: position("https://api.moysklad.ru/api/remap/1.2/entity/product/"+testCanonical, "Стейк Портерхаус Праймбиф. Охл.", 1.25)}
	edit := client.AuditPositionChange{
		OldValue: position("https://api.moysklad.ru/api/remap/1.2/entity/product/"+testCanonical, "Стейк Портерхаус Праймбиф. Охл.", 1),
		NewValue: position("https://api.moysklad.ru/api/remap/1.2/entity/product/"+testCanonical, "Стейк Портерхаус Праймбиф. Охл.", 2),
	}
	other := client.AuditPositionChange{OldValue: position("https://api.moysklad.ru/api/remap/1.2/entity/product/ffffffff-0000-0000-0000-000000000000", "Другой товар", 1)}

	tests := []struct {
		name   string
		events []client.AuditEventRow
		want   int
	}{
		{"удаление найдено", []client.AuditEventRow{orderEvent("00001", "o-1", new("app"), del)}, 1},
		{"изменение количества — не удаление", []client.AuditEventRow{orderEvent("00002", "o-2", new("app"), edit)}, 0},
		{"чужой товар", []client.AuditEventRow{orderEvent("00003", "o-3", new("app"), other)}, 0},
		{"наш движок — не ручное", []client.AuditEventRow{orderEvent("00004", "o-4", new("remap-1.2"), del)}, 0},
		{"источник пуст — считаем ручным", []client.AuditEventRow{orderEvent("00005", "o-5", nil, del)}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := deletions(tt.events, testCanonical, "Стейк Портерхаус Праймбиф. Охл.")
			if len(got) != tt.want {
				t.Fatalf("найдено %d, ждали %d", len(got), tt.want)
			}
		})
	}
}

// Совпадение по имени — страховка на случай, если id в диффе не канонический.
func TestMatchesProduct(t *testing.T) {
	t.Parallel()

	byName := *position("https://api.moysklad.ru/api/remap/1.2/entity/product/legacy-id", "Рибай Cresud. Охл.", 1)
	if !matchesProduct(byName, "cc6bc91c-7bb5-11e9-912f-f3d40005dd67", "Рибай Cresud. Охл.") {
		t.Error("ждали совпадение по имени")
	}

	foreign := *position("https://api.moysklad.ru/api/remap/1.2/entity/product/legacy-id", "Вырезка Cresud. Охл.", 1)
	if matchesProduct(foreign, "cc6bc91c-7bb5-11e9-912f-f3d40005dd67", "Рибай Cresud. Охл.") {
		t.Error("чужой товар не должен совпадать")
	}
}

func TestIsManualSource(t *testing.T) {
	t.Parallel()

	cases := map[*string]bool{
		nil:              true,
		new("app"):       true,
		new("jsonapi"):   false,
		new("remap-1.2"): false,
	}

	for src, want := range cases {
		if got := isManualSource(src); got != want {
			label := "nil"
			if src != nil {
				label = *src
			}
			t.Errorf("источник %s: получено %v, ждали %v", label, got, want)
		}
	}
}

func TestPercentAndEta(t *testing.T) {
	t.Parallel()

	if got := percent(200, 50, StateRunning); got != 25 {
		t.Errorf("percent: получено %d, ждали 25", got)
	}
	if got := percent(0, 0, StateDone); got != 100 {
		t.Errorf("percent пустой задачи: получено %d, ждали 100", got)
	}
	if got := percent(10, 20, StateRunning); got != 100 {
		t.Errorf("percent больше 100: получено %d", got)
	}
	if got := etaSec(1000, 100, 10*time.Minute, StateRunning); got != 5400 {
		t.Errorf("eta: получено %d, ждали 5400", got)
	}
	if got := etaSec(1000, 3, 10*time.Minute, StateRunning); got != 0 {
		t.Errorf("eta до ETAFromDone: получено %d, ждали 0", got)
	}
	if got := etaSec(1000, 1000, time.Minute, StateDone); got != 0 {
		t.Errorf("eta завершённой задачи: получено %d, ждали 0", got)
	}
}

func TestNormalizeWindow(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.November, 26, 0, 0, 0, 0, time.UTC)

	gotFrom, gotTo, err := normalizeWindow(from, to)
	if err != nil {
		t.Fatalf("окно отвергнуто: %v", err)
	}
	if !gotTo.After(gotFrom) {
		t.Errorf("конец окна %v не позже начала %v", gotTo, gotFrom)
	}

	if _, _, err := normalizeWindow(to, from); err == nil {
		t.Error("перевёрнутое окно должно отвергаться")
	}

	wide := time.Date(2027, time.November, 26, 0, 0, 0, 0, time.UTC)
	if _, _, err := normalizeWindow(from, wide); err == nil {
		t.Error("окно больше 92 дней должно отвергаться")
	}
}

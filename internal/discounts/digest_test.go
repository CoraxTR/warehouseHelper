package discounts

import (
	"strings"
	"testing"
	"time"
)

func bb(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

// Порядок: ручные (срок ↑) → по сроку (срок ↑) → избыточные (коэф ↓),
// строки без скидки — в конце; равные ключи держат исходный порядок.
func TestSort(t *testing.T) {
	rows := []Row{
		{ProductID: "p5", Name: "Кефир", BestBefore: bb(2026, 9, 25), Percent: 40, Source: SourceExpiry},
		{ProductID: "p1", Name: "Йогурт", BestBefore: bb(2026, 10, 1), Percent: 10, Source: SourceSurplus, Coeff: 1.2},
		{ProductID: "p9", Name: "Вода", BestBefore: bb(2027, 1, 1), Source: SourceNone},
		{ProductID: "p3", Name: "Колбаса", BestBefore: bb(2026, 10, 12), Percent: 10, Source: SourceSurplus, Coeff: 3},
		{ProductID: "p2", Name: "Молоко", BestBefore: bb(2026, 9, 18), Percent: 20, Source: SourceManual},
		{ProductID: "p4", Name: "Сыр", BestBefore: bb(2026, 10, 5), Percent: 10, Source: SourceExpiry},
		{ProductID: "p1", Name: "Хлеб", BestBefore: bb(2026, 9, 22), Percent: 30, Source: SourceManual},
		{ProductID: "p6", Name: "Творог", BestBefore: bb(2026, 9, 22), Percent: 15, Source: SourceManual},
	}
	want := []string{"Молоко", "Хлеб", "Творог", "Кефир", "Сыр", "Колбаса", "Йогурт", "Вода"}

	Sort(rows)
	for i, name := range want {
		if rows[i].Name != name {
			t.Fatalf("позиция %d: %q, want %q (порядок: %v)", i, rows[i].Name, name, names(rows))
		}
	}
}

func names(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// Ёмкость окна делит отчёт: первые cap позиций по приоритету — активные, остальное
// «доступно для допродажи». Избыточная пара сюда же — она больше не отдельная
// секция, а участник общего приоритета (решение владельца 23.09.2026).
func TestBuildDigest(t *testing.T) {
	rows := []Row{
		{ProductID: "p3", Name: "Колбаса", BestBefore: bb(2026, 10, 12), Percent: 10, Source: SourceSurplus, Coeff: 1.6},
		{ProductID: "p2", Name: "Сыр", BestBefore: bb(2026, 10, 5), Percent: 10, Source: SourceExpiry},
		{ProductID: "p9", Name: "Вода", BestBefore: bb(2027, 1, 1), Source: SourceNone},
		{ProductID: "p1", Name: "Хлеб", BestBefore: bb(2026, 9, 22), Percent: 30, Source: SourceManual},
	}
	d := BuildDigest(rows, 2)

	if len(d.Discounts) != 2 {
		t.Fatalf("секция активных: %d строк, want 2 (%v)", len(d.Discounts), names(d.Discounts))
	}
	if d.Discounts[0].Name != "Хлеб" || d.Discounts[1].Name != "Сыр" {
		t.Errorf("секция активных = %v, want [Хлеб Сыр]", names(d.Discounts))
	}
	if len(d.Surplus) != 1 || d.Surplus[0].Name != "Колбаса" {
		t.Errorf("доступно для допродажи = %v, want [Колбаса]", names(d.Surplus))
	}
	if d.Cap != 2 {
		t.Errorf("ёмкость отчёта %d, want 2", d.Cap)
	}
	// Строка без источника в отчёт не попадает, дату проставляет вызывающий.
	if len(d.Discounts)+len(d.Surplus) != 3 {
		t.Errorf("всего строк в отчёте %d, want 3", len(d.Discounts)+len(d.Surplus))
	}
	if !d.Date.IsZero() {
		t.Errorf("BuildDigest выставил дату %v — дату задаёт вызывающий", d.Date)
	}
	// Входной срез не отсортирован на месте.
	if rows[0].Name != "Колбаса" {
		t.Errorf("входной срез изменён: %v", names(rows))
	}
}

// Ёмкость не задана (0) — активны все строки, «доступно для допродажи» пусто.
func TestBuildDigestNoCap(t *testing.T) {
	rows := []Row{
		{ProductID: "p2", Name: "Сыр", BestBefore: bb(2026, 10, 5), Percent: 10, Source: SourceExpiry},
		{ProductID: "p1", Name: "Хлеб", BestBefore: bb(2026, 9, 22), Percent: 30, Source: SourceManual},
	}
	d := BuildDigest(rows, 0)

	if len(d.Discounts) != 2 || len(d.Surplus) != 0 {
		t.Fatalf("секции: активных %d, допродажа %d, want 2 и 0", len(d.Discounts), len(d.Surplus))
	}
}

// Группа избытка — ОДНА позиция ёмкости с перечислением сроков: строки одного
// товара с Row.SurplusGroup свёрнуты в одну, скидка — максимальная по группе (она
// у ближайшего срока), коэффициент — наибольший (решение владельца 23.09.2026).
func TestBuildDigestGroupIsOneSlot(t *testing.T) {
	rows := []Row{
		{ProductID: "p1", Name: "Стейк Филе миньон Праймбиф. Охл.", BestBefore: bb(2026, 10, 15), Percent: 10, Source: SourceSurplus, Coeff: 1.2, SurplusGroup: true},
		{ProductID: "p1", Name: "Стейк Филе миньон Праймбиф. Охл.", BestBefore: bb(2026, 10, 22), Percent: 10, Source: SourceSurplus, Coeff: 1.0, SurplusGroup: true},
		{ProductID: "p2", Name: "Сыр Гауда", BestBefore: bb(2026, 10, 5), Percent: 30, Source: SourceExpiry},
	}
	d := BuildDigest(rows, 2)

	if len(d.Discounts) != 2 || len(d.Surplus) != 0 {
		t.Fatalf("секции: активных %d, допродажа %d, want 2 и 0", len(d.Discounts), len(d.Surplus))
	}
	// Приоритет: сроковая позиция впереди группы избытка, группа — одна строка.
	g := d.Discounts[1]
	if g.Name != "Стейк Филе миньон Праймбиф. Охл." || len(g.Dates) != 2 {
		t.Fatalf("группа: %+v", g)
	}
	if !g.Dates[0].Equal(bb(2026, 10, 15)) || !g.Dates[1].Equal(bb(2026, 10, 22)) {
		t.Errorf("сроки группы %v, want [15.10 22.10]", g.Dates)
	}
	if g.Percent != 10 || g.Coeff != 1.2 || !g.BestBefore.Equal(bb(2026, 10, 15)) {
		t.Errorf("группа: процент %d, коэф %v, ближний срок %v", g.Percent, g.Coeff, g.BestBefore)
	}
	if !strings.Contains(d.Text(), "(до 15.10, 22.10) — 10% (коэф 1,2)") {
		t.Errorf("в тексте нет перечисления сроков группы:\n%s", d.Text())
	}
}

// Golden-тест текста: 2 позиции в скидках (ручная + срок) и 1 за ёмкостью.
func TestDigestText(t *testing.T) {
	rows := []Row{
		{ProductID: "p3", Name: "Колбаса Докторская", BestBefore: bb(2026, 10, 12), Percent: SurplusPercent(), Source: SourceSurplus, Coeff: 1.6, DaysLeft: 5},
		{ProductID: "p2", Name: "Сыр Гауда", BestBefore: bb(2026, 10, 5), Percent: 10, Source: SourceExpiry, DaysLeft: 21},
		{ProductID: "p1", Name: "Хлеб Бородинский", BestBefore: bb(2026, 9, 22), Percent: 30, Source: SourceManual, DaysLeft: 8},
	}
	d := BuildDigest(rows, 2)
	d.Date = bb(2026, 9, 14)

	want := `Дайджест по скидкам · 14.09.2026

Позиции в скидках:
1. Хлеб Бородинский (до 22.09) — 30%
2. Сыр Гауда (до 05.10) — 10%

Доступно для допродажи (сверх 2 активных):
1. Колбаса Докторская (до 12.10) — 10% (коэф 1,6)
`
	if got := d.Text(); got != want {
		t.Errorf("Digest.Text():\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDigestTextEmptySections(t *testing.T) {
	date := bb(2026, 9, 14)
	tests := []struct {
		name string
		d    Digest
		want string
	}{
		{
			"обе секции пусты",
			Digest{Date: date},
			"Дайджест по скидкам · 14.09.2026\n\nПозиции в скидках: нет\n\nДоступно для допродажи: нет\n",
		},
		{
			"только за ёмкостью",
			Digest{Date: date, Cap: 12, Surplus: []Row{
				{Name: "Колбаса", BestBefore: bb(2026, 10, 12), Percent: 10, Source: SourceSurplus, Coeff: 2},
			}},
			"Дайджест по скидкам · 14.09.2026\n\nПозиции в скидках: нет\n\n" +
				"Доступно для допродажи (сверх 12 активных):\n" +
				"1. Колбаса (до 12.10) — 10% (коэф 2,0)\n",
		},
		{
			"только скидки",
			Digest{Date: date, Discounts: []Row{
				{Name: "Хлеб", BestBefore: bb(2026, 9, 22), Percent: 50, Source: SourceExpiry},
			}},
			"Дайджест по скидкам · 14.09.2026\n\nПозиции в скидках:\n1. Хлеб (до 22.09) — 50%\n\n" +
				"Доступно для допродажи: нет\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.Text(); got != tc.want {
				t.Errorf("Digest.Text():\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

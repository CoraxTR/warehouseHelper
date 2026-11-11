package stockreport

import (
	"strings"
	"testing"
)

func TestSplitClassification(t *testing.T) {
	positions := []Position{
		{Name: "Ноль с историей", Qty: 0, AvgWeek: new(3.0)},
		{Name: "Ноль без истории", Qty: 0},
		{Name: "История есть, остаток есть", Qty: 4, AvgWeek: nil},
		{Name: "Кончается", Qty: 2, AvgWeek: new(2.0)},
		{Name: "Избыток", Qty: 10, AvgWeek: new(3.0)},
		{Name: "Середина", Qty: 4, AvgWeek: new(3.0)},
		{Name: "Нулевая средняя", Qty: 5, AvgWeek: new(0.0)},
	}

	got := Split(positions)

	// qty = 0 попадает ТОЛЬКО в «нет в наличии», даже когда средняя известна,
	// и даже когда истории продаж нет вовсе (решение владельца 11.11.2026).
	if names := names(got.OutOfStock); strings.Join(names, ",") != "Ноль без истории,Ноль с историей" {
		t.Errorf("OutOfStock = %v, want [Ноль без истории Ноль с историей]", names)
	}

	if names := names(got.LowStock); strings.Join(names, ",") != "Кончается" {
		t.Errorf("LowStock = %v, want [Кончается]", names)
	}

	if names := names(got.Excess); strings.Join(names, ",") != "Избыток" {
		t.Errorf("Excess = %v, want [Избыток]", names)
	}

	// Без истории (nil) и с нулевой средней — в один список: делить на ноль
	// нельзя, продаж по сути не было.
	if names := names(got.NoHistory); strings.Join(names, ",") != "История есть, остаток есть,Нулевая средняя" {
		t.Errorf("NoHistory = %v, want [История есть, остаток есть Нулевая средняя]", names)
	}

	// Остаток больше недельной нормы, но меньше двух — никуда.
	for _, list := range [][]Position{got.OutOfStock, got.LowStock, got.Excess, got.NoHistory} {
		for _, p := range list {
			if p.Name == "Середина" {
				t.Errorf("«Середина» попала в список %v — не должна никуда", names(list))
			}
		}
	}
}

func TestReportLines(t *testing.T) {
	r := Split([]Position{
		{Name: "Молоко", Qty: 0},
		{Name: "Сыр", Qty: 2, AvgWeek: new(2.5)},
		{Name: "Говядина", Qty: 10, AvgWeek: new(3.0)},
		{Name: "Новинка", Qty: 7},
	})

	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{
			name:  "нет в наличии",
			lines: r.OutOfStockLines(),
			want:  []string{"Молоко - нет в наличии"},
		},
		{
			name:  "кончается",
			lines: r.LowStockLines(),
			want:  []string{"Сыр - в наличии: 2 - среднее в неделю: 2.5"},
		},
		{
			name:  "избыток и хвост без истории",
			lines: r.ExcessLines(),
			want: []string{
				"Говядина - в наличии: 10 - запас на 3.3 недель",
				"Новинка - нет истории продаж",
			},
		},
	}

	for _, c := range cases {
		if strings.Join(c.lines, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s:\n got  %q\n want %q", c.name, c.lines, c.want)
		}
	}
}

func TestReportEmpty(t *testing.T) {
	if !Split(nil).Empty() {
		t.Error("Empty() = false для пустого отчёта")
	}
	if Split([]Position{{Name: "x", Qty: 0}}).Empty() {
		t.Error("Empty() = true, а позиция «нет в наличии» есть")
	}
}

func TestSplitSortsByName(t *testing.T) {
	r := Split([]Position{
		{Name: "яблоко", Qty: 0},
		{Name: "Груша", Qty: 0},
		{Name: "абрикос", Qty: 0},
	})

	if got := strings.Join(names(r.OutOfStock), ","); got != "абрикос,Груша,яблоко" {
		t.Errorf("порядок = %q, want сортировку по имени без учёта регистра", got)
	}
}

func names(positions []Position) []string {
	out := make([]string, 0, len(positions))
	for _, p := range positions {
		out = append(out, p.Name)
	}

	return out
}

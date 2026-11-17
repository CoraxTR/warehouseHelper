package stockreport

import (
	"slices"
	"strings"
	"testing"
)

func TestSplitClassification(t *testing.T) {
	positions := []Position{
		{Name: "Ноль с историей", Qty: 0, AvgWeek: new(3.0), Coeff: 5},
		{Name: "Ноль без истории", Qty: 0},
		{Name: "Кончается с коэффициентом", Qty: 2, AvgWeek: new(2.0), Coeff: 3},
		{Name: "Избыток", Qty: 10, AvgWeek: new(3.0), Coeff: 2},
		{Name: "Коэффициент единица", Qty: 10, AvgWeek: new(3.0), Coeff: 1},
		{Name: "Середина", Qty: 4, AvgWeek: new(3.0)},
		{Name: "Без истории с коэффициентом", Qty: 4, Coeff: 2},
		{Name: "Без истории без коэффициента", Qty: 4},
		{Name: "Нулевая средняя с коэффициентом", Qty: 5, AvgWeek: new(0.0), Coeff: 3},
	}

	got := Split(positions)

	// qty = 0 попадает ТОЛЬКО в «нет в наличии», даже когда средняя и
	// коэффициент известны.
	if names := names(got.OutOfStock); strings.Join(names, ",") != "Ноль без истории,Ноль с историей" {
		t.Errorf("OutOfStock = %v, want [Ноль без истории Ноль с историей]", names)
	}

	// «Кончается» приоритетнее коэффициента: это действие важнее повода писать.
	if names := names(got.LowStock); strings.Join(names, ",") != "Кончается с коэффициентом" {
		t.Errorf("LowStock = %v, want [Кончается с коэффициентом]", names)
	}

	// Порог «запас ≥ 2 недель» отменён: в «избытке» только коэффициент > 1.
	if names := names(got.Excess); strings.Join(names, ",") !=
		"Без истории с коэффициентом,Избыток,Нулевая средняя с коэффициентом" {
		t.Errorf("Excess = %v, want [Без истории с коэффициентом Избыток Нулевая средняя с коэффициентом]", names)
	}

	// Остаток выше недельной нормы при коэффициенте 0/1 — никуда: повода нет.
	// Позиция без истории и без коэффициента — тоже никуда.
	nowhere := []string{"Коэффициент единица", "Середина", "Без истории без коэффициента"}
	for _, p := range append(got.OutOfStock, append(got.LowStock, got.Excess...)...) {
		if slices.Contains(nowhere, p.Name) {
			t.Errorf("«%s» попала в отчёт — не должна никуда", p.Name)
		}
	}
}

func TestReportItems(t *testing.T) {
	r := Split([]Position{
		{Name: "Молоко", Qty: 0},
		{Name: "Кот & Ко", Qty: 0},
		{Name: "Сыр", Qty: 2, AvgWeek: new(2.5)},
		{Name: "Говядина", Qty: 10, AvgWeek: new(3.0), Coeff: 4},
		{Name: "Новинка", Qty: 7, Coeff: 3},
	})

	cases := []struct {
		name  string
		items []string
		want  []string
	}{
		{
			name:  "нет в наличии",
			items: r.OutOfStockItems(),
			want: []string{
				"<b>1. Кот &amp; Ко</b>",
				"<b>2. Молоко</b>",
			},
		},
		{
			name:  "кончается",
			items: r.LowStockItems(),
			want: []string{
				"<b>1. Сыр</b>\n<code>остаток 2 шт · среднее 2.5/нед</code>",
			},
		},
		{
			name:  "избыток",
			items: r.ExcessItems(),
			want: []string{
				"<b>1. Говядина</b>\n<code>10 шт · запас 3.3 нед · коэф 4</code>",
				"<b>2. Новинка</b>\n<code>7 шт · нет истории продаж · коэф 3</code>",
			},
		},
	}

	for _, c := range cases {
		if strings.Join(c.items, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got  %q\n want %q", c.name, c.items, c.want)
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

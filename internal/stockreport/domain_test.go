package stockreport

import (
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

func avg(v float64) *float64 { return new(v) }

// Split: нулевой остаток уходит ТОЛЬКО в «нет в наличии»; без истории продаж
// сравнивать не с чем; избыток здесь не считается вовсе — его отдаёт модуль
// скидок.
func TestSplit(t *testing.T) {
	got := Split([]Position{
		{Name: "Пусто", Qty: 0, AvgWeek: avg(5)},
		{Name: "Кончается", Qty: 3, AvgWeek: avg(5)},
		{Name: "Ровно норма", Qty: 5, AvgWeek: avg(5)},
		{Name: "Без истории", Qty: 40},
		{Name: "Выше нормы", Qty: 40, AvgWeek: avg(5)},
	})

	if len(got.OutOfStock) != 1 || got.OutOfStock[0].Name != "Пусто" {
		t.Fatalf("нет в наличии = %+v, want только «Пусто»", got.OutOfStock)
	}
	if len(got.LowStock) != 2 {
		t.Fatalf("кончается = %+v, want «Кончается» и «Ровно норма»", got.LowStock)
	}
	if len(got.Surplus) != 0 {
		t.Errorf("избыток = %+v, want пусто (это расчёт модуля скидок)", got.Surplus)
	}
}

// «Кончается» — таблица из трёх колонок (решение владельца 25.11.2026):
// остаток и продажа разными колонками, средняя округлена до целого, слова
// «среднее» в отчёте нет.
func TestRichMarkdownLowStockTable(t *testing.T) {
	r := Report{LowStock: []Position{{Name: "Сыр", Qty: 3, AvgWeek: avg(4.6)}}}

	md := r.RichMarkdown()
	for _, want := range []string{
		"<summary>🟨 КОНЧАЕТСЯ · 1</summary>",
		"<table bordered compact>",
		"<th>Остаток</th><th>Продажа/нед</th>",
		"<tr><td>Сыр</td><td>3</td><td>5</td></tr>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в разметке нет %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "среднее") {
		t.Errorf("слово «среднее» осталось:\n%s", md)
	}
}

// «Избыток»: одна позиция на товар, имя склеено rowspan по строкам лотов,
// «накоплено / продадим» одной ячейкой, скидка — большее из сайта и ступени
// по сроку, коэффициента в таблице нет.
func TestRichMarkdownSurplusRowspan(t *testing.T) {
	forty, ten := int16(40), int16(10)
	r := Report{Surplus: []discounts.SurplusLot{
		{ProductID: "p1", Name: "Кальби", BestBefore: time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC), CumQty: 120, PlannedQty: 45, Coeff: 2.7, Expiry: &forty},
		{ProductID: "p1", Name: "Кальби", BestBefore: time.Date(2026, time.December, 2, 0, 0, 0, 0, time.UTC), CumQty: 160, PlannedQty: 95, Coeff: 1.7, Expiry: &ten},
		{ProductID: "p2", Name: "Специя", BestBefore: time.Date(2026, time.December, 3, 0, 0, 0, 0, time.UTC), CumQty: 30, PlannedQty: 12, Coeff: 1.6},
	}}

	md := r.RichMarkdown()
	for _, want := range []string{
		"<summary>🟦 ИЗБЫТОК · 2</summary>",
		`<td rowspan="2">Кальби</td><td>21.11</td><td>120 / ~45</td><td><b>40 %</b></td>`,
		"<td>02.12</td><td>160 / ~95</td><td><b>10 %</b></td>",
		"<td>Специя</td><td>03.12</td><td>30 / ~12</td><td>—</td>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в разметке нет %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "коэф") {
		t.Errorf("коэффициент остался в таблице:\n%s", md)
	}
	if got := strings.Count(md, "Кальби"); got != 1 {
		t.Errorf("имя товара встречается %d раз, want 1 (rowspan)", got)
	}
}

// «Нет в наличии» — нумерованный список под стрелкой, без таблицы.
func TestRichMarkdownOutOfStockList(t *testing.T) {
	r := Report{OutOfStock: []Position{{Name: "Соль Z&P"}, {Name: "Вырезка"}}}

	md := r.RichMarkdown()
	if !strings.Contains(md, "<summary>🟥 НЕТ В НАЛИЧИИ · 2</summary>") {
		t.Errorf("нет шапки блока:\n%s", md)
	}
	if !strings.Contains(md, "1. Соль Z&amp;P\n2. Вырезка") {
		t.Errorf("список или экранирование «&» сломаны:\n%s", md)
	}
	if strings.Contains(md, "<table") {
		t.Errorf("в блоке «нет в наличии» появилась таблица:\n%s", md)
	}
}

// Пустой блок не печатается, пустой отчёт — пустая строка: отправлять нечего.
func TestRichMarkdownSkipsEmptyBlocks(t *testing.T) {
	if got := (Report{}).RichMarkdown(); got != "" {
		t.Errorf("пустой отчёт = %q, want пусто", got)
	}

	md := Report{OutOfStock: []Position{{Name: "Пусто"}}}.RichMarkdown()
	if strings.Contains(md, "КОНЧАЕТСЯ") || strings.Contains(md, "ИЗБЫТОК") {
		t.Errorf("пустые блоки напечатаны:\n%s", md)
	}
}

package discounts

import "testing"

// Строка со ступенью по сроку не сворачивается с избыточной и не получает чужой
// источник — даже если флаг группы пришёл выставленным. Случай владельца
// (23.09.2026): «Филе-Миньон Signature — должна стоять скидка по срокам, а на
// последний срок избыток».
//
// С 25.11.2026 позиция в актуальных — одна на товар (ёмкость «12 строк = 12
// товаров»), остальные лоты ждут в «доступно для допродажи» своими строками.
func TestBuildDigestKeepsExpiryRowsOutOfGroup(t *testing.T) {
	rows := []Row{
		{ProductID: "p1", Name: "Стейк Филе-Миньон Мираторг Signature. Охл. 380 гр.", BestBefore: bb(2026, 9, 27), Percent: 20, Source: SourceExpiry, SurplusGroup: true},
		{ProductID: "p1", Name: "Стейк Филе-Миньон Мираторг Signature. Охл. 380 гр.", BestBefore: bb(2026, 9, 30), Percent: 20, Source: SourceExpiry},
		{ProductID: "p1", Name: "Стейк Филе-Миньон Мираторг Signature. Охл. 380 гр.", BestBefore: bb(2026, 10, 2), Percent: 10, Source: SourceSurplus, Coeff: 1.1, SurplusGroup: true},
	}

	d := BuildDigest(rows, 12)

	if len(d.Discounts) != 1 {
		t.Fatalf("активных строк %d, want 1 (%v)", len(d.Discounts), names(d.Discounts))
	}
	row := d.Discounts[0]
	if row.Source != SourceExpiry {
		t.Errorf("источник строки %v, want срок", row.Source)
	}
	if row.Percent != 20 {
		t.Errorf("строка получила чужой процент: %+v", row)
	}
	if row.Coeff != 0 {
		t.Errorf("строке приписан коэффициент избытка: %+v", row)
	}
	if len(row.Dates) != 0 {
		t.Errorf("строка по сроку свернулась в группу: %+v", row)
	}
	if !row.BestBefore.Equal(bb(2026, 9, 27)) {
		t.Errorf("ближний срок строки %v, want 27.09", row.BestBefore)
	}

	// Дальние лоты того же товара не выброшены — они ждут в дополнительном
	// списке своими строками, в порядке приоритета (срок ↑, затем избыток).
	if len(d.Surplus) != 2 {
		t.Fatalf("в дополнительном %d строк, want 2 (%v)", len(d.Surplus), names(d.Surplus))
	}
	if !d.Surplus[0].BestBefore.Equal(bb(2026, 9, 30)) || d.Surplus[0].Source != SourceExpiry {
		t.Errorf("первая строка дополнительного %+v, want 30.09 сроком", d.Surplus[0])
	}
	if !d.Surplus[1].BestBefore.Equal(bb(2026, 10, 2)) || d.Surplus[1].Source != SourceSurplus {
		t.Errorf("вторая строка дополнительного %+v, want 02.10 избытком", d.Surplus[1])
	}
}

// Товар попадает в актуальные один раз — первой своей строкой. Случай владельца
// (25.11.2026): Ти-Бон — ручная 30 % до 07.12 и избыток 10 % до 28.12; избыток
// в актуальных не показываем, но и не теряем: он в дополнительном списке.
func TestBuildDigestOneRowPerProduct(t *testing.T) {
	rows := []Row{
		{ProductID: "tbone", Name: "Стейк Ти-Бон Праймбиф. Охл.", BestBefore: bb(2026, 12, 7), Percent: 30, Source: SourceManual},
		{ProductID: "tbone", Name: "Стейк Ти-Бон Праймбиф. Охл.", BestBefore: bb(2026, 12, 28), Percent: 10, Source: SourceSurplus, Coeff: 1.2, SurplusGroup: true},
	}

	d := BuildDigest(rows, 12)

	if len(d.Discounts) != 1 {
		t.Fatalf("активных строк %d, want 1 (%v)", len(d.Discounts), names(d.Discounts))
	}
	row := d.Discounts[0]
	if row.Source != SourceManual || row.Percent != 30 {
		t.Errorf("строка %+v, want ручная 30 %% на 07.12", row)
	}
	if !row.BestBefore.Equal(bb(2026, 12, 7)) {
		t.Errorf("срок строки %v, want 07.12", row.BestBefore)
	}
	if len(d.Surplus) != 1 || !d.Surplus[0].BestBefore.Equal(bb(2026, 12, 28)) {
		t.Fatalf("дополнительный список %v, want одну строку 28.12", names(d.Surplus))
	}
}

// Освободившийся слот достаётся следующей позиции: дубль товара не съедает
// ёмкость окна.
func TestBuildDigestOneRowPerProductFreesSlot(t *testing.T) {
	rows := []Row{
		{ProductID: "tbone", Name: "Стейк Ти-Бон Праймбиф. Охл.", BestBefore: bb(2026, 12, 7), Percent: 30, Source: SourceManual},
		{ProductID: "tbone", Name: "Стейк Ти-Бон Праймбиф. Охл.", BestBefore: bb(2026, 12, 28), Percent: 10, Source: SourceSurplus, Coeff: 1.2, SurplusGroup: true},
		{ProductID: "sirloin", Name: "Стейк Сирлойн флэп Праймбиф. Охл.", BestBefore: bb(2026, 12, 9), Percent: 10, Source: SourceExpiry},
	}

	d := BuildDigest(rows, 2)

	if len(d.Discounts) != 2 {
		t.Fatalf("активных строк %d, want 2 (%v)", len(d.Discounts), names(d.Discounts))
	}
	if d.Discounts[1].ProductID != "sirloin" {
		t.Errorf("второй слот занял %q, want Сирлойн", d.Discounts[1].ProductID)
	}
	if len(d.Surplus) != 1 || d.Surplus[0].ProductID != "tbone" {
		t.Errorf("дополнительный список %v, want лот Ти-Бона", names(d.Surplus))
	}
}

// Ближний лот продался — его пара ушла из расчёта, и следующий лот товара
// поднимается в актуальные сам, без отдельного кода (владелец, 25.11.2026).
func TestBuildDigestPromotesNextLotAfterSale(t *testing.T) {
	first := Row{ProductID: "p1", Name: "Стейк Филе-Миньон. Охл.", BestBefore: bb(2026, 9, 27), Percent: 20, Source: SourceExpiry}
	next := Row{ProductID: "p1", Name: "Стейк Филе-Миньон. Охл.", BestBefore: bb(2026, 9, 30), Percent: 20, Source: SourceExpiry}

	before := BuildDigest([]Row{first, next}, 12)
	if len(before.Discounts) != 1 || !before.Discounts[0].BestBefore.Equal(bb(2026, 9, 27)) {
		t.Fatalf("до продажи в актуальных %v, want 27.09", names(before.Discounts))
	}

	after := BuildDigest([]Row{next}, 12)
	if len(after.Discounts) != 1 || !after.Discounts[0].BestBefore.Equal(bb(2026, 9, 30)) {
		t.Fatalf("после продажи в актуальных %v, want 30.09", names(after.Discounts))
	}
	if len(after.Surplus) != 0 {
		t.Errorf("дополнительный список после продажи %v, want пусто", names(after.Surplus))
	}
}

// Постоянные скидки правило не касается: их карточка может стоять и в слотах, и
// хвостом, а постоянные лоты одного товара не сворачиваются.
func TestBuildDigestKeepsPermanentRows(t *testing.T) {
	rows := []Row{
		{ProductID: "tbone", Name: "Стейк Ти-Бон Праймбиф. Охл.", BestBefore: bb(2026, 12, 7), Percent: 30, Source: SourceManual},
		{ProductID: "tbone", Name: "Стейк Ти-Бон Праймбиф. Охл.", BestBefore: bb(2026, 12, 14), Percent: 5, Source: SourcePermanent, Permanent: true},
		{ProductID: "tbone", Name: "Стейк Ти-Бон Праймбиф. Охл.", BestBefore: bb(2026, 12, 21), Percent: 5, Source: SourcePermanent, Permanent: true},
	}

	d := BuildDigest(rows, 12)

	if len(d.Discounts) != 3 {
		t.Fatalf("активных строк %d, want 3 (%v)", len(d.Discounts), names(d.Discounts))
	}
	if d.Discounts[0].Source != SourceManual || d.Discounts[0].Percent != 30 {
		t.Errorf("первая строка %+v, want ручная 30 %%", d.Discounts[0])
	}
	for _, r := range d.Discounts[1:] {
		if !r.Permanent {
			t.Errorf("постоянная строка потеряла признак: %+v", r)
		}
	}
}

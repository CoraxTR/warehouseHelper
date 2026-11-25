package discounts

import "testing"

// Строка со ступенью по сроку не сворачивается с избыточной и не получает чужой
// источник — даже если флаг группы пришёл выставленным. Случай владельца
// (23.09.2026): «Филе-Миньон Signature — должна стоять скидка по срокам, а на
// последний срок избыток».
//
// С 25.11.2026 действует правило «одна карточка товара — одна позиция», поэтому
// позиция у товара одна: ближний срок скидкой по сроку. Проверяем и то, и
// другое: строка не получила чужой источник, и товар не размножился.
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
}

// Одна карточка товара — одна позиция: избыток на дальнем сроке не появляется
// вторым разом, если на ближнем сроке стоит ручная скидка. Случай владельца
// (25.11.2026): Ти-Бон — ручная 30 % до 07.12 и избыток 10 % до 28.12.
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
	if len(d.Surplus) != 0 {
		t.Errorf("в «доступно для допродажи» попал дубль товара: %v", names(d.Surplus))
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
	if len(d.Surplus) != 0 {
		t.Errorf("позиция ушла в «доступно для допродажи»: %v", names(d.Surplus))
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

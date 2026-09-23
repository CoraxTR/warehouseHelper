package discounts

import "testing"

// Строка со ступенью по сроку не сворачивается с избыточной и не получает чужой
// источник — даже если флаг группы пришёл выставленным. Случай владельца
// (23.09.2026): «Филе-Миньон Signature — должна стоять скидка по срокам, а на
// последний срок избыток».
func TestBuildDigestKeepsExpiryRowsOutOfGroup(t *testing.T) {
	rows := []Row{
		{ProductID: "p1", Name: "Стейк Филе-Миньон Мираторг Signature. Охл. 380 гр.", BestBefore: bb(2026, 9, 27), Percent: 20, Source: SourceExpiry, SurplusGroup: true},
		{ProductID: "p1", Name: "Стейк Филе-Миньон Мираторг Signature. Охл. 380 гр.", BestBefore: bb(2026, 9, 30), Percent: 20, Source: SourceExpiry},
		{ProductID: "p1", Name: "Стейк Филе-Миньон Мираторг Signature. Охл. 380 гр.", BestBefore: bb(2026, 10, 2), Percent: 10, Source: SourceSurplus, Coeff: 1.1, SurplusGroup: true},
	}

	d := BuildDigest(rows, 12)

	if len(d.Discounts) != 3 {
		t.Fatalf("активных строк %d, want 3 (%v)", len(d.Discounts), names(d.Discounts))
	}
	for _, r := range d.Discounts {
		if r.Source != SourceExpiry {
			continue
		}
		if len(r.Dates) != 0 {
			t.Errorf("строка по сроку свернулась в группу: %+v", r)
		}
		if r.Percent != 20 {
			t.Errorf("строка по сроку получила чужой процент: %+v", r)
		}
		if r.Coeff != 0 {
			t.Errorf("строке по сроку приписан коэффициент избытка: %+v", r)
		}
	}
	last := d.Discounts[2]
	if last.Source != SourceSurplus || last.Percent != 10 {
		t.Fatalf("последняя строка: %+v, want избыток 10 %%", last)
	}
	if len(last.Dates) != 1 || !last.Dates[0].Equal(bb(2026, 10, 2)) {
		t.Errorf("сроки избыточной строки %v, want [02.10]", last.Dates)
	}
}

package usecase

import (
	"testing"

	"warehouseHelper/internal/discounts"
)

// Источник строки — победившая скидка, а не наличие избытка: если ступень по
// сроку глубже, строка печатается «по сроку» и в группу избытка не попадает
// (жалоба владельца 23.09.2026: «Стейк Ковбой должен быть скидка по сроку, а в
// таблице написан избыток при скидке 30 %»).
func TestRowSourceIsWinnerAndGroupOnlyForSurplus(t *testing.T) {
	expiry30 := int16(30)
	surplus10 := discounts.SurplusPercent()

	// Ковбой: скидка по сроку 30 % перекрывает избыток (коэф 2,1).
	expiryPair := PairState{
		ProductID:    "p1",
		Name:         "Стейк Ковбой Праймбиф. Охл.",
		BestBefore:   day(5),
		DaysLeft:     5,
		Expiry:       &expiry30,
		Surplus:      &surplus10,
		HasSurplus:   true,
		Coeff:        2.1,
		SurplusGroup: true, // расчёт пометил пару группой — но избыток тут не победитель
		Qty:          10,
	}

	row := expiryPair.Row()
	if row.Source != discounts.SourceExpiry {
		t.Errorf("источник строки %s, want %s (скидка по сроку)", row.Source, discounts.SourceExpiry)
	}
	if row.SurplusGroup {
		t.Error("пара со ступенью по сроку в группу избытка попадать не должна")
	}
	if row.Percent != 30 {
		t.Errorf("скидка %d, want 30", row.Percent)
	}

	// Избыток без ступени по сроку — источник «избыток», пара в группе.
	surplusPair := PairState{
		ProductID:    "p2",
		Name:         "Филе миньон Праймбиф. Охл.",
		BestBefore:   day(7),
		DaysLeft:     7,
		Surplus:      &surplus10,
		HasSurplus:   true,
		Coeff:        1.2,
		SurplusGroup: true,
		Qty:          10,
	}

	row = surplusPair.Row()
	if row.Source != discounts.SourceSurplus || !row.SurplusGroup {
		t.Errorf("пара с избытком: источник %s, группа %v — want избыток и группу",
			row.Source, row.SurplusGroup)
	}
	if row.Percent != discounts.SurplusPercent() {
		t.Errorf("скидка %d, want %d", row.Percent, discounts.SurplusPercent())
	}
}

// Ручная скидка важнее избытка так же: пара печатается источником «ручная».
func TestRowSourceManualBeatsSurplus(t *testing.T) {
	manual15 := int16(15)
	surplus10 := discounts.SurplusPercent()

	p := PairState{
		ProductID:    "p3",
		Name:         "Стейк Ти-Бон. Зам.",
		BestBefore:   day(9),
		DaysLeft:     9,
		Manual:       &manual15,
		Surplus:      &surplus10,
		HasSurplus:   true,
		Coeff:        1.4,
		SurplusGroup: true,
		Qty:          5,
	}

	row := p.Row()
	if row.Source != discounts.SourceManual {
		t.Errorf("источник строки %s, want %s", row.Source, discounts.SourceManual)
	}
	if row.SurplusGroup {
		t.Error("пара с ручной скидкой в группу избытка попадать не должна")
	}
	if row.Percent != 15 {
		t.Errorf("скидка %d, want 15", row.Percent)
	}
}

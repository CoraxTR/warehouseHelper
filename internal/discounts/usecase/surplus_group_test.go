package usecase

import (
	"testing"

	"warehouseHelper/internal/discounts"
)

// surplusPtr — скидка избытка указателем: PairState.Surplus хранит адрес значения.
func surplusPtr() *int16 {
	percent := discounts.SurplusPercent()
	return &percent
}

// pairWith — пара товара с коэффициентом избытка, как её отдаёт расчёт: свой
// избыток (коэффициент > 1) приходит вместе со скидкой, у остальных пар её нет.
func pairWith(daysLeft int, coeff float64) PairState {
	p := PairState{DaysLeft: daysLeft, Coeff: coeff, Qty: 10}
	if coeff > 1 {
		p.HasSurplus = true
		p.Surplus = surplusPtr()
	}
	return p
}

// Группа избытка (опора A, решение владельца 23.09.2026): избыток на сроке
// распространяется на все более близкие сроки товара. Пары идут по возрастанию
// срока — проверяем группы прямо на функции, без входа расчёта.
func TestApplySurplusGroupPrefix(t *testing.T) {
	tests := []struct {
		name string
		// coeff — коэффициенты пар от ближнего срока к дальнему.
		coeff []float64
		want  []bool // в группе ли пара (по порядку сроков)
	}{
		{
			name:  "избыток только на дальнем: группа — все три срока",
			coeff: []float64{0.8, 1.4, 2.0},
			want:  []bool{true, true, true},
		},
		{
			name:  "дальний без избытка: группа кончается на избыточном",
			coeff: []float64{0.8, 1.4, 0.5},
			want:  []bool{true, true, false},
		},
		{
			name:  "средний срок продали: дальний держит группу на всех",
			coeff: []float64{0.8, 0.9, 1.3},
			want:  []bool{true, true, true},
		},
		{
			name:  "избытка нет нигде",
			coeff: []float64{0.8, 0.9, 0.5},
			want:  []bool{false, false, false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pairs := make([]PairState, 0, len(tc.coeff))
			for i, c := range tc.coeff {
				pairs = append(pairs, pairWith(10+i, c))
			}

			applySurplusGroup(pairs)

			for i, want := range tc.want {
				if pairs[i].SurplusGroup != want {
					t.Fatalf("пара %d: в группе %v, want %v (коэффициенты %v)", i, pairs[i].SurplusGroup, want, tc.coeff)
				}
			}
		})
	}
}

// Паре без своего избытка группа ставит скидку 10 % и коэффициент группы: по нему
// она и печатается, и попадает в дайджест как обычная активная позиция, и уходит
// в БД (метку снимает модуль по SourceRaw).
func TestApplySurplusGroupMemberGetsDiscount(t *testing.T) {
	pairs := []PairState{
		pairWith(8, 0.7),
		pairWith(9, 1.6),
		pairWith(20, 0.4),
	}

	applySurplusGroup(pairs)

	if !pairs[0].HasSurplus || pairs[0].Surplus == nil || *pairs[0].Surplus != discounts.SurplusPercent() {
		t.Fatalf("ближняя пара группы: избыток %v/%v, want %d", pairs[0].HasSurplus, pairs[0].Surplus, discounts.SurplusPercent())
	}
	if pairs[0].Coeff != 1.6 {
		t.Errorf("коэффициент группы у пары %v, want 1.6", pairs[0].Coeff)
	}
	if pairs[2].SurplusGroup || pairs[2].HasSurplus {
		t.Errorf("дальняя пара без избытка попадать в группу не должна: %+v", pairs[2])
	}
}

// Просроченная пара в группу не входит: скидка ей смысла не имеет (и в отчёте ей
// не место).
func TestApplySurplusGroupSkipsExpired(t *testing.T) {
	pairs := []PairState{
		pairWith(0, 0),
		pairWith(9, 1.7),
	}

	applySurplusGroup(pairs)

	if pairs[0].SurplusGroup || pairs[0].Surplus != nil {
		t.Fatalf("просроченная пара в группе: %+v", pairs[0])
	}
}

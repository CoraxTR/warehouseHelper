package usecase

import (
	"math"
	"testing"

	"warehouseHelper/internal/discounts"
)

// testPairPID — товар тестовых пар: в плане добора все пары одного товара
// (оборот у них общий), поэтому имя товара у хелперов одно.
const testPairPID = "p1"

// salePair — пара товара для плана добора: срок в днях от дня расчёта, остаток
// лота, накопленный FIFO-остаток (CumQty), дневной оборот товара и признак
// группы избытка. Оборот товара один на все его пары, поэтому v одна и та же;
// Coeff пары и её метка избытка заполняются, как их отдал бы Evaluate.
func salePair(daysLeft int, qty, cumQty int64, rate float64) PairState {
	return pairState(daysLeft, qty, cumQty, rate)
}

// groupPair — пара, помеченная участником группы избытка: скидка по избытку
// ложится и на неё (так их метит Evaluate).
func groupPair(daysLeft int, qty, cumQty int64, rate float64) PairState {
	p := pairState(daysLeft, qty, cumQty, rate)
	p.SurplusGroup = true
	p.HasSurplus = true
	p.Surplus = surplusPtr()
	return p
}

// pairState — состояние пары без признака группы избытка.
func pairState(daysLeft int, qty, cumQty int64, rate float64) PairState {
	bb := day(daysLeft)
	p := PairState{
		Key:        discounts.LotKey{ProductID: testPairPID, BestBefore: bb},
		ProductID:  testPairPID,
		Name:       testPairPID,
		BestBefore: bb,
		DaysLeft:   daysLeft,
		Qty:        qty,
		CumQty:     cumQty,
		Rate:       rate,
		HasRate:    rate > 0,
	}
	if rate > 0 && daysLeft > 0 {
		p.Coeff = float64(cumQty) / (rate * float64(daysLeft))
	}
	return p
}

// coeffsAfterSale — коэффициенты избытка пар ГРУППЫ после продажи x штук по
// раскладке плана: FIFO с ближних сроков, паре достаётся не больше её остатка.
// CumQty пары уже включает остатки пар впереди (в том числе просроченных, вне
// группы), поэтому из накопленного остатка вычитаем только то, что снято внутри
// группы. Нужен, чтобы в тесте проверить ЛЮБОЙ объём — хоть план, хоть «на глаз»
// владельца, — и убедиться, что коэффициент стал < 1 у всех пар.
func coeffsAfterSale(pairs []PairState, x int64) []float64 {
	group := surplusGroup(pairs)
	left := x
	taken := int64(0)
	coeffs := make([]float64, 0, len(group))
	for _, p := range group {
		take := max(min(p.Qty, left), 0)
		left -= take
		taken += take
		coeffs = append(coeffs, float64(p.CumQty-taken)/(p.Rate*float64(p.DaysLeft)))
	}
	return coeffs
}

// wantSale — ожидаемая запись раскладки: индекс пары во входе и объём продажи.
type wantSale struct {
	pair int
	qty  int64
}

// План добора слота из избытка: объём и раскладка по парам группы (FIFO).
func TestSurplusSalePlan(t *testing.T) {
	tests := []struct {
		name  string
		pairs []PairState
		wantX int64
		want  []wantSale
		// coeffs — коэффициенты пар группы после продажи плана; nil — не
		// проверяем (в этих кейсах интересен только объём).
		coeffs []float64
	}{
		{
			name: "у всех пар кф уже < 1 — продавать нечего",
			// 9/(0,5×20) = 0,9 и 15/(0,5×30) = 1,0: лишков нет (у второй
			// need = 0 — «ровно столько, сколько продаётся»), группа не
			// помечена, значит и плана нет.
			pairs: []PairState{
				salePair(20, 9, 9, 0.5),
				salePair(30, 6, 15, 0.5),
			},
		},
		{
			name: "одна пара в группе: объём меньше остатка",
			// v×D = 0,1×20 = 2, need = 10 − 2 = 8 → вклад 9; дальняя пара вне
			// группы (избытка нет) и в раскладку не входит. После продажи
			// остаток 1, кф 1/2 = 0,5 < 1.
			pairs: []PairState{
				groupPair(20, 10, 10, 0.1),
				salePair(60, 7, 17, 0.1),
			},
			wantX:  9,
			want:   []wantSale{{pair: 0, qty: 9}},
			coeffs: []float64{0.5},
		},
		{
			name: "объём упирается в остаток группы: распродаём всё",
			// v×D = 0,5 и 1,0; need = 9,5 и 14 → вклады 10 и 15. Максимум 15
			// равен всему остатку группы, раскладка съедает обе пары (дальняя —
			// целиком), кф последней 0 < 1. Пара за группой не в счёте.
			pairs: []PairState{
				groupPair(5, 10, 10, 0.1),
				groupPair(10, 5, 15, 0.1),
				salePair(40, 4, 19, 0.1),
			},
			wantX:  15,
			want:   []wantSale{{pair: 0, qty: 10}, {pair: 1, qty: 5}},
			coeffs: []float64{0, 0},
		},
		{
			name: "просроченный остаток впереди: объём ограничен остатком группы",
			// Просроченная пара (D = 0) в группу не берётся, но её 40 штук
			// входят в CumQty, поэтому need = 46 − 4 = 42 и вклад 43 — больше,
			// чем есть у группы. Потолок — остаток группы: 6 штук, всё, что
			// можно продать. Правило «< 1» держится ровно настолько, насколько
			// позволяет остаток (кф после списания — 40/4 = 10: впереди лежит
			// нераспродаваемое).
			pairs: []PairState{
				salePair(0, 40, 40, 0.5),
				groupPair(8, 6, 46, 0.5),
			},
			wantX: 6,
			want:  []wantSale{{pair: 1, qty: 6}},
		},
		{
			name: "нет данных оборота (Rate = 0) — избытка нет",
			// Защитный случай: такой пары расчёт в группу не поставит (нет
			// оборота — нет и коэффициента), но и помеченная пара без оборота не
			// должна ни давать вклад, ни делить на ноль — объёма нет.
			pairs: []PairState{
				groupPair(20, 10, 10, 0),
				groupPair(30, 5, 15, 0),
			},
		},
		{
			name:  "пустой вход",
			pairs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotX, got := SurplusSalePlan(tc.pairs)
			if gotX != tc.wantX {
				t.Fatalf("объём X = %d, want %d (раскладка %v)", gotX, tc.wantX, got)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("раскладка %v, want %d записей", got, len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].Key != tc.pairs[w.pair].Key {
					t.Errorf("запись %d: ключ %v, want ключ пары %d (%v)",
						i, got[i].Key, w.pair, tc.pairs[w.pair].Key)
				}
				if got[i].Qty != w.qty {
					t.Errorf("запись %d (%v): %d шт, want %d",
						i, got[i].Key, got[i].Qty, w.qty)
				}
			}
			if tc.coeffs == nil {
				return
			}
			after := coeffsAfterSale(tc.pairs, gotX)
			for i, want := range tc.coeffs {
				if math.Abs(after[i]-want) > 1e-9 {
					t.Errorf("пара %d: кф после плана %v, want %v", i, after[i], want)
				}
			}
		})
	}
}

// ownerExamplePairs — контрольный пример владельца: пары 9 до 30.09 (кф 1,0),
// 6 до 02.10 (кф 1,5) и 10 до 05.10 (кф 2,0). Числа иллюстративные, поэтому дни
// и оборот подобраны под эти коэффициенты: v = 0,5 шт/день, остатки дней 18/20/25
// дают CumQty/(v×D) = 9/9, 15/10 и 25/12,5 ровно те самые 1,0 / 1,5 / 2,0.
func ownerExamplePairs() []PairState {
	return []PairState{
		groupPair(18, 9, 9, 0.5),
		groupPair(20, 6, 15, 0.5),
		groupPair(25, 10, 25, 0.5),
	}
}

// Контрольный пример владельца: плана «на глаз» (10 шт) не хватает, правило
// «строго < 1 у всех» даёт 13 — и продажа уходит во второй срок.
func TestSurplusSalePlanOwnerExample(t *testing.T) {
	pairs := ownerExamplePairs()

	// Лишки: need = CumQty − v×D = 0 (первая пара уже без избытка) / 5 / 12,5.
	// Вклады: у первой нет вовсе, у второй 6, у третьей floor(12,5) + 1 = 13.
	// Максимум — 13, значит X = 13, и FIFO забирает 9 у ближней пары (она
	// распродана) и 4 у второй: раскладка из двух записей, а не из одной.
	x, sales := SurplusSalePlan(pairs)
	if x != 13 {
		t.Fatalf("объём X = %d, want 13 (раскладка %v)", x, sales)
	}
	if len(sales) != 2 {
		t.Fatalf("раскладка %v, want две записи: ближняя пара целиком и остаток второй", sales)
	}
	if sales[0].Key != pairs[0].Key || sales[0].Qty != 9 {
		t.Errorf("запись 0: %v %d шт, want %v 9 шт", sales[0].Key, sales[0].Qty, pairs[0].Key)
	}
	if sales[1].Key != pairs[1].Key || sales[1].Qty != 4 {
		t.Errorf("запись 1: %v %d шт, want %v 4 шт", sales[1].Key, sales[1].Qty, pairs[1].Key)
	}

	// После плана: 0 + 2 + 10 = 12 против v×D = 12,5 → кф третьей пары 0,96,
	// и у всех трёх кф строго меньше единицы — правило выполнено.
	after := coeffsAfterSale(pairs, x)
	for i, c := range after {
		if c >= 1 {
			t.Errorf("пара %d: кф после плана %v, want < 1", i, c)
		}
	}

	// Оценка владельца X = 10 «на глаз» правилу не отвечает: продажа забирает
	// 9 у ближней пары и 1 у второй, накопленный остаток третьей остаётся
	// 0 + 5 + 10 = 15, то есть её кф 15/12,5 = 1,2 — избыток не закрыт.
	// Правило «строго < 1 у всех» даёт другое число (13), и в рассылку идёт оно.
	if c := coeffsAfterSale(pairs, 10)[2]; math.Abs(c-1.2) > 1e-9 {
		t.Errorf("кф третьей пары после продажи 10 = %v, want 1,2", c)
	}
}

package usecase

import (
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

func banFixtures() (time.Time, func(int) time.Time, func(int16) *int16, func(time.Time, *int16, *int16) discounts.Input) {
	today := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	bb := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	i16 := func(v int16) *int16 { return &v }
	input := func(bestBefore time.Time, manual, plain *int16) discounts.Input {
		return discounts.Input{
			ProductID:      "p1",
			Name:           "Товар",
			BestBefore:     bestBefore,
			Qty:            1,
			GeneralManual:  manual,
			GeneralPlain:   plain,
			DiscountSource: discounts.SourceExpiry.String(),
		}
	}
	return today, bb, i16, input
}

// Запрет менеджера: ручная 0 блокирует все сроки дальше того, на который
// поставлена (решение владельца, 15.09.2026): накрытая пара и все пары товара
// с более далёким сроком из автоматических скидок выпадают, ближние живут.
func TestEvaluateBanCascade(t *testing.T) {
	today, bb, i16, input := banFixtures()

	tests := []struct {
		name    string
		inputs  []discounts.Input
		wantBan []bool // по порядку сроков: накрыта ли пара запретом
	}{
		{
			name: "запрет на ближней паре накрывает все",
			inputs: []discounts.Input{
				input(bb(10), i16(0), nil),
				input(bb(20), nil, i16(40)),
				input(bb(30), nil, nil),
			},
			wantBan: []bool{true, true, true},
		},
		{
			name: "запрет на средней паре не трогает ближнюю",
			inputs: []discounts.Input{
				input(bb(10), nil, i16(40)),
				input(bb(20), i16(0), nil),
				input(bb(30), nil, i16(10)),
			},
			wantBan: []bool{false, true, true},
		},
		{
			name: "без запрета пары живут",
			inputs: []discounts.Input{
				input(bb(10), nil, i16(40)),
				input(bb(20), i16(30), nil),
			},
			wantBan: []bool{false, false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pairs := Evaluate(tc.inputs, nil, today)
			if len(pairs) != len(tc.wantBan) {
				t.Fatalf("пар %d, want %d", len(pairs), len(tc.wantBan))
			}
			for i, want := range tc.wantBan {
				if pairs[i].Frozen() != want {
					t.Errorf("пара %s: Frozen = %v, want %v",
						pairs[i].BestBefore.Format(time.DateOnly), pairs[i].Frozen(), want)
				}
			}
		})
	}
}

// Накрытая каскадом пара без ручной не имеет плана: скидки по ней быть не
// должно. Пара с самой ручной 0 отдаёт (0, manual) — «скидка 0 %».
func TestBlockedPairHasNoDesired(t *testing.T) {
	today, bb, i16, input := banFixtures()

	pairs := Evaluate([]discounts.Input{
		input(bb(10), i16(0), nil),
		input(bb(20), nil, i16(40)),
	}, nil, today)

	if pct, src := pairs[1].Desired(); pct != nil || src != discounts.SourceNone {
		t.Errorf("Desired накрытой пары = (%v, %v), want (nil, none)", pct, src)
	}
	pct, src := pairs[0].Desired()
	if pct == nil || *pct != 0 || src != discounts.SourceManual {
		t.Errorf("Desired пары с ручной 0 = (%v, %v), want (0, manual)", pct, src)
	}
}

// Каскадный запрет снимает ступень, поставленную движком до запрета, и не
// трогает пары вне запрета.
func TestBanWritesClearBlockedPlain(t *testing.T) {
	today, bb, i16, input := banFixtures()

	pairs := Evaluate([]discounts.Input{
		input(bb(10), nil, i16(40)),    // ближняя: не накрыта — скидку не снимаем
		input(bb(20), i16(0), i16(20)), // запрет: своя ступень — снять
		input(bb(30), nil, i16(10)),    // накрыта каскадом — снять
		input(bb(40), nil, nil),        // накрыта, но снимать нечего
	}, nil, today)

	writes := banWrites(pairs)
	if len(writes) != 2 {
		t.Fatalf("правок %d, want 2", len(writes))
	}

	got := make(map[string]bool, len(writes))
	for _, w := range writes {
		if w.General != nil {
			t.Errorf("правка %s: General = %d, want nil (снятие)", w.BestBefore.Format(time.DateOnly), *w.General)
		}
		got[w.BestBefore.Format(time.DateOnly)] = true
	}
	if !got["2026-09-20"] || !got["2026-09-30"] {
		t.Errorf("сняты не те пары: %v, want 2026-09-20 и 2026-09-30", got)
	}
	if got["2026-09-10"] {
		t.Error("снята пара вне запрета (10.09)")
	}
}

// Запрет по ТГ-колонке работает так же, как по колонке сайта (каналы
// симметричны, решение владельца 15.09.2026).
func TestEvaluateBanFromTelegramManual(t *testing.T) {
	today, bb, i16, _ := banFixtures()

	tg := i16(0)
	pairs := Evaluate([]discounts.Input{
		{ProductID: "p1", Name: "Товар", BestBefore: bb(10), Qty: 1},
		{ProductID: "p1", Name: "Товар", BestBefore: bb(20), Qty: 1, TelegramManual: tg},
		{ProductID: "p1", Name: "Товар", BestBefore: bb(30), Qty: 1, GeneralPlain: i16(40)},
	}, nil, today)

	want := []bool{false, true, true}
	for i, w := range want {
		if pairs[i].Frozen() != w {
			t.Errorf("пара %s: Frozen = %v, want %v",
				pairs[i].BestBefore.Format(time.DateOnly), pairs[i].Frozen(), w)
		}
	}
}

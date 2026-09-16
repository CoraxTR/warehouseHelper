package usecase

import (
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// banFixture — общие значения тестов каскадного запрета: дата расчёта, генератор
// сроков и сборка пары. Держим их в структуре, а не в четырёх возвращаемых
// значениях: больше трёх результатов функция отдавать не должна (revive
// function-result-limit).
type banFixture struct {
	today time.Time
	bb    func(int) time.Time
	input func(time.Time, *int16, *int16) discounts.Input
}

func banFixtures() banFixture {
	today := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)
	bb := func(day int) time.Time {
		return time.Date(2026, time.September, day, 0, 0, 0, 0, time.UTC)
	}
	input := func(bestBefore time.Time, manual *int16, plain *int16) discounts.Input {
		return discounts.Input{
			ProductID:     "p1",
			Name:          "Товар",
			BestBefore:    bestBefore,
			Qty:           1,
			GeneralManual: manual,
			GeneralPlain:  plain,
		}
	}

	return banFixture{today: today, bb: bb, input: input}
}

// Запрет менеджера: ручная 0 блокирует все сроки дальше того, на который
// поставлена (решение владельца, 15.09.2026): накрытая пара и все пары товара
// с более далёким сроком из автоматических скидок выпадают, ближние живут.
func TestEvaluateBanCascade(t *testing.T) {
	f := banFixtures()

	tests := []struct {
		name    string
		inputs  []discounts.Input
		wantBan []bool // накрыта ли запретом пара, по порядку сроков
	}{
		{
			name: "запрет на ближней паре накрывает все",
			inputs: []discounts.Input{
				f.input(f.bb(10), new(int16(0)), nil),
				f.input(f.bb(20), nil, new(int16(40))),
				f.input(f.bb(30), nil, nil),
			},
			wantBan: []bool{true, true, true},
		},
		{
			name: "запрет на средней паре не трогает ближнюю",
			inputs: []discounts.Input{
				f.input(f.bb(10), nil, new(int16(40))),
				f.input(f.bb(20), new(int16(0)), nil),
				f.input(f.bb(30), nil, new(int16(10))),
			},
			wantBan: []bool{false, true, true},
		},
		{
			name: "без запрета пары живут",
			inputs: []discounts.Input{
				f.input(f.bb(10), nil, new(int16(40))),
				f.input(f.bb(20), new(int16(30)), nil),
			},
			wantBan: []bool{false, false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pairs := Evaluate(tc.inputs, nil, f.today)
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

// Накрытая каскадом пара без ручной не имеет плана: скидки по ней быть не должно,
// а пара с ручной 0 % остаётся победителем своего источника.
func TestBlockedPairHasNoDesired(t *testing.T) {
	f := banFixtures()

	pairs := Evaluate([]discounts.Input{
		f.input(f.bb(10), new(int16(0)), nil),
		f.input(f.bb(20), nil, new(int16(40))),
	}, nil, f.today)

	pct, src := pairs[1].Desired()
	if pct != nil || src != discounts.SourceNone {
		t.Errorf("Desired накрытой пары = (%v, %v), want (nil, none)", pct, src)
	}

	pct, src = pairs[0].Desired()
	if pct == nil || *pct != 0 || src != discounts.SourceManual {
		t.Errorf("Desired пары с ручной 0 = (%v, %v), want (0, manual)", pct, src)
	}
}

// Каскадный запрет снимает ступень, поставленную движком до запрета, и не даёт
// снимать ничего у пар вне запрета.
func TestBanWritesClearBlockedPlain(t *testing.T) {
	f := banFixtures()

	pairs := Evaluate([]discounts.Input{
		f.input(f.bb(10), nil, new(int16(40))),           // ближняя: не накрыта, скидку не снимаем
		f.input(f.bb(20), new(int16(0)), new(int16(20))), // запрет: пара со своей ступенью
		f.input(f.bb(30), nil, new(int16(10))),           // накрыта каскадом — снять
		f.input(f.bb(40), nil, nil),                      // накрыта, но снимать нечего
	}, nil, f.today)

	writes := banWrites(pairs)
	if len(writes) != 2 {
		t.Fatalf("правок %d, want 2 (пары 20.09 и 30.09)", len(writes))
	}

	cleared := make(map[string]bool, len(writes))
	for _, w := range writes {
		if w.General != nil {
			t.Errorf("правка %s: General = %v, want nil (снятие)",
				w.BestBefore.Format(time.DateOnly), *w.General)
		}
		cleared[w.BestBefore.Format(time.DateOnly)] = true
	}

	if !cleared["2026-09-20"] || !cleared["2026-09-30"] {
		t.Errorf("сняты не те пары: %v, want 2026-09-20 и 2026-09-30", cleared)
	}
}

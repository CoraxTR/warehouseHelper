package usecase

import (
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
)

func TestWindowAvg(t *testing.T) {
	// Месячные периоды: ноябрь 2025 … октябрь 2026 (завершённые, по убыванию),
	// qty = 0..11 (finished[0] — самый свежий = 0, finished[11] — самая дальняя = 11).
	mkMonth := func(y, m int, q float64) averagesales.TurnoverRow {
		return averagesales.TurnoverRow{ProductID: "p1", PeriodStart: time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC), Qty: q}
	}
	finished := []averagesales.TurnoverRow{}
	for i, y, m := 0, 2026, 10; ; i++ {
		finished = append(finished, mkMonth(y, m, float64(i)))
		if y == 2025 && m == 11 {
			break
		}
		m--
		if m == 0 {
			y--
			m = 12
		}
	}
	// finished: 2026-10(q0), 2026-09(q1), …, 2025-11(q11) — 12 месяцев по убыванию.
	if len(finished) != 12 {
		t.Fatalf("prep: len(finished) = %d, want 12", len(finished))
	}

	qty := func(v float64) *float64 { return &v }

	tests := []struct {
		name      string
		finished  []averagesales.TurnoverRow
		current   *averagesales.TurnoverRow
		n         int
		wantAvg   *float64 // nil — «продаж не было вообще»
		wantErr   error
		wantError bool
	}{
		{
			name:     "продаж не было вообще — nil",
			finished: nil,
			current:  nil,
			n:        12,
			wantAvg:  nil,
			wantErr:  ErrNoData,
		},
		{
			name:     "только один текущий незакрытый — avg = current/1",
			finished: nil,
			current:  &averagesales.TurnoverRow{PeriodStart: mkMonth(2026, 11, 0).PeriodStart, Qty: 5},
			n:        12,
			wantAvg:  qty(5),
		},
		{
			name:     "неполное окно без текущего — по имеющимся (3/3)",
			finished: finished[:3],
			current:  nil,
			n:        12,
			wantAvg:  qty((0 + 1 + 2) / 3),
		},
		{
			name:     "неполное окно с текущим — все завершённые + текущий (/4)",
			finished: finished[:3],
			current:  &averagesales.TurnoverRow{PeriodStart: mkMonth(2026, 11, 0).PeriodStart, Qty: 1},
			n:        12,
			wantAvg:  qty((0 + 1 + 2 + 1) / 4),
		},
		{
			name:     "полное окно без текущего — 12 завершённых (/12)",
			finished: finished,
			current:  nil,
			n:        12,
			wantAvg:  qty(66.0 / 12.0),
		},
		{
			name:     "полное окно, текущий больше самой дальней — 11 + текущий (/12)",
			finished: finished,
			current:  &averagesales.TurnoverRow{PeriodStart: mkMonth(2026, 11, 0).PeriodStart, Qty: 999},
			n:        12,
			// 11 последних (0+1+…+10=55) + 999 = 1054 / 12
			wantAvg: qty(1054.0 / 12.0),
		},
		{
			name:     "полное окно, текущий меньше дальней — 12 завершённых",
			finished: finished,
			current:  &averagesales.TurnoverRow{PeriodStart: mkMonth(2026, 11, 0).PeriodStart, Qty: 1},
			n:        12,
			wantAvg:  qty(66.0 / 12.0),
		},
		{
			name: "недельное окно 5, текущий больше дальней — 4 + текущий (/5)",
			finished: []averagesales.TurnoverRow{
				{PeriodStart: time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC), Qty: 4},
				{PeriodStart: time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC), Qty: 3},
				{PeriodStart: time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC), Qty: 2},
				{PeriodStart: time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC), Qty: 1},
				{PeriodStart: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC), Qty: 0},
			},
			current: &averagesales.TurnoverRow{PeriodStart: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC), Qty: 100},
			n:       5,
			wantAvg: qty((4 + 3 + 2 + 1 + 100) / 5.0),
		},
		{
			name: "отрицательные qty (возврат задним числом) — учитываются честно",
			finished: []averagesales.TurnoverRow{
				{PeriodStart: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Qty: 10},
				{PeriodStart: time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC), Qty: -3},
				{PeriodStart: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Qty: 5},
			},
			current: nil,
			n:       12,
			wantAvg: qty((10 - 3 + 5) / 3.0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := windowAvg(tt.finished, tt.current, tt.n)
			if tt.wantError {
				if err == nil {
					t.Fatal("windowAvg() error = nil, want error")
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("windowAvg() error = %v, want %v", err, tt.wantErr)
			}

			if tt.wantAvg == nil {
				if got != nil {
					t.Fatalf("windowAvg() = %v, want nil (нет данных)", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("windowAvg() = nil, want %v", *tt.wantAvg)
			}
			if *got != *tt.wantAvg {
				t.Errorf("windowAvg() = %v, want %v", *got, *tt.wantAvg)
			}
		})
	}
}

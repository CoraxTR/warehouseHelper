package discounts

import (
	"math"
	"testing"
)

func TestDailyRate(t *testing.T) {
	tests := []struct {
		name       string
		turnover   float64
		periodDays int
		want       float64
	}{
		{"недельный оборот 35 → 5 шт/день", 35, 7, 5},
		{"месячный оборот 300 → 10 шт/день", 300, 30, 10},
		{"месячный оборот 10 → 0,33 шт/день", 10, 30, 0.3333},
		{"оборота нет", 0, 7, 0},
		{"период не задан", 35, 0, 0},
		{"период отрицательный", 35, -7, 0},
		{"отрицательный оборот (возвраты задним числом)", -5, 7, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DailyRate(tc.turnover, tc.periodDays)
			if math.Abs(got-tc.want) > 1e-4 {
				t.Errorf("DailyRate(%v, %d) = %v, want %v", tc.turnover, tc.periodDays, got, tc.want)
			}
		})
	}
}

// Пример хлеба из плана: Г=7, накопленный остаток Q=40, недельный оборот 35
// (rate = 5), D=5 → коэф 1,6 — избыток есть.
func TestSurplusCoeff(t *testing.T) {
	tests := []struct {
		name        string
		cumQty      int64
		rate        float64
		daysLeft    int
		hasTurnover bool
		wantCoeff   float64
		wantOK      bool
	}{
		{"хлеб: Q=40, rate=5, D=5 → коэф 1,6", 40, 5, 5, true, 1.6, true},
		{"коэф ровно 1 — ещё не избыток", 25, 5, 5, true, 1, false},
		{"коэф меньше 1 — остаток распродаётся", 10, 5, 5, true, 0.4, false},
		{"хлеб с D=8: коэф 1,0", 40, 5, 8, true, 1, false},
		{"хлеб с D=4: коэф 2,0", 40, 5, 4, true, 2, true},
		{"нет оборота — избытка нет", 40, 5, 5, false, 0, false},
		{"rate=0 — избытка нет", 40, 0, 5, true, 0, false},
		{"D=0 — избытка нет", 40, 5, 0, true, 0, false},
		{"D<0 — избытка нет", 40, 5, -3, true, 0, false},
		{"нулевой остаток", 0, 5, 5, true, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coef, ok := SurplusCoeff(tc.cumQty, tc.rate, tc.daysLeft, tc.hasTurnover)
			if math.Abs(coef-tc.wantCoeff) > 1e-6 {
				t.Errorf("SurplusCoeff(%d, %v, %d, %v) = %v, want %v",
					tc.cumQty, tc.rate, tc.daysLeft, tc.hasTurnover, coef, tc.wantCoeff)
			}
			if ok != tc.wantOK {
				t.Errorf("SurplusCoeff(%d, %v, %d, %v) ok = %v, want %v",
					tc.cumQty, tc.rate, tc.daysLeft, tc.hasTurnover, ok, tc.wantOK)
			}
		})
	}
}

// Процент по избытку фиксирован решением владельца: всегда 10.
func TestSurplusPercent(t *testing.T) {
	if got := SurplusPercent(); got != 10 {
		t.Errorf("SurplusPercent() = %d, want 10", got)
	}
	if SurplusPercent() > autoCap {
		t.Errorf("процент по избытку %d выше потолка автомата %d", SurplusPercent(), autoCap)
	}
}

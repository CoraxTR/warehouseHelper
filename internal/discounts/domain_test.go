package discounts

import (
	"math"
	"testing"
)

// Таблица опорных точек §4 черновика: 14 дн → W 7,0 / 3 точки / старт 30 %,
// 45 дн → 13,9 / 5 / 10, 365 → 47,3, 730 → 71,0, 1095 → 90 (потолок).
// Старт — первая ступень лестницы: autoCap − 10×(N−1).
func TestWindow(t *testing.T) {
	tests := []struct {
		name      string
		shelfLife int16
		wantW     float64
		wantN     int
		wantStart int16
	}{
		{"срок не задан — окна нет", 0, 0, 0, 0},
		{"отрицательный срок — окна нет", -5, 0, 0, 0},
		{"7 дней — две точки", 7, 4.7, 2, 40},
		{"граница точек до 13 дн включительно", 13, 6.7, 2, 40},
		{"14 дней — три точки, старт 30", 14, 7.0, 3, 30},
		{"22 дня — ещё три точки", 22, 9.1, 3, 30},
		{"23 дня — уже четыре точки", 23, 9.4, 4, 20},
		{"33 дня — ещё четыре точки", 33, 11.6, 4, 20},
		{"34 дня — уже пять точек", 34, 11.8, 5, 10},
		{"45 дней — пять точек, старт 10", 45, 13.9, 5, 10},
		{"90 дней", 90, 20.8, 5, 10},
		{"180 дней", 180, 31.2, 5, 10},
		{"365 дней", 365, 47.3, 5, 10},
		{"730 дней", 730, 71.0, 5, 10},
		{"под порогом потолка: 1094 дня", 1094, 90.0, 5, 10},
		{"3 года — ровно потолок", 1095, 90, 5, 10},
		{"дальше потолок не растёт", 3000, 90, 5, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Window(tc.shelfLife)
			if math.Abs(got-tc.wantW) > 0.05 {
				t.Fatalf("Window(%d) = %.4f, want %.1f", tc.shelfLife, got, tc.wantW)
			}
			if tc.wantW == 0 {
				return
			}
			n := Points(got)
			if n != tc.wantN {
				t.Errorf("Points(%.4f) = %d, want %d", got, n, tc.wantN)
			}
			start := autoCap - 10*int16(n-1)
			if start != tc.wantStart {
				t.Errorf("старт при %d точках = %d, want %d", n, start, tc.wantStart)
			}
			step := Step(got, n)
			if step <= 0 || math.Abs(step*float64(n)-got) > 1e-9 {
				t.Errorf("Step(%.4f, %d) = %.4f — шаг не покрывает окно", got, n, step)
			}
		})
	}
}

// Границы числа точек заданы шкалой 2,33 дн на пересмотр и clamp'ом 1..5.
func TestPointsClamp(t *testing.T) {
	tests := []struct {
		name string
		w    float64
		want int
	}{
		{"нулевое окно — одна точка", 0, 1},
		{"меньше пересмотра — одна точка", 2.32, 1},
		{"один пересмотр", 2.33, 1},
		{"два пересмотра", 4.66, 2},
		{"опора 14 дн", 6.9955, 3},
		{"опора 45 дн", 13.8666, 5},
		{"верхний clamp", 90, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Points(tc.w); got != tc.want {
				t.Errorf("Points(%v) = %d, want %d", tc.w, got, tc.want)
			}
		})
	}
}

func TestStep(t *testing.T) {
	tests := []struct {
		name string
		w    float64
		n    int
		want float64
	}{
		{"опора 14 дн: 7,0 / 3 точки", 6.9955, 3, 2.3318},
		{"опора 7 дн: 2 точки", 4.6603, 2, 2.3301},
		{"без точек — шага нет", 7, 0, 0},
		{"без окна — шага нет", 0, 3, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Step(tc.w, tc.n)
			if math.Abs(got-tc.want) > 1e-3 {
				t.Errorf("Step(%v, %d) = %.4f, want %.4f", tc.w, tc.n, got, tc.want)
			}
		})
	}
}

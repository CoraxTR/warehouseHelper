package discounts

import (
	"math"
	"testing"
)

// Таблица опорных точек §4 черновика: 14 дн → W 7,0 / 3 точки / старт 30 %;
// 45 дн → 15,5 / 5 / 10 (разгон задел средние сроки), 90 дн → ровно 30 — опора
// владельца 21.09.2026; 365 → 47,4, 730 → 71,0, 1095 → 90 (потолок).
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
		{"32 дня — ещё четыре точки", 32, 11.6, 4, 20},
		{"33 дня — уже пять точек (граница сдвинулась разгоном)", 33, 11.9, 5, 10},
		{"34 дня", 34, 12.1, 5, 10},
		{"45 дней — разгон трогает средние сроки", 45, 15.5, 5, 10},
		{"89 дней — за долю дня до опоры, без излома", 89, 29.8, 5, 10},
		{"90 дней — опора разгона: ровно 30 дн", 90, 30.0, 5, 10},
		{"180 дней", 180, 34.9, 5, 10},
		{"365 дней", 365, 47.4, 5, 10},
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
			// Приведение int → int16 — продовым asInt16 (проверка границ, gosec G115).
			start := asInt16(autoCap - 10*(n-1))
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
		{"окно 13,87 дн — прежняя опора 45 дн", 13.8666, 5},
		{"окно опоры разгона 90 дн", 30, 5},
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

// Опора разгона (решение владельца 21.09.2026): 90-дневная позиция входит в скидку за
// 30 дн (прежняя кривая давала 20,81 — по КТ фактически 17–20). Формула одна, без
// ветвлений, поэтому проверяется то, что держит именно её форма: точная опора, отсутствие
// излома у опоры, неприкосновенность коротких сроков, монотонность и потолок на трёх годах.
func TestWindowRampAnchor(t *testing.T) {
	if got := Window(rampShelf); math.Abs(got-rampBase) > 1e-9 {
		t.Errorf("Window(%d) = %.6f, want ровно %d — опора разгона", rampShelf, got, rampBase)
	}
	if got := Window(rampShelf - 1); got < rampBase-0.5 {
		t.Errorf("Window(%d) = %.4f — излом у опоры: окно меньше опоры больше чем на полдня", rampShelf-1, got)
	}
	if got, base := Window(21), twinCoef*math.Pow(21, twinExp); math.Abs(got-base) > 0.05 {
		t.Errorf("Г=21: окно %.4f против прежнего %.4f — разгон задел короткие сроки", got, base)
	}
	prev := 0.0
	for g := int16(1); g <= 3000; g++ {
		got := Window(g)
		if got < prev {
			t.Fatalf("окно падает: Г=%d → %.4f после %.4f", g, got, prev)
		}
		if got > winMax {
			t.Fatalf("Г=%d: окно %.4f выше потолка %d", g, got, winMax)
		}
		prev = got
	}
	if got := Window(shelfCap); math.Abs(got-winMax) > 1e-9 {
		t.Errorf("Window(%d) = %.6f, want ровно потолок %d", shelfCap, got, winMax)
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

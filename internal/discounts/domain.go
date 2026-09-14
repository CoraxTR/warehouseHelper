// Пакет discounts — модуль расчёта скидок.
// Считает глубину скидки по сроку годности (лестница от остатка дней) и по
// избытку остатка относительно скорости продаж, сводит кандидатов по каналам
// и отдаёт строки для страницы «Скидки», ТГ-слота и дайджеста.
// Пакет — чистая логика без БД: входы формула получает через тип Input
// (заполняет репозиторий модуля), запись значений идёт через шов стока.
package discounts

import "math"

// Параметры лестницы по сроку годности (согласованы 14.09.2026,
// черновик §4: /root/notes/warehouseHelper-discounts-draft.md).
const (
	winCoef   = 1.49  // a = 7 / 14^0,586 — опора «14 дн → окно 7 дн»
	winExp    = 0.586 // k = ln(90/7) / ln(1095/14)
	winMax    = 90    // потолок окна, дн (три месяца)
	ktStep    = 2.33  // один пересмотр лестницы, дн
	maxPoints = 5     // максимум точек лестницы
	autoCap   = 50    // потолок автоматической скидки, % (выше — только вручную)
)

// Window — окно скидки в днях для срока годности (Г): min(1,49 × Г^0,586; 90).
// Срок не задан (shelfLife <= 0) → окна нет (0).
func Window(shelfLife int16) float64 {
	if shelfLife <= 0 {
		return 0
	}
	w := winCoef * math.Pow(float64(shelfLife), winExp)
	if w > winMax {
		return winMax
	}
	return w
}

// Points — число точек (ступеней) лестницы внутри окна: clamp(floor(W/2,33), 1, 5).
// Ровно 2,33 дн на один пересмотр, поэтому короткие сроки получают одну точку.
func Points(w float64) int {
	if w <= 0 {
		return 1
	}
	n := int(math.Floor(w/ktStep + 1e-9))
	if n < 1 {
		return 1
	}
	if n > maxPoints {
		return maxPoints
	}
	return n
}

// Step — шаг лестницы в днях: W / N. Без точек (n <= 0) шага нет (0).
func Step(w float64, n int) float64 {
	if w <= 0 || n <= 0 {
		return 0
	}
	return w / float64(n)
}

// inWindow — «остаток дней ещё внутри окна». Окно сравнивается с точностью до
// десятых дня — в этой же точности печатается таблица модели (§4 черновика),
// поэтому 14-дневная партия с D = 7 в окне, хотя W = 6,9954 < 7.
func inWindow(w float64, daysLeft int) bool {
	if w <= 0 {
		return false
	}
	return float64(daysLeft) <= math.Round(w*10)/10
}

// startPercent — первая (минимальная) ступень лестницы: 50 − 10×(N−1).
// N = 1 → 50, N = 3 → 30, N = 5 → 10.
func startPercent(n int) int16 {
	if n < 1 {
		n = 1
	}
	return int16(autoCap) - 10*int16(n-1)
}

// ExpiryPercent — скидка по сроку годности, %.
// Вне окна (D > W), по просроченной партии (D <= 0) и без заданного срока — 0.
// Внутри окна ступени идут от старта вверх по 10 % за точку:
// k = min(floor(D/шаг), N−1), d = max(старт, 50 − 10×k). Потолок автомата — 50.
func ExpiryPercent(shelfLife int16, daysLeft int) int16 {
	w := Window(shelfLife)
	if !inWindow(w, daysLeft) || daysLeft <= 0 {
		return 0
	}
	n := Points(w)
	step := Step(w, n)
	if step <= 0 {
		return startPercent(n)
	}
	k := int(math.Floor(float64(daysLeft)/step + 1e-9))
	if k > n-1 {
		k = n - 1
	}
	d := int16(autoCap) - 10*int16(k)
	if start := startPercent(n); d < start {
		d = start
	}
	return d
}

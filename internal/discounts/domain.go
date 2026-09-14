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

package usecase

import "time"

// currentPeriodStart — начало текущего незакрытого периода: 1-е число месяца
// или понедельник текущей недели.
func currentPeriodStart(interval string, now time.Time) time.Time {
	switch interval {
	case "week":
		return weekStart(now)
	default: // "month"
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	}
}

// periodEnd — последний момент периода: конец месяца (23:59:59 последнего дня)
// или воскресенья для недели.
func periodEnd(start time.Time, interval string) time.Time {
	switch interval {
	case "week":
		return start.AddDate(0, 0, 7).Add(-time.Nanosecond)
	default: // "month"
		return start.AddDate(0, 1, 0).Add(-time.Nanosecond)
	}
}

// weekStart — понедельник недели, содержащей now.
func weekStart(now time.Time) time.Time {
	// time.Weekday: Sunday=0 … Saturday=6; смещение до понедельника.
	offset := (int(now.Weekday()) + 6) % 7
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -offset)
}

// firstFullWeekStart — понедельник первой полной недели года: первый понедельник
// не раньше 1 января (неделя, пересекающая границу года, неполная — не берём).
func firstFullWeekStart(year int, loc *time.Location) time.Time {
	jan1 := time.Date(year, time.January, 1, 0, 0, 0, 0, loc)
	return jan1.AddDate(0, 0, (int(jan1.Weekday())+6)%7)
}

// previousPeriodStart — начало последнего ЗАВЕРШЁННОГО периода (текущий
// незакрытый исключён): для месяца — 1-е число прошлого месяца, для недели —
// понедельник прошлой недели. Именно с него идёт бэкфилл: окно средних = n
// последних завершённых + текущий, поэтому свежие завершённые периоды важнее
// глубокой истории (и набирают n not null быстрее — в свежих периодах почти
// всегда есть продажи).
func previousPeriodStart(interval string, now time.Time) time.Time {
	switch interval {
	case intervalWeek:
		return weekStart(now).AddDate(0, 0, -7)
	default: // intervalMonth
		return time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, now.Location())
	}
}

// periodBack — шаг на один период назад (месяц или неделя).
func periodBack(interval string, t time.Time) time.Time {
	switch interval {
	case intervalWeek:
		return t.AddDate(0, 0, -7)
	default: // intervalMonth
		return t.AddDate(0, -1, 0)
	}
}

// backfillFloor — нижняя граница бэкфилла: 1-е января 2014 (месяцы) или
// понедельник первой полной недели 2014 (недели). Магазин открылся в 2014,
// дальше вглубь не ходим (правило владельца).
func backfillFloor(interval string, loc *time.Location) time.Time {
	if interval == intervalWeek {
		return firstFullWeekStart(minBackfillYear, loc)
	}
	return time.Date(minBackfillYear, time.January, 1, 0, 0, 0, 0, loc)
}

// completedPeriodStarts — начала последних n ЗАВЕРШЁННЫХ периодов (без текущего
// незакрытого), от свежего к старому. Используется для селекции товаров с
// дырами в окне (см. ProductsMissingMonthlyTurnover/Weekly).
func completedPeriodStarts(interval string, n int, now time.Time) []time.Time {
	starts := make([]time.Time, 0, n)
	for p := previousPeriodStart(interval, now); len(starts) < n; p = periodBack(interval, p) {
		starts = append(starts, p)
	}
	return starts
}

// formatPeriodStarts — даты начал периодов строками YYYY-MM-DD для сравнения
// с DATE-колонками таблиц оборотов (без часовых поясов).
func formatPeriodStarts(starts []time.Time) []string {
	out := make([]string, len(starts))
	for i, s := range starts {
		out[i] = s.Format(time.DateOnly)
	}
	return out
}

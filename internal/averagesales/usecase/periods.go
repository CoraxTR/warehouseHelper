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

package ordercoeff

import "time"

// PeriodStart — начало периода, содержащего at: понедельник недели
// или 1-е число месяца.
func PeriodStart(pt PeriodType, at time.Time) time.Time {
	switch pt {
	case PeriodWeek:
		return weekStart(at)
	default: // PeriodMonth
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, at.Location())
	}
}

// PrevPeriodStart — начало периода, предшествующего start.
func PrevPeriodStart(pt PeriodType, start time.Time) time.Time {
	switch pt {
	case PeriodWeek:
		return start.AddDate(0, 0, -7)
	default: // PeriodMonth
		return start.AddDate(0, -1, 0)
	}
}

// weekStart — понедельник недели, содержащей now (как в averagesales).
func weekStart(now time.Time) time.Time {
	offset := (int(now.Weekday()) + 6) % 7
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -offset)
}

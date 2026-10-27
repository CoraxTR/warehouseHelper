package usecase

import (
	"testing"
	"time"
)

// Диапазон календаря: месяцы — YYYY-MM, недели — любой день (снап к понедельнику).

func TestParseRange_Month(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)

	r, err := parseRange(TabMonth, "2025-01", "2026-03", now)
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if got := r.from.Format(time.DateOnly); got != "2025-01-01" {
		t.Errorf("from = %s", got)
	}
	if got := r.to.Format(time.DateOnly); got != "2026-03-01" {
		t.Errorf("to = %s", got)
	}
}

func TestParseRange_WeekSnapsToMonday(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)

	r, err := parseRange(TabWeek, "2026-09-16", "2026-10-01", now)
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if got := r.from.Format(time.DateOnly); got != "2026-09-14" {
		t.Errorf("from = %s, want 2026-09-14 (понедельник)", got)
	}
	if got := r.to.Format(time.DateOnly); got != "2026-09-28" {
		t.Errorf("to = %s, want 2026-09-28 (понедельник)", got)
	}
}

func TestParseRange_EmptyFromUsesFloor(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)

	r, err := parseRange(TabMonth, "", "2026-03", now)
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if r.from.Year() != 2014 {
		t.Errorf("from = %s, want 2014", r.from.Format(time.DateOnly))
	}
}

func TestParseRange_ReversedIsError(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)

	if _, err := parseRange(TabMonth, "2026-05", "2026-01", now); err == nil {
		t.Fatal("перевёрнутый диапазон должен быть ошибкой")
	}
}

func TestPeriodsBetweenAndMerge(t *testing.T) {
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)

	got := periodsBetween(TabMonth, from, to)
	if len(got) != 3 {
		t.Fatalf("месяцев = %d, want 3", len(got))
	}

	window := []time.Time{
		time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC),
	}
	merged := mergePeriods(window, got)
	if len(merged) != 5 {
		t.Fatalf("периодов = %d, want 5", len(merged))
	}
	for i := 1; i < len(merged); i++ {
		if !merged[i-1].Before(merged[i]) {
			t.Fatalf("порядок нарушен: %s >= %s", merged[i-1], merged[i])
		}
	}

	again := mergePeriods(merged, got)
	if len(again) != 5 {
		t.Fatalf("повторный merge дал %d, want 5", len(again))
	}
}

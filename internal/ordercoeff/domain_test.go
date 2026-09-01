package ordercoeff

import (
	"testing"
	"time"
)

// series — мини-«календарь» для тестов: воспроизводит логику хранилища
// (найти строку текущего и предыдущего периода, применить событие, сохранить)
// без SQL. Понедельники: 2026-08-31, 2026-09-07, 2026-09-14 (01.09.2026 — вторник).
type series struct {
	pt   PeriodType
	rows map[time.Time]*PeriodCoeff
}

func newSeries(pt PeriodType) *series {
	return &series{pt: pt, rows: map[time.Time]*PeriodCoeff{}}
}

// apply применяет событие в момент at; возвращает applied.
func (s *series) apply(ev EventType, at time.Time) bool {
	start := PeriodStart(s.pt, at)
	prevStart := PrevPeriodStart(s.pt, start)
	cur := s.rows[start]
	prev := s.rows[prevStart]

	newCur, newPrev, applied := ApplyEvent(cur, prev, ev)
	if !applied {
		return false
	}
	if newCur != nil {
		newCur.ProductID = "p1"
		newCur.PeriodType = s.pt
		newCur.PeriodStart = start
		s.rows[start] = newCur
	}
	if newPrev != nil {
		s.rows[newPrev.PeriodStart] = newPrev
	}
	return true
}

func (s *series) row(start time.Time) *PeriodCoeff { return s.rows[start] }

// rowAt — строка периода, содержащего момент at (начало периода в ключе).
func (s *series) rowAt(at time.Time) *PeriodCoeff { return s.rows[PeriodStart(s.pt, at)] }

func week(t *testing.T, y int, m time.Month, d int) time.Time {
	t.Helper()
	return time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
}

// w1, w2, w3 — понедельники подряд (недельный период).
var (
	w1 = time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	w2 = time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	w3 = time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
)

func TestApplyEventTimelines(t *testing.T) {
	t.Run("перенос и обнуление предыдущего (2.2)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		if !s.apply(EventSoldOut, w1) {
			t.Fatal("w1: sold out не применился")
		}
		if !s.apply(EventSoldOut, w2) {
			t.Fatal("w2: sold out не применился")
		}

		if got := s.rowAt(w2).Coeff; got != 2 {
			t.Errorf("w2.coeff = %d, want 2 (1 с прошлой недели + 1)", got)
		}
		if got := s.rowAt(w1).Coeff; got != 0 {
			t.Errorf("w1.coeff = %d, want 0 (обнулён переносом)", got)
		}
		if got := s.rowAt(w1).SoldOut; got != 1 {
			t.Errorf("w1.sold_out = %d, want 1 (счётчик-факт сохраняется)", got)
		}
	})

	t.Run("возврат в событийной неделе откатывает +1 ЭТОЙ недели (таймлайн 1)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		s.apply(EventSoldOut, w1)
		s.apply(EventSoldOut, w2) // цепочка: w2 = 2, w1 = 0

		if applied := s.apply(EventRollbackSoldOut, w2); !applied {
			t.Fatal("возврат не применился (в w2 есть живой +1)")
		}
		if got := s.rowAt(w2).Coeff; got != 1 {
			t.Errorf("w2.coeff = %d, want 1 (2 − откат 1)", got)
		}
		if got := s.rowAt(w2).SoldOut; got != 0 {
			t.Errorf("w2.sold_out = %d, want 0 (живой счётчик снят)", got)
		}
		if got := s.rowAt(w1).Coeff; got != 0 {
			t.Errorf("w1.coeff = %d, want 0 (уже обнулён переносом)", got)
		}
	})

	t.Run("возврат в чистой неделе — no-op (таймлайн 2)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		s.apply(EventSoldOut, w1) // w1 = 1, w2 чистая

		if applied := s.apply(EventRollbackSoldOut, w2); applied {
			t.Fatal("возврат применился, хотя в w2 нет живого +1")
		}
		if s.rowAt(w2) != nil {
			t.Error("w2 получила строку — должна остаться чистой")
		}
		if got := s.rowAt(w1).Coeff; got != 1 {
			t.Errorf("w1.coeff = %d, want 1 (не тронут)", got)
		}
	})

	t.Run("взаимообнуление скидки и закончился — цепочка жива (2.1)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		if !s.apply(EventDiscount, w1) {
			t.Fatal("скидка не применилась")
		}
		if !s.apply(EventSoldOut, w1) {
			t.Fatal("sold out не применился")
		}
		if got := s.rowAt(w1).Coeff; got != 0 {
			t.Errorf("w1.coeff = %d, want 0 (−1 и +1 взаимообнулились)", got)
		}
		// Неделя событийная: следующая неделя переносит 0 и накапливает своё.
		if !s.apply(EventSoldOut, w2) {
			t.Fatal("w2: sold out не применился")
		}
		if got := s.rowAt(w2).Coeff; got != 1 {
			t.Errorf("w2.coeff = %d, want 1 (перенос 0 + 1)", got)
		}
	})

	t.Run("недоступен сохраняет значение и передаёт дальше (2.3)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		s.apply(EventSoldOut, w1) // w1 = 1
		if !s.apply(EventUnavailable, w2) {
			t.Fatal("недоступен не применился")
		}
		if got := s.rowAt(w2).Coeff; got != 1 {
			t.Errorf("w2.coeff = %d, want 1 (значение w1 сохранено)", got)
		}
		if got := s.rowAt(w2).Unavailable; got != 1 {
			t.Errorf("w2.unavailable = %d, want 1", got)
		}
		if got := s.rowAt(w1).Coeff; got != 0 {
			t.Errorf("w1.coeff = %d, want 0 (передано дальше)", got)
		}
	})

	t.Run("возврат скидки снимает −1, закончился остаётся (2.4)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		s.apply(EventDiscount, w1)        // −1
		s.apply(EventSoldOut, w1)         // +1 → 0
		s.apply(EventRollbackSoldOut, w1) // откат +1 → −1

		if got := s.rowAt(w1).Coeff; got != -1 {
			t.Errorf("w1.coeff = %d, want −1 (скидка осталась, sold out откатан)", got)
		}
		if got := s.rowAt(w1).Discount; got != 1 {
			t.Errorf("w1.discount = %d, want 1", got)
		}
		if got := s.rowAt(w1).SoldOut; got != 0 {
			t.Errorf("w1.sold_out = %d, want 0", got)
		}
	})

	t.Run("повторные события накладываются повторно (+2)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		s.apply(EventSoldOut, w1)
		s.apply(EventSoldOut, w1)
		if got := s.rowAt(w1).Coeff; got != 2 {
			t.Errorf("w1.coeff = %d, want 2 (дважды закончился)", got)
		}
		if got := s.rowAt(w1).SoldOut; got != 2 {
			t.Errorf("w1.sold_out = %d, want 2", got)
		}
	})

	t.Run("чистая неделя рвёт цепочку — старт с нуля (разрыв)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		s.apply(EventSoldOut, w1) // w1 = 1
		s.apply(EventSoldOut, w3) // w2 чистая (строки нет) → w3 стартует с 0

		if got := s.rowAt(w3).Coeff; got != 1 {
			t.Errorf("w3.coeff = %d, want 1 (без переноса через w2)", got)
		}
		if got := s.rowAt(w1).Coeff; got != 1 {
			t.Errorf("w1.coeff = %d, want 1 (значение осталось на w1)", got)
		}
	})

	t.Run("возврат не снимает перенесённое значение (чужой +1)", func(t *testing.T) {
		s := newSeries(PeriodWeek)
		s.apply(EventSoldOut, w1)     // w1 = 1
		s.apply(EventUnavailable, w2) // w2 = 1 (перенесено), w2.sold_out = 0
		if applied := s.apply(EventRollbackSoldOut, w2); applied {
			t.Fatal("возврат применился: в w2 нет СВОЕГО +1, только перенесённый")
		}
		if got := s.rowAt(w2).Coeff; got != 1 {
			t.Errorf("w2.coeff = %d, want 1 (перенесённое не откатывается)", got)
		}
	})
}

func TestApplyEventFrozen(t *testing.T) {
	s := newSeries(PeriodWeek)
	if !s.apply(EventFrozen, w1) {
		t.Fatal("заморозка не применилась")
	}
	if got := s.rowAt(w1).Coeff; got != -2 {
		t.Errorf("w1.coeff = %d, want −2", got)
	}
	if got := s.rowAt(w1).Frozen; got != 1 {
		t.Errorf("w1.frozen = %d, want 1", got)
	}
}

func TestApplyEventMonth(t *testing.T) {
	m1 := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC) // сентябрь
	m2 := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)   // октябрь

	s := newSeries(PeriodMonth)
	if start := PeriodStart(PeriodMonth, m1); start.Day() != 1 || start.Month() != time.September {
		t.Fatalf("PeriodStart месяца = %v, want 2026-09-01", start)
	}
	s.apply(EventSoldOut, m1) // сентябрь = 1
	s.apply(EventSoldOut, m2) // октябрь = 2 (перенос), сентябрь = 0

	if got := s.row(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)).Coeff; got != 0 {
		t.Errorf("сентябрь.coeff = %d, want 0", got)
	}
	oct := s.row(time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC))
	if oct == nil || oct.Coeff != 2 {
		t.Errorf("октябрь.coeff = %v, want 2", oct)
	}
}

func TestPeriodStartWeek(t *testing.T) {
	// 01.09.2026 — вторник; понедельник недели = 31.08.2026.
	got := PeriodStart(PeriodWeek, week(t, 2026, time.September, 1))
	want := time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("PeriodStart(week, 01.09) = %v, want %v", got, want)
	}

	// Понедельник — сам себе начало (по дате).
	gotMonday := PeriodStart(PeriodWeek, w1)
	if gotMonday.Day() != w1.Day() || gotMonday.Month() != w1.Month() || gotMonday.Year() != w1.Year() {
		t.Errorf("PeriodStart(week, понедельник) = %v, want тот же день", gotMonday)
	}
}

package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"warehouseHelper/internal/averagesales"
)

// calendarRange — выбранный промежуток: от и до включительно, уже приведённые
// к началу периода (понедельник недели / первое число месяца).
type calendarRange struct {
	from time.Time
	to   time.Time
}

// parseRange читает «с»/«по» из формы. Пустые значения — диапазона нет.
func parseRange(tab, from, to string, now time.Time) (*calendarRange, error) {
	if from == "" && to == "" {
		return nil, errNoRange
	}
	layout := "2006-01"
	if tab == TabWeek {
		layout = time.DateOnly
	}
	parse := func(v string) (time.Time, error) {
		if v == "" {
			return time.Time{}, nil
		}
		return time.ParseInLocation(layout, v, now.Location())
	}
	start, err := parse(from)
	if err != nil {
		return nil, fmt.Errorf("начало диапазона: %w", err)
	}
	end, err := parse(to)
	if err != nil {
		return nil, fmt.Errorf("конец диапазона: %w", err)
	}
	if start.IsZero() {
		start = calendarFloor(tab, now)
	}
	if end.IsZero() {
		end = now
	}
	start, end = normalizePeriod(tab, start), normalizePeriod(tab, end)
	if end.Before(start) {
		return nil, errors.New("конец диапазона раньше начала")
	}
	return &calendarRange{from: start, to: end}, nil
}

// calendarFloor — нижняя граница истории: 2014 год (открытие магазина).
func calendarFloor(tab string, now time.Time) time.Time {
	loc := now.Location()
	if tab == TabWeek {
		return time.Date(2014, time.January, 6, 0, 0, 0, 0, loc)
	}
	return time.Date(2014, time.January, 1, 0, 0, 0, 0, loc)
}

// normalizePeriod приводит дату к началу недели (пн) или месяца.
func normalizePeriod(tab string, t time.Time) time.Time {
	if tab != TabWeek {
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	}
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	shift := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -shift)
}

// periodsBetween — периоды диапазона от..до включительно (не больше calendarMaxCells).
func periodsBetween(tab string, from, to time.Time) []time.Time {
	out := make([]time.Time, 0, 24)
	step := func(t time.Time) time.Time {
		if tab == TabWeek {
			return t.AddDate(0, 0, 7)
		}
		return t.AddDate(0, 1, 0)
	}
	for t := from; !t.After(to) && len(out) < calendarMaxCells; t = step(t) {
		out = append(out, t)
	}
	return out
}

// mergePeriods — объединение периодов окна и диапазона без повторов, по возрастанию.
func mergePeriods(base, extra []time.Time) []time.Time {
	seen := make(map[time.Time]struct{}, len(base)+len(extra))
	out := make([]time.Time, 0, len(base)+len(extra))
	for _, list := range [][]time.Time{base, extra} {
		for _, p := range list {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

// extendRange дотягивает продажи за выбранный диапазон: окно отдаёт только свои
// периоды, история берётся чтением оборотов (в МС не ходим).
func (uc *GoodsUseCase) extendRange(ctx context.Context, tab, from, to string, ids []string, data *calendarData) (*calendarData, error) {
	r, err := parseRange(tab, from, to, uc.now())
	if errors.Is(err, errNoRange) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	extra := periodsBetween(tab, r.from, r.to)
	if len(extra) == 0 {
		return data, nil
	}
	data.periods = mergePeriods(data.periods, extra)
	return data, uc.mergeTurnover(ctx, tab, ids, data)
}

// mergeTurnover подмешивает продажи диапазона к окну: значения окна
// приоритетнее, потому что несут пометку участия в среднем.
func (uc *GoodsUseCase) mergeTurnover(ctx context.Context, tab string, ids []string, data *calendarData) error {
	var rows []averagesales.TurnoverRow
	var err error
	if tab == TabWeek {
		rows, err = uc.salesCalendar.WeeklyTurnover(ctx, ids, data.periods[0])
	} else {
		rows, err = uc.salesCalendar.MonthlyTurnover(ctx, ids, data.periods[0])
	}
	if err != nil {
		return fmt.Errorf("продажи диапазона: %w", err)
	}
	if data.rowsByID == nil {
		data.rowsByID = make(map[string][]averagesales.PeriodSales, len(ids))
	}
	for id, list := range periodSalesByProduct(rows) {
		cur := data.rowsByID[id]
		have := make(map[time.Time]struct{}, len(cur))
		for _, ps := range cur {
			have[ps.PeriodStart] = struct{}{}
		}
		for _, ps := range list {
			if _, ok := have[ps.PeriodStart]; ok {
				continue
			}
			cur = append(cur, ps)
		}
		data.rowsByID[id] = cur
	}
	return nil
}

// errNoRange — «с»/«по» не заполнены: показываем действующее окно.
var errNoRange = errors.New("диапазон не задан")

// calendarMaxCells — предел столбцов на страницу (защита от «2014..сегодня» разом).
const calendarMaxCells = 200

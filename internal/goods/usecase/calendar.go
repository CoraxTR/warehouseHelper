package usecase

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/ordercoeff"
)

// Календарь продаж — страница каталога: строки товаров, столбцы периоды
// (неделя/месяц), в ячейке кг и штуки, подсказка с накопленным коэффициентом
// коррекции, красная обводка периодов, участвующих в расчёте среднего.
const (
	// TabWeek — понедельная вкладка (только товары с недельным учётом).
	TabWeek = "week"
	// TabMonth — помесячная вкладка (все товары, листание по месяцам).
	TabMonth = "month"

	// calendarWeeks/calendarMonths — столбцы по умолчанию, когда активных
	// ячеек окна нет вовсе (данных по товарам нет).
	calendarWeeks  = 6
	calendarMonths = 13

	// calendarLookbackWeeks/Months — насколько глубоко ищем активные ячейки:
	// недоступные периоды окно не занимают, поэтому оно может уехать в прошлое
	// дальше обычного (запас — три окна).
	calendarLookbackWeeks  = 26
	calendarLookbackMonths = 36
)

// SalesCalendarReader — данные продаж для календаря (реализует
// *aucase.UseCase, владелец оборотов).
type SalesCalendarReader interface {
	// Calendar — окно товаров: действующее среднее и периоды с пометкой
	// участия. active — периоды доступности (шов AvailabilityReader):
	// доступный период без продаж — ячейка 0, она же участвует в среднем.
	Calendar(ctx context.Context, productIDs []string, active map[string][]time.Time) (map[string]averagesales.SalesCalendar, error)
	// MonthlyTurnover — исторический диапазон месяцев (листание).
	MonthlyTurnover(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error)
	// WeeklyTurnover — исторический диапазон недель.
	WeeklyTurnover(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error)
}

// PeriodCoeffReader — коэффициенты коррекции по периодам (реализует
// *oucase.UseCase, владелец product_period_coeff).
type PeriodCoeffReader interface {
	PeriodsBatch(ctx context.Context, productIDs []string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[string]map[time.Time]ordercoeff.PeriodCoeff, error)
}

// AvailabilityReader — периоды доступности товаров (реализует
// *ducecase.UseCase, владелец product_day_state). Календарю нужен признак
// «ячейка подходит для расчёта»: товар был доступен хоть один день периода, а
// продаж не было — это 0, а не прочерк.
type AvailabilityReader interface {
	AvailablePeriods(ctx context.Context, interval string, from, to time.Time) (map[string][]time.Time, error)
}

// CalendarCell — одна ячейка календаря (товар × период).
type CalendarCell struct {
	PeriodStart time.Time
	KgText      string // кг с тремя знаками (весовые); пусто — штучный товар
	QtyText     string // штуки за период; пусто — данных нет
	Zero        bool   // ноль: был доступен, продаж не было (ячейка расчёта)
	InWindow    bool   // период участвует в расчёте среднего (красная обводка)
	Coeff       string // накопленный коэффициент периода; пусто — нет событий
	Delta       string // вклад событий этого периода: +1/−2; пусто — ноль
	Title       string // подсказка ячейки: период, продажи, коэффициент
	Class       string // цвет события: cal-disc / cal-out / cal-both (пусто — событий нет)
}

// CalendarRow — строка календаря: товар и его ячейки по столбцам.
type CalendarRow struct {
	ProductID    string
	Name         string
	InternalCode string
	Cells        []CalendarCell
	AvgText      string // действующее среднее, шт/период
	CoeffText    string // коэффициент текущего периода
	Weekly       bool   // недельный учёт: месячного ряда и среднего нет
}

// CalendarGroup — группа товаров (разделение как на «Сроках»).
type CalendarGroup struct {
	Name string
	Rows []CalendarRow
}

// CalendarPage — данные страницы «Календарь продаж».
type CalendarPage struct {
	Tab          string
	WeekTabURL   string
	MonthTabURL  string
	PeriodTitles []string
	Groups       []CalendarGroup
	MonthParam   string // выбранный месяц (YYYY-MM), только для месячной вкладки
	FromParam    string // «с» из формы диапазона (YYYY-MM или YYYY-MM-DD)
	ToParam      string // «по» из формы диапазона
	PrevMonthURL string
	NextMonthURL string
	NextDisabled bool
	AvgHeader    string // подпись колонки средних («средние, шт/нед»)
	Empty        bool
	Error        string
}

// CalendarPage собирает страницу календаря. tab — week|month (пусто = week),
// month — YYYY-MM для месячной вкладки (пусто = текущий месяц).
func (uc *GoodsUseCase) CalendarPage(ctx context.Context, tab, month, from, to string) (*CalendarPage, error) {
	done := metrics.Track(trackPkg, "CalendarPage")
	defer done()

	if uc.salesCalendar == nil || uc.periodCoeffs == nil {
		return nil, errors.New("календарь продаж не подключён")
	}

	if tab != TabMonth {
		tab = TabWeek
	}

	products, err := uc.repo.LoadAllProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("товары каталога: %w", err)
	}

	page := &CalendarPage{
		Tab:         tab,
		WeekTabURL:  "/goods/calendar?tab=" + TabWeek,
		MonthTabURL: "/goods/calendar?tab=" + TabMonth,
		AvgHeader:   avgHeader(tab),
	}

	visible := productsForTab(products, tab)
	if len(visible) == 0 {
		page.Empty = true
		return page, nil
	}

	ids := make([]string, len(visible))
	for i, v := range visible {
		ids[i] = v.ID
	}

	page.FromParam, page.ToParam = from, to

	available, err := uc.availablePeriods(ctx, tab, from, to, month)
	if err != nil {
		return nil, err
	}

	data, err := uc.calendarData(ctx, tab, month, ids, page, available)
	if err != nil {
		return nil, err
	}

	data, err = uc.extendRange(ctx, tab, from, to, ids, data)
	if err != nil {
		page.Error = err.Error()
		return page, nil
	}

	for _, p := range data.periods {
		page.PeriodTitles = append(page.PeriodTitles, periodTitle(p, tab))
	}

	coeffs, err := uc.periodCoeffs.PeriodsBatch(ctx, ids, coeffPeriodType(tab), data.periods)
	if err != nil {
		return nil, fmt.Errorf("коэффициенты периодов: %w", err)
	}

	page.Groups = buildCalendarGroups(visible, data, tab, coeffs, activeSets(available))

	return page, nil
}

// calendarData — столбцы и продажи по товарам для выбранной вкладки.
type calendarData struct {
	periods  []time.Time
	rowsByID map[string][]averagesales.PeriodSales
	avg      map[string]float64
}

// calendarData собирает данные вкладки: недели — окно владельца оборотов
// (со средними и пометкой окна), месяцы — окно, заканчивающееся выбранным
// месяцем; для действующего месяца средние и пометки тоже приходят от
// владельца, для истории — только продажи.
func (uc *GoodsUseCase) calendarData(ctx context.Context, tab, month string, ids []string, page *CalendarPage, available map[string][]time.Time) (*calendarData, error) {
	if tab == TabWeek {
		return uc.weekData(ctx, ids, available)
	}

	return uc.monthData(ctx, month, ids, page, available)
}

// weekData — недельная вкладка: столбцы считаются от текущей недели, продажи —
// из окна averagesales (там же пометка участия в среднем).
func (uc *GoodsUseCase) weekData(ctx context.Context, ids []string, available map[string][]time.Time) (*calendarData, error) {
	window, err := uc.salesCalendar.Calendar(ctx, ids, available)
	if err != nil {
		return nil, fmt.Errorf("продажи окна: %w", err)
	}

	data := &calendarData{
		periods:  weekPeriods(uc.now(), calendarWeeks),
		rowsByID: make(map[string][]averagesales.PeriodSales, len(window)),
		avg:      make(map[string]float64, len(window)),
	}
	for id, cal := range window {
		data.rowsByID[id] = cal.Periods
		data.avg[id] = cal.Avg
	}
	// Столбцы — от самой дальней участвующей ячейки: иначе она за краем окна.
	// Уже стандартного окна страница не становится.
	if oldest := oldestInWindow(data.rowsByID); !oldest.IsZero() && oldest.Before(data.periods[0]) {
		data.periods = periodsFrom(TabWeek, oldest, uc.now())
	}

	return data, nil
}

// monthData — месячная вкладка: окно заканчивается выбранным месяцем.
func (uc *GoodsUseCase) monthData(ctx context.Context, month string, ids []string, page *CalendarPage, available map[string][]time.Time) (*calendarData, error) {
	now := uc.now()
	start := monthStart(month, now)

	// Листание: назад — история, вперёд — обратно к действующему окну.
	rangeStart := start.AddDate(0, -(calendarMonths - 1), 0)
	page.MonthParam = start.Format("2006-01")
	page.PrevMonthURL = monthURL(start.AddDate(0, -1, 0))
	next := start.AddDate(0, 1, 0)
	page.NextMonthURL = monthURL(next)
	page.NextDisabled = !next.Before(currentMonthStart(now))

	data := &calendarData{periods: monthPeriods(rangeStart, calendarMonths)}

	if start.Equal(currentMonthStart(now)) {
		return uc.currentMonthData(ctx, ids, data, available)
	}

	turnover, err := uc.salesCalendar.MonthlyTurnover(ctx, ids, data.periods[0])
	if err != nil {
		return nil, fmt.Errorf("продажи по месяцам: %w", err)
	}
	data.rowsByID = periodSalesByProduct(turnover)

	return data, nil
}

// currentMonthData — действующий диапазон: средние и обводку окна отдаёт
// владелец оборотов (Calendar считает окно сам).
func (uc *GoodsUseCase) currentMonthData(ctx context.Context, ids []string, data *calendarData, available map[string][]time.Time) (*calendarData, error) {
	window, err := uc.salesCalendar.Calendar(ctx, ids, available)
	if err != nil {
		return nil, fmt.Errorf("продажи окна: %w", err)
	}

	data.rowsByID = make(map[string][]averagesales.PeriodSales, len(window))
	data.avg = make(map[string]float64, len(window))
	for id, cal := range window {
		data.rowsByID[id] = cal.Periods
		data.avg[id] = cal.Avg
	}
	if oldest := oldestInWindow(data.rowsByID); !oldest.IsZero() && oldest.Before(data.periods[0]) {
		data.periods = periodsFrom(TabMonth, oldest, uc.now())
	}

	return data, nil
}

// buildCalendarGroups — строки и группы страницы.
func buildCalendarGroups(visible []productInfo, data *calendarData, tab string, stats map[string]map[time.Time]ordercoeff.PeriodCoeff, active map[string]map[time.Time]struct{}) []CalendarGroup {
	groups := make([]CalendarGroup, 0, 8)
	groupIndex := make(map[string]int, 8)
	currentStart := data.periods[len(data.periods)-1]

	for _, v := range visible {
		var avg *float64
		if a, ok := data.avg[v.ID]; ok {
			avg = &a
		}
		row := buildCalendarRow(v, data.rowsByID[v.ID], avg, stats[v.ID], data.periods, tab, currentStart, active[v.ID])

		idx, ok := groupIndex[v.Group]
		if !ok {
			idx = len(groups)
			groupIndex[v.Group] = idx
			groups = append(groups, CalendarGroup{Name: v.Group})
		}
		groups[idx].Rows = append(groups[idx].Rows, row)
	}

	return groups
}

// buildCalendarRow — одна строка: ячейки по столбцам, среднее, коэффициент.
func buildCalendarRow(v productInfo, sales []averagesales.PeriodSales, avg *float64, stats map[time.Time]ordercoeff.PeriodCoeff, periods []time.Time, tab string, currentStart time.Time, active map[time.Time]struct{}) CalendarRow {
	row := CalendarRow{
		ProductID:    v.ID,
		Name:         v.Name,
		InternalCode: v.Code,
		Weekly:       v.Weekly,
		Cells:        make([]CalendarCell, 0, len(periods)),
	}

	byPeriod := make(map[time.Time]averagesales.PeriodSales, len(sales))
	for _, ps := range sales {
		byPeriod[ps.PeriodStart] = ps
	}

	for _, p := range periods {
		cell := CalendarCell{PeriodStart: p}
		if ps, ok := byPeriod[p]; ok {
			cell.QtyText = formatQty(ps.Qty)
			cell.KgText = formatKg(ps.Kg)
			cell.InWindow = ps.InWindow
		} else if _, ok := active[p]; ok {
			// Товар был доступен, но продаж не было: 0 — подходящая для
			// расчёта ячейка, в отличие от прочерка.
			cell.QtyText = "0"
			cell.Zero = true
		}
		if st, ok := stats[p]; ok {
			if st.Coeff != 0 {
				cell.Coeff = formatCoeff(st.Coeff)
			}
			if d := st.Delta(); d != 0 {
				cell.Delta = formatCoeff(d)
			}
			cell.Class = eventClass(st)
		}
		cell.Title = cellTitle(p, tab, cell)
		if st, ok := stats[p]; ok {
			if extra := eventCounts(st); extra != "" {
				cell.Title += "; " + extra
			}
		}
		row.Cells = append(row.Cells, cell)
	}

	if avg != nil {
		row.AvgText = formatQty(*avg)
	}
	if st, ok := stats[currentStart]; ok && st.Coeff != 0 {
		row.CoeffText = formatCoeff(st.Coeff)
	}

	return row
}

// eventClass — цвет ячейки по фактам периода: скидки (жёлтый), «закончился»
// (красный), оба события сразу — жёлтый фон с красным шрифтом.
func eventClass(st ordercoeff.PeriodCoeff) string {
	disc := st.Discount > 0
	out := st.SoldOut > 0
	switch {
	case disc && out:
		return "cal-both"
	case disc:
		return "cal-disc"
	case out:
		return "cal-out"
	default:
		return ""
	}
}

// eventCounts — счётчики событий периода для подсказки: сколько раз повторилось.
func eventCounts(st ordercoeff.PeriodCoeff) string {
	parts := make([]string, 0, 4)
	if st.Discount > 0 {
		parts = append(parts, fmt.Sprintf("скидки %d", st.Discount))
	}
	if st.SoldOut > 0 {
		parts = append(parts, fmt.Sprintf("закончился %d", st.SoldOut))
	}
	if st.Frozen > 0 {
		parts = append(parts, fmt.Sprintf("заморозка %d", st.Frozen))
	}
	if st.Unavailable > 0 {
		parts = append(parts, fmt.Sprintf("недоступен %d", st.Unavailable))
	}
	return strings.Join(parts, ", ")
}

// productsForTab — товары вкладки: недельная — только с недельным учётом.
func productsForTab(products []domain.Product, tab string) []productInfo {
	out := make([]productInfo, 0, len(products))
	for _, p := range products {
		if tab == TabWeek && !p.TrackWeekly {
			continue
		}
		out = append(out, productInfo{ID: p.ID, Name: p.Name, Code: p.InternalCode, Group: p.GroupName, Weekly: p.TrackWeekly})
	}

	return out
}

// avgHeader — подпись колонки средних по вкладке.
func avgHeader(tab string) string {
	if tab == TabMonth {
		return "средние, шт/мес"
	}

	return "средние, шт/нед"
}

// coeffPeriodType — гранулярность коэффициентов по вкладке.
func coeffPeriodType(tab string) ordercoeff.PeriodType {
	if tab == TabMonth {
		return ordercoeff.PeriodMonth
	}

	return ordercoeff.PeriodWeek
}

// monthURL — адрес месячной вкладки для месяца.
func monthURL(m time.Time) string {
	return "/goods/calendar?tab=" + TabMonth + "&month=" + m.Format("2006-01")
}

// productInfo — срез каталога для сборки строк календаря.
type productInfo struct {
	ID     string
	Name   string
	Code   string
	Group  string
	Weekly bool // недельный учёт (track_weekly): месячного расчёта по нему нет
}

// periodSalesByProduct раскладывает строки оборотов по товарам.
func periodSalesByProduct(rows []averagesales.TurnoverRow) map[string][]averagesales.PeriodSales {
	out := make(map[string][]averagesales.PeriodSales, len(rows))
	for _, r := range rows {
		out[r.ProductID] = append(out[r.ProductID], averagesales.PeriodSales{
			PeriodStart: r.PeriodStart,
			Qty:         r.Qty,
			Kg:          r.Kg,
		})
	}

	return out
}

// weekPeriods — столбцы недель: n недель, заканчивая текущей (по возрастанию).
func weekPeriods(now time.Time, n int) []time.Time {
	start := currentWeekStart(now)
	out := make([]time.Time, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, start.AddDate(0, 0, -7*i))
	}

	return out
}

// monthPeriods — столбцы месяцев: n месяцев от start (по возрастанию).
func monthPeriods(start time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for i := range n {
		out = append(out, start.AddDate(0, i, 0))
	}

	return out
}

// monthStart — месяц, которым заканчивается окно: выбранный или текущий.
// Кривой/будущий параметр не ломает страницу — показываем текущий диапазон.
func monthStart(month string, now time.Time) time.Time {
	cur := currentMonthStart(now)
	month = strings.TrimSpace(month)
	if month == "" {
		return cur
	}

	parsed, err := time.Parse("2006-01", month)
	if err != nil || parsed.After(cur) {
		return cur
	}

	return parsed
}

// currentWeekStart — понедельник недели, в которую попадает момент.
func currentWeekStart(now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	offset := (int(day.Weekday()) + 6) % 7 // Пн = 0

	return day.AddDate(0, 0, -offset)
}

// currentMonthStart — первое число текущего месяца.
func currentMonthStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// periodTitle — подпись столбца: dd.MM для недель, MM.YY для месяцев.
func periodTitle(p time.Time, tab string) string {
	if tab == TabWeek {
		return p.Format("02.01")
	}

	return p.Format("01.06")
}

// formatQty — штуки: до трёх знаков, лишние нули убраны, запятая.
func formatQty(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)

	return trimZeros(strings.ReplaceAll(s, ".", ","))
}

// formatKg — кг с тремя знаками (nil = штучный товар, пустая строка).
func formatKg(kg *float64) string {
	if kg == nil {
		return ""
	}

	return strings.ReplaceAll(strconv.FormatFloat(*kg, 'f', 3, 64), ".", ",")
}

// formatCoeff — коэффициент со знаком («+1», «−2»).
func formatCoeff(c int16) string {
	if c > 0 {
		return "+" + strconv.FormatInt(int64(c), 10)
	}

	return strconv.FormatInt(int64(c), 10)
}

// trimZeros убирает хвостовые нули дробной части («8,500» → «8,5», «8,000» → «8»).
func trimZeros(s string) string {
	if !strings.Contains(s, ",") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ",")
	if s == "-0" || s == "" {
		return "0"
	}

	return s
}

// cellTitle — подсказка ячейки: период, продажи, коэффициент.
func cellTitle(p time.Time, tab string, cell CalendarCell) string {
	parts := []string{periodFull(p, tab)}
	if cell.QtyText != "" {
		parts = append(parts, cell.QtyText+" шт")
	}
	if cell.KgText != "" {
		parts = append(parts, cell.KgText+" кг")
	}
	if cell.Coeff != "" {
		parts = append(parts, "коэф. "+cell.Coeff)
	} else {
		parts = append(parts, "коэф. 0")
	}

	return strings.Join(parts, " · ")
}

// periodFull — полная подпись периода для подсказки.
func periodFull(p time.Time, tab string) string {
	if tab == TabWeek {
		return "неделя " + p.Format("02.01") + "–" + p.AddDate(0, 0, 6).Format("02.01")
	}

	months := [...]string{"янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

	return months[int(p.Month())-1] + " " + p.Format("2006")
}

// activeSets — периоды доступности по товарам множествами: быстрый поиск
// «период есть» при сборке ячеек.
func activeSets(available map[string][]time.Time) map[string]map[time.Time]struct{} {
	out := make(map[string]map[time.Time]struct{}, len(available))
	for id, periods := range available {
		set := make(map[time.Time]struct{}, len(periods))
		for _, p := range periods {
			set[p] = struct{}{}
		}
		out[id] = set
	}

	return out
}

// oldestInWindow — самая дальняя ячейка, участвующая в среднем. С неё
// начинаются столбцы страницы по умолчанию: иначе участвующая в расчёте
// ячейка оказывается за краем окна.
func oldestInWindow(rowsByID map[string][]averagesales.PeriodSales) time.Time {
	var out time.Time
	for _, list := range rowsByID {
		for _, ps := range list {
			if ps.InWindow && (out.IsZero() || ps.PeriodStart.Before(out)) {
				out = ps.PeriodStart
			}
		}
	}

	return out
}

// periodsFrom — столбцы от периода from до текущего включительно (по
// возрастанию), не больше calendarMaxCells.
func periodsFrom(tab string, from, now time.Time) []time.Time {
	current, step := currentWeekStart(now), func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }
	if tab == TabMonth {
		current = currentMonthStart(now)
		step = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
	}

	out := make([]time.Time, 0, 32)
	for p := from; !p.After(current) && len(out) < calendarMaxCells; p = step(p) {
		out = append(out, p)
	}

	return out
}

// availablePeriods — периоды доступности товаров для ячеек календаря. Читаем
// от самой дальней из границ: окно среднего (lookback), листание по месяцам и
// запрошенный диапазон. Шов не подключён — пустая карта: страница работает
// по-старому (прочерк = продаж не было).
func (uc *GoodsUseCase) availablePeriods(ctx context.Context, tab, from, to, month string) (map[string][]time.Time, error) {
	if uc.availability == nil {
		return nil, nil
	}

	interval := "month"
	if tab == TabWeek {
		interval = "week"
	}

	out, err := uc.availability.AvailablePeriods(ctx, interval, uc.availabilityFloor(tab, month, from, to), uc.now())
	if err != nil {
		return nil, fmt.Errorf("доступность по периодам: %w", err)
	}

	return out, nil
}

// availabilityFloor — с какого периода читать доступность: граница lookback,
// но не позже начала запрошенного диапазона или окна листания.
func (uc *GoodsUseCase) availabilityFloor(tab, month, from, to string) time.Time {
	now := uc.now()
	floor := now.AddDate(0, 0, -7*calendarLookbackWeeks)
	if tab == TabMonth {
		floor = now.AddDate(0, -calendarLookbackMonths, 0)
		if start := monthStart(month, now); start.Before(floor) {
			floor = start
		}
	}
	if r, err := parseRange(tab, from, to, now); err == nil && r.from.Before(floor) {
		floor = r.from
	}

	return floor
}

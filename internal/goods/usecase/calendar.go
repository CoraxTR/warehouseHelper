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

	calendarWeeks  = 6  // окно недель: 5 завершённых + текущая
	calendarMonths = 13 // окно месяцев: 12 завершённых + текущий
)

// SalesCalendarReader — данные продаж для календаря (реализует
// *aucase.UseCase, владелец оборотов).
type SalesCalendarReader interface {
	// Calendar — окно товаров: действующее среднее и периоды с пометкой участия.
	Calendar(ctx context.Context, productIDs []string) (map[string]averagesales.SalesCalendar, error)
	// MonthlyTurnover — исторический диапазон месяцев (листание).
	MonthlyTurnover(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error)
	// WeeklyTurnover — исторический диапазон недель.
	WeeklyTurnover(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error)
}

// PeriodCoeffReader — коэффициенты коррекции по периодам (реализует
// *oucase.UseCase, владелец product_period_coeff).
type PeriodCoeffReader interface {
	CoefficientsBatch(ctx context.Context, productIDs []string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[string]map[time.Time]int16, error)
}

// CalendarCell — одна ячейка календаря (товар × период).
type CalendarCell struct {
	PeriodStart time.Time
	KgText      string // кг с тремя знаками (весовые); пусто — штучный товар
	QtyText     string // штуки за период; пусто — данных нет
	InWindow    bool   // период участвует в расчёте среднего (красная обводка)
	Coeff       string // накопленный коэффициент периода; пусто — нет событий
	Title       string // подсказка ячейки: период, продажи, коэффициент
}

// CalendarRow — строка календаря: товар и его ячейки по столбцам.
type CalendarRow struct {
	ProductID    string
	Name         string
	InternalCode string
	Cells        []CalendarCell
	AvgText      string // действующее среднее, шт/период
	CoeffText    string // коэффициент текущего периода
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
	PrevMonthURL string
	NextMonthURL string
	NextDisabled bool
	AvgHeader    string // подпись колонки средних («средние, шт/нед»)
	Empty        bool
	Error        string
}

// CalendarPage собирает страницу календаря. tab — week|month (пусто = week),
// month — YYYY-MM для месячной вкладки (пусто = текущий месяц).
func (uc *GoodsUseCase) CalendarPage(ctx context.Context, tab, month string) (*CalendarPage, error) {
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

	data, err := uc.calendarData(ctx, tab, month, ids, page)
	if err != nil {
		return nil, err
	}

	for _, p := range data.periods {
		page.PeriodTitles = append(page.PeriodTitles, periodTitle(p, tab))
	}

	coeffs, err := uc.periodCoeffs.CoefficientsBatch(ctx, ids, coeffPeriodType(tab), data.periods)
	if err != nil {
		return nil, fmt.Errorf("коэффициенты периодов: %w", err)
	}

	page.Groups = buildCalendarGroups(visible, data, tab, coeffs)

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
func (uc *GoodsUseCase) calendarData(ctx context.Context, tab, month string, ids []string, page *CalendarPage) (*calendarData, error) {
	if tab == TabWeek {
		return uc.weekData(ctx, ids)
	}

	return uc.monthData(ctx, month, ids, page)
}

// weekData — недельная вкладка: столбцы считаются от текущей недели, продажи —
// из окна averagesales (там же пометка участия в среднем).
func (uc *GoodsUseCase) weekData(ctx context.Context, ids []string) (*calendarData, error) {
	window, err := uc.salesCalendar.Calendar(ctx, ids)
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

	return data, nil
}

// monthData — месячная вкладка: окно заканчивается выбранным месяцем.
func (uc *GoodsUseCase) monthData(ctx context.Context, month string, ids []string, page *CalendarPage) (*calendarData, error) {
	now := uc.now()
	start, err := monthStart(month, now)
	if err != nil {
		return nil, err
	}

	// Листание: назад — история, вперёд — обратно к действующему окну.
	rangeStart := start.AddDate(0, -(calendarMonths - 1), 0)
	page.MonthParam = start.Format("2006-01")
	page.PrevMonthURL = monthURL(start.AddDate(0, -1, 0))
	next := start.AddDate(0, 1, 0)
	page.NextMonthURL = monthURL(next)
	page.NextDisabled = !next.Before(currentMonthStart(now))

	data := &calendarData{periods: monthPeriods(rangeStart, calendarMonths)}

	if start.Equal(currentMonthStart(now)) {
		return uc.currentMonthData(ctx, ids, data)
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
func (uc *GoodsUseCase) currentMonthData(ctx context.Context, ids []string, data *calendarData) (*calendarData, error) {
	window, err := uc.salesCalendar.Calendar(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("продажи окна: %w", err)
	}

	data.rowsByID = make(map[string][]averagesales.PeriodSales, len(window))
	data.avg = make(map[string]float64, len(window))
	for id, cal := range window {
		data.rowsByID[id] = cal.Periods
		data.avg[id] = cal.Avg
	}

	return data, nil
}

// buildCalendarGroups — строки и группы страницы.
func buildCalendarGroups(visible []productInfo, data *calendarData, tab string, coeffs map[string]map[time.Time]int16) []CalendarGroup {
	groups := make([]CalendarGroup, 0, 8)
	groupIndex := make(map[string]int, 8)
	currentStart := data.periods[len(data.periods)-1]

	for _, v := range visible {
		var avg *float64
		if a, ok := data.avg[v.ID]; ok {
			avg = &a
		}
		row := buildCalendarRow(v, data.rowsByID[v.ID], avg, coeffs[v.ID], data.periods, tab, currentStart)

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
func buildCalendarRow(v productInfo, sales []averagesales.PeriodSales, avg *float64, coeffs map[time.Time]int16, periods []time.Time, tab string, currentStart time.Time) CalendarRow {
	row := CalendarRow{
		ProductID:    v.ID,
		Name:         v.Name,
		InternalCode: v.Code,
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
		}
		if c, ok := coeffs[p]; ok && c != 0 {
			cell.Coeff = formatCoeff(c)
		}
		cell.Title = cellTitle(p, tab, cell)
		row.Cells = append(row.Cells, cell)
	}

	if avg != nil {
		row.AvgText = formatQty(*avg)
	}
	if c, ok := coeffs[currentStart]; ok && c != 0 {
		row.CoeffText = formatCoeff(c)
	}

	return row
}

// productsForTab — товары вкладки: недельная — только с недельным учётом.
func productsForTab(products []domain.Product, tab string) []productInfo {
	out := make([]productInfo, 0, len(products))
	for _, p := range products {
		if tab == TabWeek && !p.TrackWeekly {
			continue
		}
		out = append(out, productInfo{ID: p.ID, Name: p.Name, Code: p.InternalCode, Group: p.GroupName})
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
	ID    string
	Name  string
	Code  string
	Group string
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

// monthStart — первый месяц диапазона: выбранный месяц или текущий.
func monthStart(month string, now time.Time) (time.Time, error) {
	cur := currentMonthStart(now)
	if strings.TrimSpace(month) == "" {
		return cur, nil
	}

	parsed, err := time.Parse("2006-01", strings.TrimSpace(month))
	if err != nil {
		// Кривой параметр не ломает страницу — показываем текущий диапазон.
		return cur, nil //nolint:nilerr // параметр адреса, не данные
	}

	if parsed.After(cur) {
		return cur, nil
	}

	return parsed, nil
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

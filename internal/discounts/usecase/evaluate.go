// Состояние пар (лот + канал сайта) на момент расчёта: общий шаг тика избытка,
// утреннего пересчёта по сроку и реестра — чтобы «что считается» было в одном
// месте, а не расползалось по recalc и registry.
package usecase

import (
	"sort"
	"time"

	"warehouseHelper/internal/discounts"
)

// Дни периода оборота: недельный ряд — неделя, месячный — месяц
// (решение владельца 14.09.2026, черновик §12).
const (
	weekDays  = 7
	monthDays = 30
)

// PairState — состояние одной пары (лот + канал сайта) на момент расчёта:
// что даёт каждый источник скидки и что стоит в product_stock сейчас.
type PairState struct {
	Key        discounts.LotKey
	ProductID  string
	Name       string
	GroupName  string
	ShortList  bool
	BestBefore time.Time
	DaysLeft   int    // best_before − today; последний день (0) — вне расчёта
	ShelfLife  *int16 // NULL — срок годности не задан
	Qty        int64  // остаток лота

	// Накопленный остаток: Q пары плюс все пары товара с меньшим сроком
	// (передние по FIFO), включая саму пару.
	CumQty int64

	// Действующий средний оборот: Turnover — за период (шт), Rate — в день.
	Turnover   float64
	PeriodDays int
	Rate       float64
	HasRate    bool // есть ли данные о продажах (нет данных → избытка нет)

	// Кандидаты по каналам: nil — источник скидку не даёт.
	Manual     *int16  // ручная скидка канала сайта (>0)
	Expiry     *int16  // лестница по сроку на сегодня (>0), вне окна — nil
	Surplus    *int16  // 10 при избытке, nil — избытка нет
	Coeff      float64 // коэффициент избытка Q/(v×D), >1 — избыток
	HasSurplus bool

	// То, что стоит в БД: Applied — эффективная скидка канала (ручная
	// перекрывает plain), AppliedPlain — значение plain-колонки,
	// SourceRaw — метка источника plain-значения (product_stock.discount_source).
	Applied       *int16
	AppliedPlain  *int16
	SourceRaw     string
	TelegramPlain *int16 // план ТГ-колонки: её пишет только ТГ-день (14:00/16:00)
}

// Desired — какой источник должен победить по кандидатам дня (приоритет
// ручная → срок → избыток). nil — ни один источник скидку не даёт.
func (p PairState) Desired() (*int16, discounts.Source) {
	return discounts.Resolve(p.Manual, p.Expiry, p.Surplus)
}

// Row — строка отчёта/реестра по паре (канал сайта).
func (p PairState) Row() discounts.Row {
	percent, src := p.Desired()
	row := discounts.Row{
		ProductID:  p.ProductID,
		Name:       p.Name,
		BestBefore: p.BestBefore,
		Source:     src,
		DaysLeft:   p.DaysLeft,
	}
	if percent != nil {
		row.Percent = *percent
	}
	if p.HasSurplus {
		row.Coeff = p.Coeff
	}
	return row
}

// SurplusPairs — пары, у которых избыток есть сейчас (кандидаты на обновление
// оборота: у них решение может уйти в любой момент).
func SurplusPairs(pairs []PairState) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if !p.HasSurplus {
			continue
		}
		if _, ok := seen[p.ProductID]; ok {
			continue
		}
		seen[p.ProductID] = struct{}{}
		ids = append(ids, p.ProductID)
	}
	return ids
}

// Evaluate — состояния всех пар входа на момент расчёта.
//
// rates — действующий средний оборот за период (шт) от модуля средних продаж
// (шов Turnover): для кандидатов — свежий (RefreshCurrent), для остальных —
// сохранённый (Averages). Единственный источник оборота: своих запросов к
// таблицам оборота у расчёта нет. Товар без данных (нет в карте либо оборот
// <= 0) избытка не получает — решение владельца 14.09.2026.
//
// today — дата расчёта (локальная дата склада, время обнуляется).
func Evaluate(inputs []discounts.Input, rates map[string]float64, today time.Time) []PairState {
	day := beginningOfDay(today)

	out := make([]PairState, 0, len(inputs))
	byProduct := groupByProduct(inputs)

	for _, pid := range orderProducts(inputs) {
		lots := byProduct[pid]
		var cum int64
		for _, in := range lots {
			cum += in.Qty
			out = append(out, evaluatePair(in, cum, rates, day))
		}
	}

	return out
}

// evaluatePair — состояние одной пары; cumQty уже включает её остаток.
func evaluatePair(in discounts.Input, cumQty int64, rates map[string]float64, day time.Time) PairState {
	daysLeft := daysBetween(day, in.BestBefore)

	periodDays := in.PeriodDays
	if periodDays <= 0 {
		// Дни периода по ряду оборота: недельный — 7, месячный — 30
		// (решение владельца 14.09.2026, черновик §12).
		periodDays = monthDays
		if in.TrackWeekly {
			periodDays = weekDays
		}
	}
	// Оборот приходит ТОЛЬКО швом модуля средних продаж: своего SQL по его
	// таблицам расчёт не делает. Товара нет в карте — данных о продажах нет.
	turnover := rates[in.ProductID]
	rate := discounts.DailyRate(turnover, periodDays)
	hasRate := rate > 0

	p := PairState{
		Key:           discounts.LotKey{ProductID: in.ProductID, BestBefore: beginningOfDay(in.BestBefore)},
		ProductID:     in.ProductID,
		Name:          in.Name,
		GroupName:     in.GroupName,
		ShortList:     in.ShortList,
		BestBefore:    in.BestBefore,
		DaysLeft:      daysLeft,
		ShelfLife:     in.ShelfLife,
		Qty:           in.Qty,
		CumQty:        cumQty,
		Turnover:      turnover,
		PeriodDays:    periodDays,
		Rate:          rate,
		HasRate:       hasRate,
		Manual:        positiveDiscount(in.GeneralManual),
		Applied:       effectiveDiscount(in.GeneralManual, in.GeneralPlain),
		AppliedPlain:  positiveDiscount(in.GeneralPlain),
		SourceRaw:     in.DiscountSource,
		TelegramPlain: positiveDiscount(in.TelegramPlain),
	}

	if in.ShelfLife != nil {
		if expiry := discounts.ExpiryPercent(*in.ShelfLife, daysLeft); expiry > 0 {
			p.Expiry = &expiry
		}
	}

	if coef, ok := discounts.SurplusCoeff(cumQty, rate, daysLeft); ok {
		percent := discounts.SurplusPercent()
		p.Surplus = &percent
		p.Coeff = coef
		p.HasSurplus = true
	} else if hasRate {
		// Коэффициент без избытка полезен в отчёте (видно, насколько близко).
		p.Coeff = coef
	}

	return p
}

// groupByProduct — лоты по товарам в порядке возрастания срока (FIFO:
// накопленный остаток считается по передним парам).
func groupByProduct(inputs []discounts.Input) map[string][]discounts.Input {
	out := make(map[string][]discounts.Input, len(inputs))
	for _, in := range inputs {
		out[in.ProductID] = append(out[in.ProductID], in)
	}
	for pid := range out {
		lots := out[pid]
		sort.SliceStable(lots, func(i, j int) bool {
			return lots[i].BestBefore.Before(lots[j].BestBefore)
		})
		out[pid] = lots
	}
	return out
}

// orderProducts — товары в порядке первого появления во входе (стабильный
// порядок отчёта при равных ключах сортировки).
func orderProducts(inputs []discounts.Input) []string {
	seen := make(map[string]struct{}, len(inputs))
	order := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if _, ok := seen[in.ProductID]; ok {
			continue
		}
		seen[in.ProductID] = struct{}{}
		order = append(order, in.ProductID)
	}
	return order
}

// effectiveDiscount — эффективная скидка канала: ручная перекрывает plain,
// ноль и nil равнозначны «скидки нет» (правило «0 = NULL»).
func effectiveDiscount(manual, plain *int16) *int16 {
	if v := positiveDiscount(manual); v != nil {
		return v
	}
	return positiveDiscount(plain)
}

// positiveDiscount — скидка, если она задана и больше нуля; иначе nil.
func positiveDiscount(v *int16) *int16 {
	if v == nil || *v <= 0 {
		return nil
	}
	return v
}

// beginningOfDay — дата без времени (локальная дата склада).
func beginningOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// daysBetween — число календарных дней между датами (без часовых поясов:
// обе даты приводятся к своим суткам).
func daysBetween(from, to time.Time) int {
	f := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	t := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(t.Sub(f) / (24 * time.Hour))
}

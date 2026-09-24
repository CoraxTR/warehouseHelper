// Состояние пар (лот + канал сайта) на момент расчёта: общий шаг тика избытка,
// утреннего пересчёта по сроку и реестра — чтобы «что считается» было в одном
// месте, а не расползалось по recalc и registry.
package usecase

import (
	"slices"
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
	Manual     *int16  // ручная скидка канала сайта: nil — ручного нет, значение (в т.ч. 0 — «скидка 0 %», пара заморожена) применяется
	Expiry     *int16  // лестница по сроку на сегодня (>0), вне окна — nil
	Surplus    *int16  // 10 при избытке, nil — избытка нет
	Coeff      float64 // коэффициент избытка Q/(v×D), >1 — избыток
	HasSurplus bool
	// SurplusGroup — пара входит в группу избытка товара: избыток есть у неё
	// самой или у любого более далёкого срока, и скидка 10 % ложится на все
	// сроки группы (опора A, решение владельца 23.09.2026). Ставит Evaluate.
	SurplusGroup bool

	// BlockedByManualZero — пара накрыта запретом менеджера: у товара есть пара
	// с ручной 0 («скидка 0 %») и её срок не позже этой. Запрет каскадный:
	// «0 блокирует все сроки дальше того, на который поставлен» (решение
	// владельца, 15.09.2026), поэтому запрет получает и сама нулевая пара,
	// и все пары товара с более далёким сроком. Ставит флаг Evaluate.
	BlockedByManualZero bool

	// То, что стоит в БД: Applied — эффективная скидка канала (ручная
	// перекрывает plain), AppliedPlain — значение plain-колонки,
	// SourceRaw — метка источника plain-значения (product_stock.discount_source).
	Applied       *int16
	AppliedPlain  *int16
	SourceRaw     string
	TelegramPlain *int16 // план ТГ-колонки: её пишет только ТГ-день (14:00/16:00)
	// TelegramManual — ручная скидка ТГ-канала: метка «ТГ» важнее plain — так же,
	// как ручная сайта важнее его plain-колонки.
	TelegramManual *int16
	// SurplusPlanQty — план продаж по паре группы избытка, шт: сколько её остатка
	// надо продать, чтобы коэффициент группы стал < 1 (раскладка FIFO). Им
	// печатается количество в отчёте и дайджесте; 0 — пара вне раскладки.
	SurplusPlanQty int64
}

// Desired — какой источник должен победить по кандидатам дня (приоритет
// ручная → срок → избыток). nil — ни один источник скидку не даёт.
// Замороженная ручным нулём пара даёт (0, SourceManual): скидки-значения нет,
// но источник есть — это «скидка 0 %», а не «скидки нет» (см. Frozen).
func (p PairState) Desired() (*int16, discounts.Source) {
	if p.BlockedByManualZero && p.Manual == nil {
		// Каскадный запрет менеджера: скидки по паре быть не должно, ни один
		// источник не победитель (скидка снимается — см. banWrites).
		return nil, discounts.SourceNone
	}
	return discounts.Resolve(p.Manual, p.Expiry, p.Surplus)
}

// Frozen — пара заморожена ручным нулём: ручная скидка задана и равна 0
// («скидка 0 %», решение владельца, сентябрь 2026). Движок такую пару не
// трогает вовсе: plain не пишет, уведомлений «пора X %» не шлёт, в план
// ТГ-слота и дайджест не берёт (см. expiryWrites/surplusWrites/activeLocked/
// slotEligible). Ручная > 0 работает как раньше: значение держит ручная,
// автомат может поднимать только plain-колонку выше неё.
func (p PairState) Frozen() bool {
	return p.BlockedByManualZero || zeroManual(p.Manual)
}

// zeroManual — ручная скидка задана нулём: «0 %», а не «скидки нет».
func zeroManual(v *int16) bool {
	return v != nil && *v == 0
}

// banThreshold — порог запрета скидок по товару: самый близкий срок среди пар
// с заданной ручной нулём (по любой из колонок каналов — решение владельца
// 15.09.2026, каналы симметричны). Пары с годностью не раньше порога
// автоматических скидок не получают вовсе. nil — запрета нет.
func banThreshold(lots []discounts.Input) *time.Time {
	var ban *time.Time
	for _, in := range lots {
		if !zeroManual(in.GeneralManual) && !zeroManual(in.TelegramManual) {
			continue
		}
		if ban == nil || in.BestBefore.Before(*ban) {
			bb := in.BestBefore
			ban = &bb
		}
	}
	return ban
}

// Row — строка отчёта/реестра по паре (канал сайта).
func (p PairState) Row() discounts.Row {
	percent, src := p.Desired()
	row := discounts.Row{
		ProductID:  p.ProductID,
		Name:       p.Name,
		BestBefore: p.BestBefore,
		Source:     src,
		Telegram:   p.telegramAboveSite(),
		DaysLeft:   p.DaysLeft,
		// Группа — только у пар, где избыток и есть победившая скидка. Если
		// ступень по сроку глубже (или есть ручная), пара печатается своим
		// источником, а не «избытком»: иначе в таблице стояло «избыток» при
		// скидке 30 % (жалоба владельца, 23.09.2026).
		SurplusGroup: p.SurplusGroup && src == discounts.SourceSurplus,
	}
	if percent != nil {
		row.Percent = *percent
	}
	if p.HasSurplus {
		row.Coeff = p.Coeff
	}
	// Количество, попавшее в скидку (ответ владельца 24.09.2026): у избытка —
	// план продаж по паре, у ручных и сроковых — весь остаток пары.
	if src == discounts.SourceSurplus {
		row.Qty = p.SurplusPlanQty
	} else {
		row.Qty = p.Qty
	}
	return row
}

// telegramAboveSite — скидка живёт в ТГ-колонке: её значение (ручная в ТГ
// важнее plain — как и на сайте) строго выше эффективной скидки сайта. Пока
// подъём (16:00) не довёл general до плана, строка печатается меткой (ТГ);
// после — своим источником. Отдельного переключателя нет: флаг читается из
// данных при каждой сборке строки (решение владельца 24.09.2026).
func (p PairState) telegramAboveSite() bool {
	tg := effectiveDiscount(p.TelegramManual, p.TelegramPlain)
	if tg == nil || *tg <= 0 {
		return false
	}
	site := p.Applied
	return site == nil || *tg > *site
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
		ban := banThreshold(lots)
		var cum int64
		first := len(out)
		for _, in := range lots {
			cum += in.Qty
			state := evaluatePair(in, cum, rates, day)
			if ban != nil && !in.BestBefore.Before(*ban) {
				state.BlockedByManualZero = true
			}
			out = append(out, state)
		}
		applySurplusGroup(out[first:])
		applyPlanQty(out[first:])
	}

	return out
}

// applySurplusGroup — группа избытка товара по опоре A (решение владельца
// 23.09.2026): избыток на сроке распространяется на все более близкие сроки
// товара. Пары товара идут по возрастанию срока, поэтому идём от дальнего к
// ближнему с накопленным максимумом коэффициента: пока он > 1 (избыток есть у
// этой пары или у любой более далёкой), пара в группе. Группа — общий префикс до
// самого дальнего избыточного срока: пары за ним избытка не имеют и скидки не
// получают.
//
// Паре без своего избытка ставится скидка 10 % (кандидат Surplus) и коэффициент
// группы — им она и печатается. Снятие избытка отдельного кода не требует:
// участие пересчитывается здесь же из коэффициентов, а запись в БД уходит по
// HasSurplus (метку источника снимает модуль, см. surplusWrites).
func applySurplusGroup(pairs []PairState) {
	var maxCoeff float64
	for i := len(pairs) - 1; i >= 0; i-- {
		if pairs[i].Coeff > maxCoeff {
			maxCoeff = pairs[i].Coeff
		}
		if maxCoeff <= 1 {
			continue
		}
		if pairs[i].DaysLeft <= 0 {
			continue // просроченная пара в группу не входит: скидка ей не нужна
		}
		pairs[i].SurplusGroup = true
		if pairs[i].HasSurplus {
			continue
		}
		percent := discounts.SurplusPercent()
		pairs[i].Surplus = &percent
		pairs[i].HasSurplus = true
		pairs[i].Coeff = maxCoeff
	}
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
		Manual:        manualDiscount(in.GeneralManual),
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
		slices.SortStableFunc(lots, func(a, b discounts.Input) int {
			return a.BestBefore.Compare(b.BestBefore)
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

// effectiveDiscount — эффективная скидка канала: ручная перекрывает plain
// КАК ЕСТЬ, включая заданный ноль («0 %», пара заморожена). plain применяется,
// только если ручной нет; легаси-ноль в plain-колонке значит «скидки нет»
// (движок туда нулей не пишет). Решение владельца, сентябрь 2026 (было
// «0 = NULL» для обеих колонок, 14.09.2026).
func effectiveDiscount(manual, plain *int16) *int16 {
	if v := manualDiscount(manual); v != nil {
		return v
	}
	return positiveDiscount(plain)
}

// manualDiscount — ручная скидка как она есть: nil — ручного применения нет,
// заданное значение (в том числе 0 — «скидка 0 %») применяется. Отрицательное
// значение — мусор из БД (запись валидируется 0..100), как и в discounts.Resolve.
func manualDiscount(v *int16) *int16 {
	if v == nil || *v < 0 {
		return nil
	}
	return v
}

// positiveDiscount — plain-скидка, если она задана и больше нуля; иначе nil
// (ноль в plain-колонке — legacy-«скидки нет», репозиторий отдаёт значение
// как есть, трактовку держит домен).
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

// applyPlanQty — план продаж по парам группы избытка: сколько остатка каждой
// пары надо продать, чтобы коэффициент группы стал < 1 (раскладка FIFO, см.
// SurplusSalePlan). Нужен для печати количества в отчёте и дайджесте; слот
// считает ту же раскладку сам — там к плану добавляются остаток на момент
// плана и место в ёмкости.
func applyPlanQty(pairs []PairState) {
	_, sales := SurplusSalePlan(pairs)
	if len(sales) == 0 {
		return
	}
	byKey := make(map[discounts.LotKey]int64, len(sales))
	for _, s := range sales {
		byKey[s.Key] = s.Qty
	}
	for i := range pairs {
		if qty, ok := byKey[pairs[i].Key]; ok {
			pairs[i].SurplusPlanQty = qty
		}
	}
}

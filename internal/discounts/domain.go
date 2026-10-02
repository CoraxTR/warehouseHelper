// Пакет discounts — модуль расчёта скидок.
// Считает глубину скидки по сроку годности (лестница от остатка дней) и по
// избытку остатка относительно скорости продаж, сводит кандидатов по каналам
// и отдаёт строки для страницы «Скидки», ТГ-слота и дайджеста.
// Пакет — чистая логика без БД: входы формула получает через тип Input
// (заполняет репозиторий модуля), запись значений идёт через шов стока.
package discounts

import (
	"math"
	"time"
)

// Параметры лестницы по сроку годности (согласованы 14.09.2026,
// черновик §4: /root/notes/warehouseHelper-discounts-draft.md).
const (
	twinCoef = 1.49  // a = 7 / 14^0,586 — опора «14 дн → окно 7 дн»
	twinExp  = 0.586 // k = ln(90/7) / ln(1095/14)
	winMax   = 90    // потолок окна, дн (три месяца)

	// Разгон средних и длинных сроков (решение владельца 21.09.2026): 90-дневная
	// позиция должна входить в скидку за 30 дн, а не за 20,81 (по КТ было 17–20).
	rampShelf = 90   // срок, на котором разгон максимален, дн
	rampBase  = 30   // окно на этом сроке, дн
	rampSigma = 0.6  // ширина разгона в ln(Г/rampShelf); σ ≥ 0,6 — иначе окно немонотонно
	shelfCap  = 1095 // три года: здесь окно ровно упирается в потолок

	ktStep    = 2.33 // один пересмотр лестницы, дн
	maxPoints = 5    // максимум точек лестницы
	autoCap   = 50   // потолок автоматической скидки, % (выше — только вручную)
)

// rampAmp — амплитуда разгона: 30 / (1,49 × 90^0,586) − 1 ≈ 0,4417. Подобрана так,
// чтобы на опоре множитель давал ровно 30 дн, а не «около 30».
var rampAmp = float64(rampBase)/(twinCoef*math.Pow(rampShelf, twinExp)) - 1

// Window — окно скидки в днях для срока годности (Г). Одна формула, без ветвлений:
// прежняя кривая плюс плавный разгон с максимумом ровно на опоре 90 дн.
//
//	W = min(1,49 × Г^0,586 × (1 + 0,4417 × exp(−(ln(Г/90)/0,6)²)) ; 90)
//
// Свойства, ради которых форма такая: короткие сроки остаются прежними (до 21 дн
// разгон добавляет меньше 0,01 дн), на опоре ровно 30 дн, излома на опоре нет
// (89 дн → 29,8; 90 дн → 30), окно монотонно растёт по сроку, потолок 90 дн
// по-прежнему включается ровно на трёх годах. Срок не задан (shelfLife <= 0) →
// окна нет (0).
func Window(shelfLife int16) float64 {
	if shelfLife <= 0 {
		return 0
	}
	g := float64(shelfLife)
	ramp := math.Exp(-math.Pow(math.Log(g/rampShelf)/rampSigma, 2))
	return min(twinCoef*math.Pow(g, twinExp)*(1+rampAmp*ramp), float64(winMax))
}

// Points — число точек (ступеней) лестницы внутри окна: clamp(floor(W/2,33), 1, 5).
// Ровно 2,33 дн на один пересмотр, поэтому короткие сроки получают одну точку.
func Points(w float64) int {
	if w <= 0 {
		return 1
	}
	n := int(math.Floor(w/ktStep + 1e-9))
	if n < 1 {
		return 1
	}
	if n > maxPoints {
		return maxPoints
	}
	return n
}

// Step — шаг лестницы в днях: W / N. Без точек (n <= 0) шага нет (0).
func Step(w float64, n int) float64 {
	if w <= 0 || n <= 0 {
		return 0
	}
	return w / float64(n)
}

// winEps — допуск сравнения окна. Константы кривой (1,49 и 0,586) округлены, поэтому
// «ровно 7 дней» у 14-дневной партии приходит как 6,9957. Без допуска она потеряла бы
// седьмой день, а он — опора §4. Допуск много меньше половины дня, так что целых дней
// в окне по-прежнему ровно floor(окна).
const winEps = 0.05

// inWindow — «остаток дней ещё внутри окна». Окно округляется ВНИЗ до целого дня:
// остаток срока всегда целый, дробная часть окна лишнего дня скидки не даёт.
func inWindow(w float64, daysLeft int) bool {
	if w <= 0 {
		return false
	}
	return daysLeft <= int(math.Floor(w+winEps))
}

// asInt16 — приведение int к int16 с проверкой границ. Значения расчёта
// заведомо малы (0..50), но конверсия без проверки не проходит линт (gosec
// G115): проверка границ — его же рекомендованный способ.
func asInt16(v int) int16 {
	if v < math.MinInt16 || v > math.MaxInt16 {
		return 0 // недостижимо: ступени лестницы не выходят из 10..50
	}
	return int16(v)
}

// startPercent — первая (минимальная) ступень лестницы: 50 − 10×(N−1).
// N = 1 → 50, N = 3 → 30, N = 5 → 10.
func startPercent(n int) int16 {
	if n < 1 {
		n = 1
	}
	return asInt16(autoCap - 10*(n-1))
}

// ExpiryPercent — скидка по сроку годности, %.
// Вне окна (D > W), по просроченной партии (D <= 0) и без заданного срока — 0.
// Внутри окна ступени идут от старта вверх по 10 % за точку:
// k = min(floor(D/шаг), N−1), d = max(старт, 50 − 10×k). Потолок автомата — 50.
func ExpiryPercent(shelfLife int16, daysLeft int) int16 {
	w := Window(shelfLife)
	if !inWindow(w, daysLeft) || daysLeft <= 0 {
		return 0
	}
	n := Points(w)
	step := Step(w, n)
	if step <= 0 {
		return startPercent(n)
	}
	k := int(math.Floor(float64(daysLeft)/step + 1e-9))
	k = min(k, n-1)
	d := asInt16(autoCap - 10*k)
	if start := startPercent(n); d < start {
		d = start
	}
	return d
}

// surplusPercent — скидка по избытку, %: всегда 10 (решение владельца).
const surplusPercent = 10

// DailyRate — скорость продаж, шт/день: оборот за период, делённый на дни
// периода (недельный ряд — на 7, месячный — на 30). Период не задан
// (periodDays <= 0) или оборота нет (в т.ч. отрицательный после возвратов
// задним числом) → 0.
func DailyRate(turnover float64, periodDays int) float64 {
	if periodDays <= 0 || turnover <= 0 {
		return 0
	}
	return turnover / float64(periodDays)
}

// SurplusCoeff — во сколько раз накопленного остатка больше, чем успеет
// продаться за оставшийся срок: coef = Q / (v × D), где Q — остаток по паре и
// всем парам с меньшим сроком включительно, v — скорость (DailyRate),
// D — остаток дней. Избыток есть ⇔ coef > 1 (строгое сравнение: «ровно
// столько, сколько продаётся» — ещё не избыток).
// Нет данных об обороте, v <= 0 или D <= 0 → (0, false).
// При coef <= 1 возвращается сам коэф (нужен для отладки/отчёта), ok = false.
// Отдельного признака «данные оборота есть» не нужно: скорость v = 0 бывает
// ровно тогда, когда данных нет, — проверки rate достаточно.
func SurplusCoeff(cumQty int64, rate float64, daysLeft int) (float64, bool) {
	if rate <= 0 || daysLeft <= 0 {
		return 0, false
	}
	coef := float64(cumQty) / (rate * float64(daysLeft))
	return coef, coef > 1
}

// SurplusPercent — скидка по избытку, %: константа 10 (отдельная функция,
// чтобы число не «магичило» в resolve и в текстах отчёта).
func SurplusPercent() int16 { return surplusPercent }

// Source — источник скидки пары (лот, канал). Порядок значений задаёт приоритет
// сведения кандидатов, поэтому SourceNone = 0 — «скидки нет».
type Source int

// Источники скидки по убыванию приоритета.
const (
	SourceNone           Source = iota // скидки нет
	SourceManual                       // ручная скидка менеджера (пишет stock, UI сроков)
	SourceTelegramManual               // ручная скидка ТГ-канала: пара в работе, сайт берёт её подъёмом 16:00
	SourceExpiry                       // лестница по сроку годности
	SourceSurplus                      // избыток остатка к скорости продаж
)

// String — короткое имя источника для логов и отчётов.
func (s Source) String() string {
	switch s {
	case SourceNone:
		return "none" // скидки нет: в product_stock это NULL (пустая метка)
	case SourceManual:
		return "manual"
	case SourceTelegramManual:
		return "manual_tg"
	case SourceExpiry:
		return "expiry"
	case SourceSurplus:
		return "surplus"
	}
	return "none" // недостижимо: все значения Source перечислены выше
}

// Owner — владелец стоящего значения колонки пары (product_stock
// .discount_general_owner / .discount_telegram_owner): кто поставил значение.
// Метка Source отвечает на «почему скидка», Owner — на «кто поставил это
// значение»; расчёт снимает значение по владельцу, а не по числу (решение
// владельца 02.10.2026). Порядок значений роли не играет.
type Owner int

// Владельцы значения.
const (
	OwnerNone       Owner = iota // владельца нет: значения нет либо он не известен
	OwnerSurplus                 // значение поставлено движком по избытку
	OwnerExpiry                  // значение поставлено ступенью по сроку годности
	OwnerEscalation              // значение поставлено ТГ-днём (план 14:00, подъём 16:00)
	OwnerManual                  // значение поставлено человеком (подъём ручной ТГ на сайт)
)

// ownerEscalation — значение владельца «ТГ-день». Своей метки причины у него нет
// (метка пишется из причины позиции плана), поэтому литерал живёт здесь один раз.
const ownerEscalation = "escalation"

// String — значение колонки product_stock.discount_*_owner: пустая строка =
// владельца нет (в БД NULL). Имена владельцев «избыток» и «срок» совпадают с
// метками причин (Reason*): это разные вопросы об одном основании — почему скидка
// (метка) и кто поставил стоящее значение (владелец).
func (o Owner) String() string {
	switch o {
	case OwnerNone:
		return ""
	case OwnerSurplus:
		return ReasonSurplus
	case OwnerExpiry:
		return ReasonExpiry
	case OwnerEscalation:
		return ownerEscalation
	case OwnerManual:
		return ReasonManual
	}
	return ""
}

// ParseOwner — владелец из значения колонки БД (CHECK в схеме). Неизвестное
// значение читается как OwnerNone («владелец не наш») — такое значение расчёт не
// снимает: молча трактовать чужую метку как свою опаснее, чем оставить как есть.
func ParseOwner(s string) Owner {
	switch s {
	case ReasonSurplus:
		return OwnerSurplus
	case ReasonExpiry:
		return OwnerExpiry
	case ownerEscalation:
		return OwnerEscalation
	case ReasonManual:
		return OwnerManual
	}
	return OwnerNone
}

// LotPlan — контроль продаж добора по паре из истории рассылок ТГ-дня: остаток
// пары в момент плана и план продаж по ней (initial_qty/plan_qty позиции
// рассылки). Задан только у добора из избытка — у сроковых позиций плана нет.
type LotPlan struct {
	Initial int64
	Plan    int64
}

// Done — план продаж добора выполнен: остатка осталось не больше, чем
// планировали продать за день (контроль вечернего подъёма, решение владельца
// 24.09.2026). По нему выходит скидка, поставленная ТГ-днём.
func (p LotPlan) Done(qty int64) bool {
	return qty <= p.Initial-p.Plan
}

// Candidate — один претендент на скидку пары (лот, канал) от своего источника.
// Percent == nil — источник скидку не дал.
type Candidate struct {
	Source  Source
	Percent *int16
}

// Resolve — победитель среди кандидатов канала по приоритету:
// ручная → срок годности → избыток.
//
// Ручная скидка задана (manual != nil) — побеждает как есть, ВКЛЮЧАЯ 0:
// «0 %» — такая же скидка менеджера, как 30 %, и лестницу расчётных скидок она
// блокирует (решение владельца, сентябрь 2026 — инверсия прежнего «0 = NULL»).
// nil ручной — ручного применения нет, скидку даёт расчётный источник
// (срок или избыток), и только при значении > 0. Отрицательное значение ручной —
// мусор из БД (запись валидируется 0..100), скидкой не считается.
// Нет ни одного кандидата → (nil, SourceNone).
func Resolve(manual, expiry, surplus *int16) (*int16, Source) {
	if manual != nil && *manual >= 0 {
		return manual, SourceManual
	}
	cands := []Candidate{
		{Source: SourceExpiry, Percent: expiry},
		{Source: SourceSurplus, Percent: surplus},
	}
	for _, c := range cands {
		if c.Percent != nil && *c.Percent > 0 {
			return c.Percent, c.Source
		}
	}
	return nil, SourceNone
}

// Input — вход матчинга формул: одна пара (товар, срок) вместе с товарными
// признаками и данными оборота. Заполняет репозиторий модуля (Task 6: чтение
// product_stock JOIN products прямым SQL по образцу daystate.LotsSnapshot);
// расчётная логика пакета работает только с этим типом и БД не знает.
type Input struct {
	ProductID   string
	Name        string
	GroupName   string
	ShortList   bool
	TrackWeekly bool
	ShelfLife   *int16 // NULL — срок не задан
	BestBefore  time.Time
	Qty         int64
	PeriodDays  int // дни периода оборота: 7 (недельный ряд) или 30 (месячный)

	// Текущие скидки лота (product_stock): plain — «простые» колонки, их пишет
	// движок расчёта; manual — ручные, их пишет UI сроков. NULL = не задана.
	GeneralPlain   *int16
	GeneralManual  *int16
	TelegramPlain  *int16
	TelegramManual *int16
	// DiscountSource — источник действующего plain-значения general
	// (product_stock.discount_source): "expiry" | "surplus" | "" — не задан.
	// Нужен для подсветки и подписи строки отчёта («почему скидка»).
	DiscountSource string
	// GeneralOwner — владелец стоящего значения колонки сайта
	// (product_stock.discount_general_owner): "surplus" | "expiry" |
	// "escalation" | "" — владельца нет. Отвечает на «кто поставил значение»:
	// снятие работает по владельцу, а не по числу (решение владельца 02.10.2026).
	GeneralOwner string
	// TelegramOwner — владелец стоящего значения ТГ-колонки
	// (product_stock.discount_telegram_owner): её пишет ТГ-день → "escalation".
	TelegramOwner string
}

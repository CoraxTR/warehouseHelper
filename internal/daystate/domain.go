// Пакет daystate — модуль состояния товара по дням.
// Владеет product_day_state: строка (товар, день) — дневной снимок состояния
// товара, который утром генерируется из product_stock и живёт событиями дня
// (изменения остатков стоком, доступность из календаря, возвраты).
// Клиенты ходят через интерфейсы usecase (сток — DayStateRecorder, календарь —
// SetOrderable, будущее расформирование — RollbackSoldOut); эмитенты фактов
// (SoldOut/Unavailable/откат) уходят в ordercoeff.
package daystate

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DayState — строка product_day_state (товар × день).
//
// InStock: true/false — состояние известно; nil — неизвестно (строки будущих
// дат, созданные календарём «Доступность»).
// DiscountStart — скидка на начало дня (max по лотам на момент создания
// строки); в течение дня НЕ меняется.
// Discount — актуальная скидка: пересчитывается при каждом событии из лотов;
// понижения не логируются (значение восстанавливается только из колонки).
// DiscountIncreases — только повышения скидки за день (значения, без времени).
// SoldOutToday — «позиция закончилась в течение дня»; обычный приход маркер
// не снимает, сбрасывается только явным возвратом (RollbackSoldOut).
// SoldOutWithdrawn — «+1 «закончился» этого дня снят бэктречной недоступностью;
// при возврате доступности +1 возвращается (инвариант: недоступность и
// коэффициент не расходятся)».
type DayState struct {
	ProductID         string
	Date              time.Time
	InStock           *bool
	DiscountStart     *int16
	Discount          *int16
	DiscountIncreases []int16
	Orderable         bool
	SoldOutToday      bool
	SoldOutWithdrawn  bool
}

// LotState — срез лота из product_stock, нужный для пересчёта дня.
type LotState struct {
	Qty int64
	// EffectiveGeneral — эффективная скидка канала «сайт»
	// (COALESCE(NULLIF(manual,0), NULLIF(plain,0)), см. EffectiveDiscount);
	// nil — скидка не задана. Telegram в состоянии дня не участвует: модуль
	// расчёта скидок дублирует тг-скидку в general (правило владельца).
	EffectiveGeneral *int16
}

// ErrDayNotFound — нет строки состояния за день.
var ErrDayNotFound = errors.New("нет строки состояния за день")

// InStockFromLots — есть ли хоть один лот с qty > 0.
func InStockFromLots(lots []LotState) bool {
	for _, l := range lots {
		if l.Qty > 0 {
			return true
		}
	}
	return false
}

// EffectiveDiscount — effective-скидка канала из пары колонок product_stock
// (ручная `_manual`, «просто»): COALESCE(manual, NULLIF(plain,0)).
//
// Ручная колонка (решение владельца, сентябрь 2026): заданное значение
// перекрывает plain КАК ЕСТЬ, включая 0 — «0 %» значит «скидка 0 %» и лестницу
// расчётных скидок по этой паре замораживает; nil — ручного применения нет.
// Plain-колонка: 0 — legacy-«скидки нет» (движок пишет NULL, но старые данные
// могут содержать ноль), поэтому ноль отбрасывается как незаданное значение и
// лестницу не перекрывает — было прежнее правило «0 = NULL» (владелец,
// 14.09.2026), инвертированное для ручных колонок.
// Скидка не задана ни в одной из колонок → nil.
//
// Зеркало SQL-правила daystate_repo.go (LotsSnapshot, SnapshotInsert): база —
// источник значения для состояния дня, функция — для Go-кода и тестов.
func EffectiveDiscount(manual, plain *int16) *int16 {
	if manual != nil {
		return manual
	}
	return nonZeroDiscount(plain)
}

// nonZeroDiscount — plain-скидка, если она задана и не ноль (0 = «скидки нет»);
// иначе nil. К РУЧНОЙ колонке не применяется: там ноль — заданная скидка 0 %.
func nonZeroDiscount(v *int16) *int16 {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

// DiscountFromLots — максимальная effective-скидка канала general по лотам;
// nil, если ни у одного лота скидка не задана. Заданный ноль участвует как
// значение (ручная 0 % — «скидка есть, 0 %»): лот с ручной 0 % и лот со
// ступенью 40 % дают 40 (решение владельца, сентябрь 2026; было «0 скидкой не
// считается», 14.09.2026). День хранит 0 как 0, а не NULL.
func DiscountFromLots(lots []LotState) *int16 {
	var top *int16
	for _, l := range lots {
		v := l.EffectiveGeneral
		if v == nil {
			continue
		}
		if top == nil || *v > *top {
			val := *v
			top = &val
		}
	}
	return top
}

// ApplyStockChange пересчитывает строку дня после изменения остатков.
// Возвращает обновлённую строку и два наблюдаемых перехода:
//   - soldOutNow — «было в наличии → стало нет»: ставит sold_out_today
//     и требует эмита SoldOut в ordercoeff;
//   - backInStock — «не было в наличии → появилось» (только уведомление,
//     маркер sold_out_today НЕ сбрасывается: «закончилась сегодня» — факт дня).
//
// Правила:
//   - in_stock — пересчёт из лотов (всегда известен после события);
//   - discount — новое значение max по лотам; повышение append'ится
//     в discount_increases, понижение/снятие не логируется;
//   - sold_out_today — сохраняется; устанавливается при переходе в 0,
//     НЕ сбрасывается приходом (сброс — только RollbackSoldOut);
//   - orderable и discount_start событие не трогает.
func ApplyStockChange(cur DayState, lots []LotState) (next DayState, soldOutNow, backInStock bool) {
	next = cur

	inStock := InStockFromLots(lots)
	next.InStock = &inStock
	if cur.InStock != nil && *cur.InStock && !inStock {
		next.SoldOutToday = true
		soldOutNow = true
	}
	if cur.InStock != nil && !*cur.InStock && inStock {
		backInStock = true
	}

	newDiscount := DiscountFromLots(lots)
	if isDiscountIncrease(cur.Discount, newDiscount) {
		next.DiscountIncreases = append(append([]int16(nil), cur.DiscountIncreases...), *newDiscount)
	}
	next.Discount = newDiscount

	return next, soldOutNow, backInStock
}

// isDiscountIncrease — повышение скидки: новое значение строго больше
// текущего. Текущего нет (NULL = скидка не задана) — повышением считается
// только значение > 0. Заданный ноль (ручная 0 %) — значение, но НЕ повышение:
// переход 40 % → 0 % в increases не логируется (решение владельца,
// сентябрь 2026: логируются только положительные повышения).
func isDiscountIncrease(cur, next *int16) bool {
	if next == nil {
		return false
	}
	if cur == nil {
		return *next > 0
	}
	return *next > *cur
}

// CatalogProduct — срез товара каталога для страниц состояния по дням
// (календарь доступности, отчёт по наличию).
type CatalogProduct struct {
	ID        string
	Name      string
	GroupName string
}

// CellKind — оформление ячейки «Отчёта по наличию».
type CellKind string

// Виды ячеек (правила владельца, 01.09.2026).
const (
	CellEmpty     CellKind = "empty"      // нет данных (будущая дата / NULL in_stock)
	CellPlain     CellKind = "plain"      // в наличии
	CellYellow    CellKind = "yellow"     // в наличии + скидка
	CellRed       CellKind = "red"        // закончилась ('x')
	CellYellowRed CellKind = "yellow-red" // скидка и позиция закончилась: жёлтая, шрифт красный
	CellGray      CellKind = "gray"       // недоступна для заказа
)

// ReportCell — ячейка отчёта: текст и оформление.
type ReportCell struct {
	Text string
	Kind CellKind
}

// CellFor вычисляет ячейку отчёта по строке дня (d nil — строки нет).
// Приоритет правил сверху вниз (владелец, 01.09.2026; уточнения 19.10.2026
// и 12.11.2026):
//  1. дата > сегодня → пустая (таблица дополняется по мере накопления данных);
//  2. !orderable (любое состояние) → серая: недоступность для заказа —
//     решение менеджера, а не состояние остатка, поэтому она видна и тогда,
//     когда о наличии по дню данных нет (строка создана календарём «Доступность»
//     с in_stock = NULL). Правило стоит ВЫШЕ «нет данных»: иначе бэктречная
//     недоступность за прошедший/сегодняшний день не показывалась вовсе
//     (красная 'x' за прошлый день перебивала её — дефект, 12.11.2026);
//  3. прошлый день без данных (строки нет / in_stock = NULL) → красная с 'x':
//     «позиции не было в наличии весь день» помечается так же, как «закончилась»
//     (решение владельца 19.10.2026). Строки нет у товара без лотов в
//     product_stock (списанный в ноль лот удаляется), NULL in_stock — день, до
//     которого утренний снапшот не дошёл. Сегодняшний день без данных остаётся
//     пустым: он ещё не сведён, красный 'x' там был бы враньём до снапшота;
//  4. !in_stock + скидка была в течение дня → жёлтая, шрифт красный:
//     «скидка → отсутствие»;
//  5. !in_stock → красная, текст 'x';
//  6. in_stock + sold_out + скидка была в течение дня → жёлтая, шрифт красный;
//  7. in_stock + sold_out → красная;
//  8. in_stock + скидка была в течение дня → жёлтая;
//  9. in_stock → серая (без скидки).
//
// «Скидка была в течение дня» — см. discountDuringDay: текущая, на начало дня
// ИЛИ любой шаг повышения. Только по discount/discount_start судить нельзя:
// скидка, поставленная после утреннего снапшота и снятая в тот же день, в
// них не попадает (discount_start = NULL, к концу дня discount = NULL) —
// остаётся лишь след в discount_increases, и клетка с текстом «20%» рисовалась
// без жёлтой заливки (дефект, 12.11.2026).
func CellFor(d *DayState, date, today time.Time) ReportCell {
	if date.After(today) {
		return ReportCell{Kind: CellEmpty}
	}
	if d != nil && !d.Orderable {
		return ReportCell{Kind: CellGray, Text: TextFor(d)}
	}
	if d == nil || d.InStock == nil {
		if date.Before(today) {
			return ReportCell{Kind: CellRed, Text: "x"}
		}
		return ReportCell{Kind: CellEmpty}
	}
	if !*d.InStock {
		if discountDuringDay(d) {
			return ReportCell{Kind: CellYellowRed, Text: TextFor(d)}
		}
		return ReportCell{Kind: CellRed, Text: TextFor(d)}
	}
	if d.SoldOutToday && discountDuringDay(d) {
		return ReportCell{Kind: CellYellowRed, Text: TextFor(d)}
	}
	if d.SoldOutToday {
		return ReportCell{Kind: CellRed, Text: TextFor(d)}
	}
	if discountDuringDay(d) {
		return ReportCell{Kind: CellYellow, Text: TextFor(d)}
	}
	return ReportCell{Kind: CellPlain, Text: TextFor(d)}
}

// discountDuringDay — скидка действовала хотя бы часть дня: стоит на начало
// (discount_start), поднималась в течение дня (discount_increases) или стоит
// сейчас (discount). След в increases — единственный признак скидки, которую
// поставили и сняли в один день после утреннего снапшота.
func discountDuringDay(d *DayState) bool {
	if d == nil {
		return false
	}
	return d.Discount != nil || d.DiscountStart != nil || len(d.DiscountIncreases) > 0
}

// TextFor — текст ячейки: скидка на начало → каждый шаг повышения → состояние
// конца дня ('x' / размер скидки, включая «0%» — скидка задана нулём).
// Повтор последнего значения не дублируется: не менялась скидка — остаётся
// финал. Ноль печатается и на начало дня, и в конце: явный 0 — ручной запрет
// расчёта, а не «скидки нет», и жёлтая клетка без значения читается как
// «скидка», хотя запрет её как раз и снял (владелец, 12.11.2026).
// Примеры: 10% → 15% → 20%; закончился → 10% → 15% → x; скидки нет — пусто;
// скидка задана нулём: 0%; снята в течение дня: 10% → 0%.
func TextFor(d *DayState) string {
	if d == nil {
		return ""
	}
	parts := make([]string, 0, len(d.DiscountIncreases)+2)
	add := func(v string) {
		if v == "" {
			return
		}
		if len(parts) > 0 && parts[len(parts)-1] == v {
			return
		}
		parts = append(parts, v)
	}
	// Ноль на начало дня тоже печатаем: явный 0 — ручной запрет расчёта, а не
	// «скидки нет» (иначе жёлтая клетка остаётся без значения).
	if d.DiscountStart != nil {
		add(fmt.Sprintf("%d%%", *d.DiscountStart))
	}
	for _, v := range d.DiscountIncreases {
		if v != 0 {
			add(fmt.Sprintf("%d%%", v))
		}
	}
	switch {
	case d.InStock != nil && !*d.InStock:
		// Явный ноль (ручной запрет) — тоже состояние дня: печатаем перед уходом
		// в ноль, иначе жёлтая клетка остаётся без значения.
		if d.Discount != nil && *d.Discount == 0 {
			add(fmt.Sprintf("%d%%", *d.Discount))
		}
		add("x")
	case d.Discount != nil:
		// Ноль печатается: явный 0 — это ручной запрет расчёта, а не «скидки нет».
		add(fmt.Sprintf("%d%%", *d.Discount))
	}

	return strings.Join(parts, " → ")
}

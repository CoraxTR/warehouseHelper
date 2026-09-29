// Реестр активных пар: снапшот последнего расчёта живёт в памяти процесса.
//
// Модуль не владеет значениями скидок (их пишет шов модуля «Сроки»), но держит
// снапшот состояния пар, чтобы отвечать на вопросы «что сейчас в окне», «что
// в очереди» и «что печатать в дайджест», не перечитывая БД. Здесь же считается
// изменение скидки ПОЗИЦИИ (максимум эффективного значения по её лотам)
// относительно прошлого расчёта — из него рождаются уведомления людям
// (notify.go), по одной задаче на товар: при неизменной скидке позиции
// изменений нет, значит и уведомления не летят.
//
// Реестр защищён своим мутексом: его читают обработчики страницы «Скидки» и
// ТГ-команды параллельно с тиками расчёта.
package usecase

import (
	"slices"
	"sync"
	"time"

	"warehouseHelper/internal/discounts"
)

// prevPair — пара предыдущего снапшота: имя товара и эффективное значение для
// сравнения скидки позиции. Срок лота лежит в ключе карты (discounts.LotKey),
// поэтому отдельным полем он не хранится.
type prevPair struct {
	name    string
	applied *int16
}

// Registry — снапшот активных пар последнего расчёта.
type Registry struct {
	mu sync.Mutex
	// snap — пары последнего расчёта: все источники (ручной, срок, избыток),
	// не только верх окна.
	snap []PairState
	// prev — эффективные значения канала сайта по парам предыдущего снапшота,
	// ключ — лот (товар + срок): с ними сравнивается новый расчёт.
	prev map[discounts.LotKey]prevPair
	// seeded — реестр получил первый снапшот процесса. Первый снапшот только
	// заполняет базу сравнения и уведомлений не даёт: иначе после каждого
	// старта в чат уходил бы залп «поставьте скидку» по позициям, которые
	// человек и так видит на сайте (а после перезапуска посреди дня — ещё раз).
	seeded bool
}

// NewRegistry — пустой реестр (расчёта ещё не было).
func NewRegistry() *Registry {
	return &Registry{prev: make(map[discounts.LotKey]prevPair)}
}

// Replace — положить новый снапшот пар и вернуть изменения СКИДКИ ПОЗИЦИИ
// относительно предыдущего снапшота (то, о чём надо уведомить).
//
// Скидка позиции — максимум по её лотам эффективной скидки канала сайта
// (PairState.Applied), ровно как discounts.DiscountFromLots считает скидку дня
// по лотам: человеку на сайте надо выставить одно значение на все сроки
// товара. Поэтому сравнение идёт по товару, а не по паре (товар, срок), и
// запись Change одна на позицию (решение владельца, 29.09.2026).
//
// Значения сравниваются нормализованно: nil и 0 равнозначны «скидки нет», как в
// discounts.Resolve и notify.go. С новым правилом ручной скидки (0 = «скидка
// 0 %», решение владельца, сентябрь 2026) это остаётся верным: ручная 0 и
// отсутствие ручной дают на сайте одну и ту же скидку 0 %, поэтому «ручную
// поставили нулём» и «ручную сняли» уведомлений не дают, а вот переход
// 40 % → ручная 0 % уведомляет «убрать скидку».
//
// Максимум позиции не изменился — записи нет вовсе: выход одного срока из
// избытка при том, что другой срок держит то же значение, уведомлять не о чем
// (скидка позиции на сайте та же). Порядок изменений стабилен — по ProductID:
// текст уведомлений не зависит от порядка обхода карт.
func (r *Registry) Replace(pairs []PairState) []Change {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[discounts.LotKey]prevPair, len(pairs))
	for _, p := range pairs {
		next[p.Key] = prevPair{name: p.Name, applied: p.Applied}
	}

	// Первый снапшот процесса только заполняет базу сравнения: уведомлений он
	// не даёт (иначе после каждого старта в чат уходил бы залп «поставьте
	// скидку» по позициям, которые человек и так видит на сайте).
	var changes []Change
	if r.seeded {
		changes = comparePositions(pairs, r.prev)
	}

	r.snap = append([]PairState(nil), pairs...)
	r.prev = next
	r.seeded = true

	return changes
}

// positionAgg — агрегат скидки позиции: максимальное эффективное значение
// канала сайта по её лотам и сроки лотов, дающих это значение.
type positionAgg struct {
	name  string
	top   int16
	dates []time.Time
}

// comparePositions — изменения скидки позиции: новый снапшот пар против
// предыдущего. prevMax и nextMax берутся по ВСЕМ лотам товара (и те, что ушли
// из расчёта, тоже — их скидка с сайта снимается вместе с парой), равные
// значения записи не дают: задача открывается только там, где скидка позиции
// на сайте реально меняется.
func comparePositions(pairs []PairState, prev map[discounts.LotKey]prevPair) []Change {
	next := aggregatePairs(pairs)
	old := aggregatePrev(prev)

	// Порядок обхода карт случаен, а порядок уведомлений — нет: идём по
	// отсортированным ProductID, включая товары, которые есть только в одном
	// из снапшотов.
	ids := make([]string, 0, len(next)+len(old))
	for pid := range next {
		ids = append(ids, pid)
	}
	for pid := range old {
		if _, ok := next[pid]; !ok {
			ids = append(ids, pid)
		}
	}
	slices.Sort(ids)

	changes := make([]Change, 0, len(ids))
	for _, pid := range ids {
		was := old[pid] // отсутствие товара в снапшоте — нулевой агрегат (prevMax = 0)
		now, hasNew := next[pid]
		if pct(was.top) == pct(now.top) {
			continue // скидка позиции не изменилась
		}
		// Имя — из нового снапшота: товар, ушедший из расчёта (все его пары
		// исчезли), берёт имя из предыдущего.
		name := now.name
		if !hasNew {
			name = was.name
		}
		// «Убрать скидку» подсказывает сроки, где скидка была; остальные
		// действия — сроки, которые получают новое значение.
		dates := now.dates
		if pct(now.top) == 0 {
			dates = was.dates
		}
		changes = append(changes, Change{
			ProductID: pid,
			Name:      name,
			Dates:     sortedDates(dates),
			Prev:      percentPtr(was.top),
			Next:      percentPtr(now.top),
		})
	}

	return changes
}

// aggregatePairs — скидка позиции по новому снапшоту пар: максимум эффективного
// значения по лотам и сроки лотов с этим значением.
func aggregatePairs(pairs []PairState) map[string]positionAgg {
	out := make(map[string]positionAgg, len(pairs))
	for _, p := range pairs {
		a := out[p.ProductID]
		a.name = p.Name
		a = addLot(a, p.BestBefore, p.Applied)
		out[p.ProductID] = a
	}
	return out
}

// aggregatePrev — то же по предыдущему снапшоту: ключ карты — лот, товар
// собирается по лотам (срок лота лежит в ключе, поэтому отдельный prevPair
// .bestBefore здесь не нужен).
func aggregatePrev(prev map[discounts.LotKey]prevPair) map[string]positionAgg {
	out := make(map[string]positionAgg, len(prev))
	for key, old := range prev {
		a := out[key.ProductID]
		a.name = old.name
		a = addLot(a, key.BestBefore, old.applied)
		out[key.ProductID] = a
	}
	return out
}

// addLot добавляет лот позиции в агрегат: значение выше максимума — новый
// максимум и только его срок; значение равно максимуму — срок добавляется к
// списку (позиция со скидкой на двух сроках); ниже — лот для текста не важен.
func addLot(a positionAgg, date time.Time, applied *int16) positionAgg {
	v := discountPercent(applied)
	switch {
	case len(a.dates) == 0 && a.top == 0:
		a.top, a.dates = v, []time.Time{date}
	case v > a.top:
		a.top, a.dates = v, []time.Time{date}
	case v == a.top:
		a.dates = append(a.dates, date)
	}
	return a
}

// pct — значение агрегата для сравнения: 0 и отсутствие значения — одно и то же
// «скидки нет».
func pct(v int16) int16 {
	return discountPercent(&v)
}

// percentPtr — нормализованное значение для Change: 0 (и NULL, и ручная 0 %)
// хранится как nil — сравнение в NotifyText всё равно идёт по discountPercent.
func percentPtr(v int16) *int16 {
	if v <= 0 {
		return nil
	}
	return &v
}

// sortedDates — сроки текста по возрастанию даты: в одной позиции сортировка
// нужна и потому, что предыдущий снапшот собирается обходом карты лотов.
func sortedDates(dates []time.Time) []time.Time {
	if len(dates) == 0 {
		return nil
	}
	out := append([]time.Time(nil), dates...)
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

// Window — верх реестра: активные строки (Desired() != nil) в порядке
// discounts.Sort, не больше n. n <= 0 — все активные строки. Слоты не
// добиваем: строк меньше n — список короче (пустые слоты рисует страница).
func (r *Registry) Window(n int) []discounts.Row {
	r.mu.Lock()
	defer r.mu.Unlock()

	return windowOf(r.activeLocked(), n)
}

// Queue — избыточные пары за пределами окна (блок «В очереди» страницы
// «Скидки»): активные строки с источником «избыток», которых нет среди первых
// windowSize активных строк. Ёмкость окна реестру неизвестна — её передаёт
// вызывающий. windowSize <= 0 — окно вмещает всё, очередь пуста.
func (r *Registry) Queue(windowSize int) []discounts.Row {
	r.mu.Lock()
	defer r.mu.Unlock()

	var queue []discounts.Row
	for _, row := range beyondWindow(r.activeLocked(), windowSize) {
		if row.Source == discounts.SourceSurplus {
			queue = append(queue, row)
		}
	}
	return queue
}

// ActiveLots — пары активных позиций окна (не больше capacity): страница «Сроки»
// отличает по ним скидки, выставленные на сайте, от «дополнительных» — у
// последних подсветка остаётся, а значение видно только в карточке количества
// (решение владельца 23.09.2026). Группа избытка раскрывается во все свои сроки:
// позиция в окне одна, а пар в ней — по числу сроков.
func (r *Registry) ActiveLots(capacity int) []discounts.LotKey {
	r.mu.Lock()
	defer r.mu.Unlock()

	return activeLots(r.activeLocked(), capacity)
}

// activeLots — ключи пар активных позиций: обычная строка даёт один ключ, строка
// группы — по ключу на каждый её срок.
func activeLots(rows []discounts.Row, capacity int) []discounts.LotKey {
	active := discounts.BuildDigest(rows, capacity).Discounts
	keys := make([]discounts.LotKey, 0, len(active))
	for _, row := range active {
		if len(row.Dates) == 0 {
			keys = append(keys, discounts.LotKey{ProductID: row.ProductID, BestBefore: row.BestBefore})
			continue
		}
		for _, dt := range row.Dates {
			keys = append(keys, discounts.LotKey{ProductID: row.ProductID, BestBefore: dt})
		}
	}

	return keys
}

// Digest — отчёт по СТОЯЩИМ скидкам реестра (см. standingLocked). capacity —
// ёмкость активных скидок (окно): в первую секцию входит не больше capacity
// позиций по приоритету, остальное уходит в «доступно для допродажи».
// capacity <= 0 — ёмкость не ограничена. Дата отчёта — now (своих часов реестр
// не заводит).
func (r *Registry) Digest(now time.Time, capacity int) discounts.Digest {
	r.mu.Lock()
	defer r.mu.Unlock()

	d := discounts.BuildDigest(r.standingLocked(), capacity)
	d.Date = now

	return d
}

// Window — верх окна реестра (см. Registry.Window).
func (uc *UseCase) Window(n int) []discounts.Row {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	return uc.reg.Window(n)
}

// Queue — избыточные пары за пределами окна ёмкости windowSize.
func (uc *UseCase) Queue(windowSize int) []discounts.Row {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	return uc.reg.Queue(windowSize)
}

// ActiveLots — пары активных позиций окна (см. Registry.ActiveLots).
func (uc *UseCase) ActiveLots(capacity int) []discounts.LotKey {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	return uc.reg.ActiveLots(capacity)
}

// Digest — отчёт по реестру на текущий момент процесса (часы uc.now); capacity —
// ёмкость активных скидок (окно), как в Registry.Digest.
func (uc *UseCase) Digest(capacity int) discounts.Digest {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	return uc.reg.Digest(uc.now(), capacity)
}

// activeLocked — активные строки снапшота в порядке окна (под мутексом реестра).
// Скидки-значения нет (Percent == nil) или она нулевая — пары в окне и отчёте
// нет: замороженная ручным нулём пара (0 %) тоже не активна, дайджест и план по
// ней молчат (решение владельца, сентябрь 2026).
func (r *Registry) activeLocked() []discounts.Row {
	rows := make([]discounts.Row, 0, len(r.snap))
	for _, p := range r.snap {
		percent, _ := p.windowResolve()
		if percent == nil || *percent <= 0 {
			continue // без скидки и замороженные ручным нулём: в окне и отчёте их нет
		}
		rows = append(rows, p.Row())
	}
	discounts.Sort(rows)

	return rows
}

// standingLocked — строки отчёта по ФАКТУ (под мутексом реестра): скидка стоит
// в канале сайта или уже ушла в рассылку (ТГ-колонка). Скидка дня, которую
// применит только план 14:00, в отчёт не попадает: до 14:00 в дайджесте
// актуальны скидки вчерашнего дня, новые встают вместе с рассылкой (решение
// владельца 29.09.2026) — дайджест читают менеджеры по продажам, и заранее
// видеть скидку, которая встанет только днём, им незачем.
//
// Замороженная ручным нулём пара (0 % — в том числе пара под каскадным запретом
// менеджера) не попадает в отчёт: скидки по ней не будет.
//
// Окно страницы «Скидки», очередь допродажи и подсветка «Сроков» считаются
// по-прежнему по кандидатам дня (activeLocked): там вопрос «что в работе»,
// а не «что стоит».
func (r *Registry) standingLocked() []discounts.Row {
	rows := make([]discounts.Row, 0, len(r.snap))
	for _, p := range r.snap {
		if p.Frozen() {
			continue
		}
		row := p.AppliedRow()
		if row.Source == discounts.SourceNone || row.Percent <= 0 {
			continue // скидки нет ни на сайте, ни в рассылке
		}
		rows = append(rows, row)
	}
	discounts.Sort(rows)

	return rows
}

// windowOf — первые n строк среза; n <= 0 — весь срез (окно вмещает всё).
func windowOf(rows []discounts.Row, n int) []discounts.Row {
	if n > 0 && n < len(rows) {
		return rows[:n]
	}
	return rows
}

// beyondWindow — строки за пределами окна ёмкости n. n <= 0 (окно вмещает всё)
// или строк не больше n — за окном пусто.
func beyondWindow(rows []discounts.Row, n int) []discounts.Row {
	if n <= 0 || n >= len(rows) {
		return nil
	}
	return rows[n:]
}

// sameDiscount — значения скидки равнозначны: nil и 0 — одно и то же «скидки
// нет» (та же трактовка, что в discounts.Resolve для plain-источников и в
// notify.go). Движок в plain-колонку пишет NULL, а не 0, поэтому ноль там —
// всегда «нет». Ручная 0 (новое правило, сентябрь 2026) приходит как значение
// 0 — для сравнения на сайте это та же скидка 0 %, что и отсутствие значения.
func sameDiscount(a, b *int16) bool {
	return discountPercent(a) == discountPercent(b)
}

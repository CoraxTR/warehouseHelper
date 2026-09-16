// Реестр активных пар: снапшот последнего расчёта живёт в памяти процесса.
//
// Модуль не владеет значениями скидок (их пишет шов модуля «Сроки»), но держит
// снапшот состояния пар, чтобы отвечать на вопросы «что сейчас в окне», «что
// в очереди» и «что печатать в дайджест», не перечитывая БД. Здесь же считается
// изменение эффективной скидки канала сайта относительно прошлого расчёта — из
// него рождаются уведомления людям (notify.go): при неизменной скидке
// изменений нет, значит и уведомления не летят.
//
// Реестр защищён своим мутексом: его читают обработчики страницы «Скидки» и
// ТГ-команды параллельно с тиками расчёта.
package usecase

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"warehouseHelper/internal/discounts"
)

// prevPair — пара предыдущего снапшота: эффективное значение для сравнения и
// подпись (имя, срок) для уведомления об исчезнувшей паре.
type prevPair struct {
	name       string
	bestBefore time.Time
	applied    *int16
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

// Replace — положить новый снапшот пар и вернуть изменения эффективной скидки
// канала сайта относительно предыдущего снапшота (то, о чём надо уведомить).
//
// Ключ сравнения — лот (товар + срок): пары, которой раньше не было, приходит
// prev = nil; исчезнувшая пара даёт next = nil. Сравниваются ЭФФЕКТИВНЫЕ
// значения (PairState.Applied), причём nil и 0 равнозначны «скидки нет»:
// при неизменной скидке изменения не рождаются. С новым правилом ручной
// скидки (0 = «скидка 0 %», решение владельца, сентябрь 2026) это остаётся
// верным: ручная 0 и отсутствие ручной дают на сайте одну и ту же скидку 0 %,
// поэтому «ручную поставили нулём» и «ручную сняли» уведомлений не дают, а вот
// переход 40 % → ручная 0 % уведомляет «убрать скидку». Порядок изменений
// стабилен (ProductID, затем срок) — текст уведомлений не зависит от порядка
// обхода карт.
func (r *Registry) Replace(pairs []PairState) []Change {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[discounts.LotKey]prevPair, len(pairs))
	for _, p := range pairs {
		next[p.Key] = prevPair{name: p.Name, bestBefore: p.BestBefore, applied: p.Applied}
	}

	changes := make([]Change, 0, len(pairs))
	for _, p := range pairs {
		if !r.seeded {
			break // первый снапшот: только заполняем базу сравнения
		}
		var prev *int16
		if old, ok := r.prev[p.Key]; ok {
			prev = old.applied
		}
		if sameDiscount(prev, p.Applied) {
			continue
		}
		changes = append(changes, Change{
			ProductID:  p.ProductID,
			Name:       p.Name,
			BestBefore: p.BestBefore,
			Prev:       prev,
			Next:       p.Applied,
		})
	}
	// Исчезнувшие пары: скидка пары уходит вместе с парой (next = nil).
	for key, old := range r.prev {
		if !r.seeded {
			break
		}
		if _, ok := next[key]; ok {
			continue
		}
		if sameDiscount(old.applied, nil) {
			continue // скидки и не было — уведомлять нечего
		}
		changes = append(changes, Change{
			ProductID:  key.ProductID,
			Name:       old.name,
			BestBefore: old.bestBefore,
			Prev:       old.applied,
		})
	}
	sortChanges(changes)

	r.snap = append([]PairState(nil), pairs...)
	r.prev = next
	r.seeded = true

	return changes
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

// Digest — отчёт по ВСЕМ активным строкам реестра: ручные и сроковые идут в
// секцию «в скидках», избыточные — в секцию избытка (discounts.BuildDigest).
// Дата отчёта — now (своих часов реестр не заводит).
func (r *Registry) Digest(now time.Time) discounts.Digest {
	r.mu.Lock()
	defer r.mu.Unlock()

	d := discounts.BuildDigest(r.activeLocked())
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

// Digest — отчёт по реестру на текущий момент процесса (часы uc.now).
func (uc *UseCase) Digest() discounts.Digest {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	return uc.reg.Digest(uc.now())
}

// activeLocked — активные строки снапшота в порядке окна (под мутексом реестра).
// Скидки-значения нет (Percent == nil) или она нулевая — пары в окне и отчёте
// нет: замороженная ручным нулём пара (0 %) тоже не активна, дайджест и план по
// ней молчат (решение владельца, сентябрь 2026).
func (r *Registry) activeLocked() []discounts.Row {
	rows := make([]discounts.Row, 0, len(r.snap))
	for _, p := range r.snap {
		percent, _ := p.Desired()
		if percent == nil || *percent <= 0 {
			continue // пары без скидки и замороженные ручным нулём: в окне и отчёте их нет
		}
		rows = append(rows, p.Row())
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

// sortChanges — стабильный порядок изменений: товар, затем срок.
func sortChanges(changes []Change) {
	slices.SortStableFunc(changes, func(a, b Change) int {
		if c := cmp.Compare(a.ProductID, b.ProductID); c != 0 {
			return c
		}
		return a.BestBefore.Compare(b.BestBefore)
	})
}

// sameDiscount — значения скидки равнозначны: nil и 0 — одно и то же «скидки
// нет» (та же трактовка, что в discounts.Resolve для plain-источников и в
// notify.go). Движок в plain-колонку пишет NULL, а не 0, поэтому ноль там —
// всегда «нет». Ручная 0 (новое правило, сентябрь 2026) приходит как значение
// 0 — для сравнения на сайте это та же скидка 0 %, что и отсутствие значения.
func sameDiscount(a, b *int16) bool {
	return discountPercent(a) == discountPercent(b)
}

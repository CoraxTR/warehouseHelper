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
	"sort"
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
// значения (PairState.Applied), причём nil и 0 равнозначны «скидки нет»: при
// неизменной скидке изменения не рождаются. Порядок изменений стабилен
// (ProductID, затем срок) — текст уведомлений не зависит от порядка обхода карт.
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

// ReplaceProduct — обновить в снапшоте пары ОДНОГО товара: шов событий стока
// сообщает о лоте вне расчётного тика (ручная скидка со страницы «Сроки»),
// пары остальных товаров не трогаются. Изменения считаются только по этому
// товару: чужие пары сравнивать незачем (их значения те же), а исчезнувшая
// последняя пара товара даёт изменение «скидка → нет».
func (r *Registry) ReplaceProduct(productID string, pairs []PairState) []Change {
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := make([]PairState, 0, len(r.snap))
	for _, p := range r.snap {
		if p.ProductID != productID {
			kept = append(kept, p)
		}
	}
	next := append(kept, pairs...)

	// Первый снапшот процесса только закладывает базу сравнения (seeded):
	// уведомлять о состоянии, которое человек и так видит на сайте, не нужно.
	var changes []Change
	if r.seeded {
		changes = make([]Change, 0, len(pairs))
		fresh := make(map[discounts.LotKey]struct{}, len(pairs))
		for _, p := range pairs {
			fresh[p.Key] = struct{}{}
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
		// Пары товара, которых больше нет: скидка уходит вместе с парой.
		for key, old := range r.prev {
			if key.ProductID != productID {
				continue
			}
			if _, ok := fresh[key]; ok {
				continue
			}
			if sameDiscount(old.applied, nil) {
				continue
			}
			changes = append(changes, Change{
				ProductID:  key.ProductID,
				Name:       old.name,
				BestBefore: old.bestBefore,
				Prev:       old.applied,
			})
		}
		sortChanges(changes)
	}

	r.snap = next
	r.prev = make(map[discounts.LotKey]prevPair, len(next))
	for _, p := range next {
		r.prev[p.Key] = prevPair{name: p.Name, bestBefore: p.BestBefore, applied: p.Applied}
	}
	r.seeded = true

	return changes
}

// activeLocked — активные строки снапшота в порядке окна (под мутексом реестра).
func (r *Registry) activeLocked() []discounts.Row {
	rows := make([]discounts.Row, 0, len(r.snap))
	for _, p := range r.snap {
		if percent, _ := p.Desired(); percent == nil {
			continue // пара без скидки: в окне и отчёте её нет
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
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].ProductID != changes[j].ProductID {
			return changes[i].ProductID < changes[j].ProductID
		}
		return changes[i].BestBefore.Before(changes[j].BestBefore)
	})
}

// sameDiscount — значения скидки равнозначны: nil и 0 — одно и то же «скидки
// нет» (та же трактовка, что в discounts.Resolve и notify.go).
func sameDiscount(a, b *int16) bool {
	return discountPercent(a) == discountPercent(b)
}

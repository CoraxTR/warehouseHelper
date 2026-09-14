// Пакет usecase — сценарии модуля расчёта скидок: утренний пересчёт по сроку,
// часовой пересчёт избытка, реестр активных пар и выводы наружу (ТГ-слот,
// дайджест, уведомления, страница «Скидки»).
//
// Модуль сам считает значения, но не владеет ими: запись идёт через шов
// DiscountWriter (модуль «Сроки»), данные для расчёта — через Repository,
// обороты — через Turnover (модуль средних продаж). Порты — ports.go,
// состояния пар — evaluate.go.
package usecase

import (
	"sync"
	"time"
)

// UseCase — сценарии модуля скидок.
//
// now инжектируется (тесты без реальных часов); состояние расчёта (реестр
// активных пар и список товаров, которым нужен свежий оборот) живёт в
// памяти процесса и защищено mu.
type UseCase struct {
	repo      Repository
	turnover  Turnover
	writer    DiscountWriter
	common    CommonNotifier
	warehouse WarehouseNotifier
	now       func() time.Time

	mu sync.Mutex
	// reg — реестр активных пар: снапшот последнего расчёта (верх окна) и
	// предыдущие эффективные значения для уведомлений (registry.go).
	reg *Registry
	// dirty — товары, по которым расчёт просит свежий оборот: события стока
	// (приёмка изменила накопленный остаток, подбор, ручная скидка) и новые
	// избытки, найденные в этом же тике.
	dirty map[string]struct{}
}

// NewUseCase собирает сценарии модуля. Все зависимости — швы (ports.go);
// связка с реализациями — в di.go. now — часы процесса (nil → time.Now).
func NewUseCase(
	repo Repository,
	turnover Turnover,
	writer DiscountWriter,
	common CommonNotifier,
	warehouse WarehouseNotifier,
	now func() time.Time,
) *UseCase {
	if now == nil {
		now = time.Now
	}
	return &UseCase{
		repo:      repo,
		turnover:  turnover,
		writer:    writer,
		common:    common,
		warehouse: warehouse,
		now:       now,
		reg:       NewRegistry(),
		dirty:     make(map[string]struct{}),
	}
}

// MarkDirty отмечает товары, по которым нужен свежий оборот: шов стока
// (OnLotsChanged) сообщает о событии — приёмка увеличила накопленный остаток,
// подбор или смена ручной скидки поменяли состояние пары. Вызывается из
// обработчика события; следующий тик избытка обновит оборот по этим товарам.
func (uc *UseCase) MarkDirty(productIDs ...string) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	for _, pid := range productIDs {
		if pid == "" {
			continue
		}
		uc.dirty[pid] = struct{}{}
	}
}

// takeDirty забирает и очищает список товаров, которым нужен свежий оборот.
func (uc *UseCase) takeDirty() []string {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	if len(uc.dirty) == 0 {
		return nil
	}
	ids := make([]string, 0, len(uc.dirty))
	for pid := range uc.dirty {
		ids = append(ids, pid)
	}
	uc.dirty = make(map[string]struct{})

	return ids
}

// Change — изменение эффективной скидки канала сайта по паре: то, о чём надо
// уведомить человека (тип сообщения определяет notify.go по prev/next).
type Change struct {
	ProductID  string
	Name       string
	BestBefore time.Time
	Prev       *int16
	Next       *int16
}

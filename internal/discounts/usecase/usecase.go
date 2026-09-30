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
	tasks     TaskOpener
	warehouse WarehouseNotifier
	now       func() time.Time

	mu sync.Mutex
	// windowCap — ёмкость активных скидок (окно сайта): сколько позиций входит
	// в секцию «Позиции в скидках» дайджеста, остальное — «доступно для
	// допродажи». Ставится связкой из настроек (di.go), 0 — ёмкость не
	// ограничена. Читать и писать — под mu (SetWindowCap/WindowCap).
	windowCap int
	// reg — реестр активных пар: снапшот последнего расчёта (верх окна) и
	// предыдущие эффективные значения для уведомлений (registry.go).
	// Маркеры расписания в памяти процесса: чтобы не дёргать проверки каждую
	// минуту. Потеря при рестарте безопасна — шаги идемпотентны и проверяют
	// маркеры дня в БД.
	lastSurplusHour time.Time // час последнего пересчёта избытка
	lastPlanDay     time.Time // день последнего плана ТГ-слота
	lastRaiseDay    time.Time // день последнего подъёма general

	reg *Registry
	// recalcMu — сериализует точечный пересчёт (RecalcAffected): его зовут и тик
	// расписания, и ручная правка скидки со страницы «Сроки» (29.09.2026), а оба
	// пишут снапшот реестра и БД.
	recalcMu sync.Mutex
	// dirty — товары, по которым расчёт просит свежий оборот: события стока
	// (приёмка изменила накопленный остаток, подбор, ручная скидка) и новые
	// избытки, найденные в этом же тике. Значение — вырос ли по товару остаток:
	// по этому признаку событийный пересчёт вправе заполнить ПУСТОЕ место
	// ступенью по сроку вне КТ-дней (см. MarkGrown и RecalcAffected).
	dirty map[string]bool
}

// NewUseCase собирает сценарии модуля. Все зависимости — швы (ports.go);
// связка с реализациями — в di.go. now — часы процесса (nil → time.Now).
func NewUseCase(
	repo Repository,
	turnover Turnover,
	writer DiscountWriter,
	common CommonNotifier,
	tasks TaskOpener,
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
		tasks:     tasks,
		warehouse: warehouse,
		now:       now,
		reg:       NewRegistry(),
		dirty:     make(map[string]bool),
	}
}

// Now — часы процесса, инжектированные в NewUseCase: страница «Скидки» берёт
// время у модуля, а не из своих часов, — иначе время шапки и время расчёта
// расходились бы на тестах и на прогоне с подменёнными часами.
func (uc *UseCase) Now() time.Time {
	return uc.now()
}

// SetWindowCap — ёмкость активных скидок (окно сайта) из настроек: её знает
// связка (di.go). Дайджест делит позиции по ней: сколько влезло — активные,
// остальное — «доступно для допродажи» (решение владельца 23.09.2026).
func (uc *UseCase) SetWindowCap(capacity int) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	uc.windowCap = capacity
}

// WindowCap — действующая ёмкость активных скидок (0 — не ограничена).
func (uc *UseCase) WindowCap() int {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	return uc.windowCap
}

// MarkDirty отмечает товары, по которым нужен свежий оборот: шов стока
// (OnLotsChanged) сообщает о событии — приёмка увеличила накопленный остаток,
// подбор или смена ручной скидки поменяли состояние пары. Вызывается из
// обработчика события; следующий тик избытка обновит оборот по этим товарам.
//
// Метка БЕЗ роста: ступень по сроку на пустое место событие не ставит — её
// ставит только MarkGrown (решение владельца 30.09.2026).
func (uc *UseCase) MarkDirty(productIDs ...string) {
	for _, pid := range productIDs {
		uc.markDirty(pid, false)
	}
}

// MarkGrown — событие стока с РОСТОМ остатка (приёмка, возврат лота в остатки):
// товар получает и свежий оборот, и право заполнить пустое место ступенью по
// сроку вне КТ-дней. Подбор остаток списывает — роста нет, поэтому кладовщик,
// подобравший заказ, скидку на сайте не получает (решение владельца
// 30.09.2026: среда, подбор соуса, «Поставить скидку 40%» без повода).
func (uc *UseCase) MarkGrown(productIDs ...string) {
	for _, pid := range productIDs {
		uc.markDirty(pid, true)
	}
}

// markDirty ставит метку товара; grew — остаток вырос. Повторная метка в одном
// окне роста не сбрасывает: товар могли и подобрать, и принять — рост есть.
func (uc *UseCase) markDirty(productID string, grew bool) {
	if productID == "" {
		return
	}
	uc.mu.Lock()
	defer uc.mu.Unlock()

	uc.dirty[productID] = uc.dirty[productID] || grew
}

// markAll возвращает метки на место после неудачного прохода: событие не должно
// потеряться из-за разового сбоя (признак роста возвращается вместе с товаром).
func (uc *UseCase) markAll(events map[string]bool) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	for pid, grew := range events {
		uc.dirty[pid] = uc.dirty[pid] || grew
	}
}

// takeDirty забирает и очищает метки событий стока: товары и признак роста по
// каждому (true — остаток вырос).
func (uc *UseCase) takeDirty() map[string]bool {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	if len(uc.dirty) == 0 {
		return nil
	}
	out := uc.dirty
	uc.dirty = make(map[string]bool)

	return out
}

// Change — изменение скидки ПОЗИЦИИ: то, о чём надо уведомить человека
// (тип сообщения определяет notify.go по prev/next). Одна запись на товар, а не
// на пару (товар, срок): человеку на сайте надо поставить одно значение скидки
// на все сроки позиции, и одно сообщение на позицию читается как одно действие
// (решение владельца, 29.09.2026). Прежнее правило «запись на пару» давало два
// сообщения на товар, когда скидка вставала на оба срока.
//
//   - Dates — сроки для текста: при Next > 0 это сроки лотов, дающих новое
//     значение, при Next == 0 («убрать скидку») — сроки лотов, где скидка была;
//     по возрастанию даты;
//   - Prev/Next — максимальные эффективные значения канала сайта по лотам
//     позиции до и после расчёта; nil — «скидки нет» (0 и NULL равнозначны).
type Change struct {
	ProductID string
	Name      string
	Dates     []time.Time
	Prev      *int16
	Next      *int16
}

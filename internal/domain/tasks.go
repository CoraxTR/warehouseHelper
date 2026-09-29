package domain

import "time"

// Внутренние задачи склада — уведомления общего канала, по которым сотрудник
// делает действие на сайте («убрать с сайта», «поставить скидку»), и отметка
// «кто выполнил». Словарь видов задачи живёт в domain, потому что его
// используют и производители уведомлений (daystate — наличие, discounts —
// скидки), и модуль задач: пакет задач не импортирует чужие usecase, а
// производители не импортируют модуль задач.
//
// Кнопка «✅ Отметить» вешается ТОЛЬКО на эти виды: складские уведомления
// (чат склада) в задачах не участвуют, дайджест 09:00 и карточки жалоб —
// не задачи (решение владельца 25.09.2026).
type TaskKind string

const (
	// TaskKindStockOut — товар закончился, позицию надо убрать с сайта.
	TaskKindStockOut TaskKind = "stock_out"
	// TaskKindStockIn — товар появился, позицию надо вернуть на сайт.
	TaskKindStockIn TaskKind = "stock_in"
	// TaskKindDiscountPut — скидки не было, её надо поставить на сайте.
	TaskKindDiscountPut TaskKind = "discount_put"
	// TaskKindDiscountRaise — скидку на сайте надо поднять.
	TaskKindDiscountRaise TaskKind = "discount_raise"
	// TaskKindDiscountLower — скидку на сайте надо понизить.
	TaskKindDiscountLower TaskKind = "discount_lower"
	// TaskKindDiscountRemove — скидку с сайта надо убрать.
	TaskKindDiscountRemove TaskKind = "discount_remove"
	// TaskKindSiteDiscount — сверка с фидом сайта: скидку на сайте не поменяли
	// (на сайте стоит не то значение, что в базе). Модуль «Проверка сайта».
	TaskKindSiteDiscount TaskKind = "site_discount"
	// TaskKindSiteOffer — сверка с фидом сайта: позиции нет в торговом
	// предложении (available="false") — её надо туда добавить.
	TaskKindSiteOffer TaskKind = "site_offer"
	// TaskKindSiteReturn — сверка с фидом сайта: позиция есть по факту, а в фиде
	// её нет — на сайт не вернули. Модуль «Проверка сайта».
	TaskKindSiteReturn TaskKind = "site_return"
	// TaskKindSiteRemove — сверка с фидом сайта: позиция есть в фиде, а по факту
	// её нет — с сайта не убрали. Модуль «Проверка сайта».
	TaskKindSiteRemove TaskKind = "site_remove"
)

// Task — задача из уведомления общего канала: текст, который увидели в чате,
// и отметка выполнения. Задача не несёт сроков и ответственного: её закрывает
// тот, кто нажал «✅» в телеграме (кто именно — фиксирует отметка).
type Task struct {
	ID        int64
	Kind      TaskKind
	Text      string     // текст уведомления, как он ушёл в чат
	CreatedAt time.Time  // когда уведомление ушло (ставит БД)
	DoneAt    *time.Time // nil — ещё не отмечена
	DoneBy    string     // ФИО на момент отметки (снимок: сотрудника могут удалить)
}

// Employee — сотрудник склада для кнопок «кто отметил»: пара ФИО - должность.
type Employee struct {
	ID       int64
	FullName string
	Position string
}

// TGButton — inline-кнопка сообщения бота. Тип живёт в domain, чтобы шов
// нижнего слоя (telegram.Notifier) не зависел от usecase-пакетов модулей.
type TGButton struct {
	Text         string
	CallbackData string
}

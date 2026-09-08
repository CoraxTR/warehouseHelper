// Пакет returns — «Возврат в продажу»: наблюдатель журнала действий
// МойСклад (audit) и страница расформирования отменённых/урезанных заказов.
// Владелец таблиц return_events и return_cursor. Снимков диффа не хранит —
// данные страницы перечитываются из МС по id события (аудит хранится долго).
package returns

import (
	"errors"
	"time"
)

// ErrEventNotFound — события аудита с таким id нет в return_events
// (не отслеживалось или уже удалено).
var ErrEventNotFound = errors.New("событие возврата не найдено")

// EventKind — вид события аудита, на которое склад реагирует расформированием.
type EventKind string

const (
	// KindCancelled — заказ переведён в статус «Отменён» (MSAPI_CANCELLED_STATE_ID);
	// состав возврата — позиции живого заказа с quantity == reserved.
	KindCancelled EventKind = "order_cancelled"
	// KindRemoved — из заказа удалены отложенные позиции (quantity == reserved);
	// состав возврата — удалённые позиции из diff события.
	KindRemoved EventKind = "positions_removed"
)

// EventStatus — жизненный цикл события в модуле.
type EventStatus string

const (
	StatusNew  EventStatus = "new"  // обнаружено, сообщение ещё не отправлено (повторная отправка после рестарта)
	StatusSent EventStatus = "sent" // сообщение в чате склада, ждёт расформирования
	StatusDone EventStatus = "done" // возврат принят stock (или закрыт вручную), сообщение удалено
)

// ReturnEvent — событие аудита, отслеживаемое модулем (строка return_events).
type ReturnEvent struct {
	ID        string    // uuid события аудита МС (audit/<id>) — PK
	Kind      EventKind // positions_removed / order_cancelled
	OrderID   string    // uuid заказа
	OrderName string    // номер заказа (для текста сообщения)
	Moment    time.Time // момент события (UTC; из audit moment в TZ учётки = МСК)
	Status    EventStatus
	Manual    bool   // закрыто вручную (куски не вернулись)
	ChatID    *int64 // TG-чат отправленного сообщения (nil — не отправлено)
	MessageID *int64 // TG message_id (deleteMessage после обработки)
}

// Expected — строка ожидания возврата: товар и количество, которое нужно
// вернуть в продажу сканированием. Агрегация по товару (одна строка на
// товар: в заказе может быть несколько строк одного товара — веса/штуки
// складываются, «накопленные товары»). Вычисляется из МС по id события,
// в БД не хранится.
type Expected struct {
	ProductID    string // uuid товара в МС (для записи в stock)
	InternalCode string // код склада (первые 8 цифр этикетки)
	Name         string // название товара
	Weighted     bool   // весовой (сверка в граммах) или штучный (сверка по количеству)
	// ExpectedQty — ожидание: весовой — граммы (Σ весов позиций); штучный —
	// количество единиц. ScannedQty — накоплено сканами (для UI).
	ExpectedQty int64
	ScannedQty  int64
}

// CatalogProduct — товар каталога для сборки ожиданий возврата.
type CatalogProduct struct {
	ProductID    string // uuid товара в МС
	InternalCode string // код склада; пусто — товар без кода, в возврат не идёт
	Weighted     bool   // весовой (сверка в граммах) или штучный (по количеству)
}

// Ошибки страницы возврата.
var (
	// ErrAlreadyDone — возврат уже обработан (или закрыт вручную):
	// повторный AcceptReturn задвоил бы остатки.
	ErrAlreadyDone = errors.New("возврат уже обработан")
	// ErrNothingToReturn — в событии нет отложенных позиций (пустая отмена /
	// удаление без quantity == reserved): уведомление не отправляем.
	ErrNothingToReturn = errors.New("нечего возвращать: отложенные позиции не найдены")
)

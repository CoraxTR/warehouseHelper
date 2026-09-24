// Пакет returns — «Возврат в продажу»: наблюдатель журнала действий
// МойСклад (audit) и страница расформирования отменённых/урезанных заказов.
// Владелец таблиц return_events и return_cursor. Снимков диффа не хранит —
// данные страницы перечитываются из МС по id события (аудит хранится долго).
package returns

import (
	"errors"
	"time"

	"warehouseHelper/internal/scanmatch"
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

// Ожидания возврата и правила сверки сканов живут в общем ядре
// internal/scanmatch (переиспользуются «возвратом в сроки при переподборе»);
// здесь — алиас, чтобы публичный API пакета returns не менялся.
//
// Expected — строка ожидания возврата: РОВНО одна строка отчёта (позиция
// живого заказа / удалённая позиция диффа) — одна строка формы, без склейки
// по товару.
type Expected = scanmatch.Expected

// CatalogProduct — товар каталога склада в шве «Каталог» модуля (состав
// возврата события, «Создать коробку», «Ручной возврат», «Вывод из продажи»).
// Свой тип, а не алиас scanmatch.CatalogProduct: название товара нужно
// подписям наклеек коробок, а ядру сверки — нет, поэтому состав полей
// расширяет потребитель шва; сведение к форме ядра делает expected.go.
// Публичный API пакета returns при этом не меняется.
type CatalogProduct struct {
	ProductID    string // uuid товара в МС
	InternalCode string // код склада; пусто — товар без кода, в возврат не идёт
	Name         string // название товара (products.name, «название из МС»)
	Weighted     bool   // весовой (сверка в граммах) или штучный (по количеству)
}

// RemainingPosition — строка, оставшаяся в живом заказе: read-only ориентир
// оператору на событии удаления (что ещё лежит в заказе и где искать товар).
type RemainingPosition struct {
	InternalCode string  // код склада (assortment.code)
	Name         string  // название товара
	Quantity     float64 // количество строки: кг для весовых, штуки для штучных
	Weighted     bool    // тип учёта из каталога склада
}

// Ошибки страницы возврата.
var (
	// ErrAlreadyDone — возврат уже обработан (или закрыт вручную):
	// повторный AcceptReturn задвоил бы остатки.
	ErrAlreadyDone = errors.New("возврат уже обработан")
	// ErrNothingToReturn — в событии нет отложенных позиций (пустая отмена /
	// удаление без quantity == reserved): уведомление не отправляем.
	ErrNothingToReturn = errors.New("нечего возвращать: отложенные позиции не найдены")
	// ErrReserveNotCleared — резерв отменённого заказа не снят (МС не принял
	// PUT / строки позиций пришли неполным составом): остатки НЕ записаны,
	// событие живо. Оператору нужен свой текст — резерв снимается в МС вручную,
	// после чего расформирование повторяется (повтор безопасен: reserve уже 0).
	ErrReserveNotCleared = errors.New("резерв отменённого заказа не снят")
)

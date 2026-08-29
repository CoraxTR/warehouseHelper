// Пакет stock — домен модуля «Сроки» (остатки по срокам годности).
// Модуль владеет таблицей product_stock; страницы «Сроки» и «Шорт-лист»
// в хабе «МойСклад» читают остатки из кэша модуля и обновляются по вебсокету.
package stock

import (
	"errors"
	"time"
)

// Lot — партия товара с одним сроком годности (строка product_stock).
// BestBefore + product_id — PK.
type Lot struct {
	BestBefore time.Time // годен до (DATE), без времени
	Qty        int64     // остаток, штук (весовые — по среднему весу)
	ProducedOn *time.Time // дата выработки; nil — не известна

	// General/Telegram — «просто» скидки, пишет будущий модуль расчёта скидок.
	// GeneralManual/TelegramManual — ручные, пишет UI сроков (всегда важнее «просто»).
	// nil = не задана; 0 = заданная скидка ноль.
	General         *int16
	Telegram        *int16
	GeneralManual   *int16
	TelegramManual  *int16
}

// Product — товар с остатками по лотам (кэш модуля).
type Product struct {
	ID           string // uuid товара МС
	InternalCode string // 8 цифр из МС code; индекс для будущей приёмки
	Name         string
	GroupName    string
	ShortList    bool
	Lots         []Lot // по возрастанию BestBefore
}

// Event — факт изменения остатков, публикуется владельцем данных (usecase)
// в вебсокет-хаб. Клиенты пересчитывают таблицу по своему состоянию.
type Event struct {
	Kind      string // EventLotUpsert (пока); lot_delete/product_delete появятся с подбором
	ProductID string
	Lot       *Lot // заполнен для lot_upsert
}

// Виды событий.
const (
	EventLotUpsert = "lot_upsert"
)

// ErrLotNotFound — нет строки product_stock с таким (product_id, best_before).
var ErrLotNotFound = errors.New("лот с таким сроком годности не найден")

// ErrProductNotFound — товар не найден в кэше остатков.
var ErrProductNotFound = errors.New("товар не найден в остатках")

// Пакет stock — домен модуля «Сроки» (остатки по срокам годности).
// Модуль владеет таблицей product_stock; страницы «Сроки» и «Шорт-лист»
// в хабе «МойСклад» читают остатки из кэша модуля и обновляются по вебсокету.
package stock

import (
	"errors"
	"time"
)

// Lot — партия товара с одним сроком годности (строка product_stock).
// Даты сериализуются в RFC3339, клиент берёт первые 10 символов (YYYY-MM-DD).
type Lot struct {
	BestBefore time.Time  `json:"best_before"` // годен до (DATE), PK-компонент
	Qty        int64      `json:"qty"`         // остаток, штук (весовые — по среднему весу)
	ProducedOn *time.Time `json:"produced_on"` // дата выработки; null — не известна

	// General/Telegram — «просто» скидки, пишет будущий модуль расчёта скидок.
	// GeneralManual/TelegramManual — ручные, пишет UI сроков.
	// null = не задана; 0 = заданная скидка ноль.
	General        *int16 `json:"discount_general"`
	Telegram       *int16 `json:"discount_telegram"`
	GeneralManual  *int16 `json:"discount_general_manual"`
	TelegramManual *int16 `json:"discount_telegram_manual"`
}

// Product — товар с остатками по лотам (кэш модуля).
type Product struct {
	ID           string `json:"id"`
	InternalCode string `json:"internal_code"`
	Name         string `json:"name"`
	GroupName    string `json:"group_name"`
	ShortList    bool   `json:"short_list"`
	Lots         []Lot  `json:"lots"` // по возрастанию BestBefore
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

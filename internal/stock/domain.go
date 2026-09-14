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

	// General/Telegram — «просто» скидки, пишет модуль расчёта скидок
	// (internal/discounts через шов DiscountWriter).
	// GeneralManual/TelegramManual — ручные, пишет UI сроков.
	// null и 0 = скидка не задана: ноль означает «скидки нет», а не «запрет
	// скидки» (правило «0 = NULL», internal/stock/AGENTS.md).
	General        *int16 `json:"discount_general"`
	Telegram       *int16 `json:"discount_telegram"`
	GeneralManual  *int16 `json:"discount_general_manual"`
	TelegramManual *int16 `json:"discount_telegram_manual"`

	// DiscountSource — метка источника действующей «простой» скидки сайта
	// (product_stock.discount_source): "expiry" | "surplus" | "manual";
	// пустая строка — метки нет (в БД NULL). Нужна клиенту для подсветки
	// ячеек: сроковая/ручная скидка — жёлтая, только избыток — розовая.
	DiscountSource string `json:"discount_source"`
}

// Product — товар каталога с лотами остатков (кэш модуля «Сроки»).
// Lots пуст, если по товару нет ни одной строки product_stock — строка
// всё равно показывается на страницах (пустые клетки, «нет в наличии»).
type Product struct {
	ID           string `json:"id"`
	InternalCode string `json:"internal_code"`
	Name         string `json:"name"`
	GroupName    string `json:"group_name"`
	ShortList    bool   `json:"short_list"`
	Lots         []Lot  `json:"lots"` // по возрастанию BestBefore
}

// ProductWrite — правки остатков одного товара (замена по сканам
// «Обновить сроки»): целевые лоты и удаления.
type ProductWrite struct {
	ProductID string
	Upserts   []LotWrite
	Deletes   []time.Time // best_before удаляемых лотов
}

// LotWrite — целевое состояние лота при upsert (скидки передаёт usecase —
// он решил, что сохранить, а что сбросить).
type LotWrite struct {
	BestBefore     time.Time
	Qty            int64
	ProducedOn     time.Time
	GeneralManual  *int16
	TelegramManual *int16
}

// LotIn — принятая партия товара (адаптер модуля приёмки): количество
// ДОБАВЛЯЕТСЯ к существующему лоту (upsert qty +=), существующий срок
// не заменяется; produced_on — COALESCE (nil не затирает известную дату).
type LotIn struct {
	ProductID  string
	BestBefore time.Time
	Qty        int64
	ProducedOn *time.Time
}

// PickLotIn — списание подобранных единиц (адаптер модуля подбора заказов):
// qty ВЫЧИТАЕТСЯ из остатка лота, остаток не уходит в минус (floor 0);
// лот, списанный до нуля, удаляется. Лот с остатком меньше qty — дефицит:
// списывается до нуля, недостача уходит в уведомление складу.
type PickLotIn struct {
	ProductID  string
	BestBefore time.Time
	Qty        int64 // единиц к списанию (>0)
}

// DiscountWrite — «просто»-скидки одного лота (шов записи модуля расчёта
// скидок): General/Telegram пишутся в plain-колонки discount_general/
// discount_telegram; nil = скидка не задана → в БД NULL (движок пишет NULL
// вместо 0). Ручные скидки UI (discount_*_manual) движок не трогает.
// Source — метка источника plain-значения (product_stock.discount_source):
// "expiry" (ступень по сроку), "surplus" (избыток), "manual"; пустая строка —
// метки нет (в БД NULL). Движок скидок пишет expiry/surplus; ручная скидка
// важнее — приоритет разбирает клиент (подсветка ячеек), не БД.
type DiscountWrite struct {
	ProductID  string
	BestBefore time.Time
	General    *int16
	Telegram   *int16
	Source     string
}

// Event — факт изменения остатков, публикуется владельцем данных (usecase)
// в вебсокет-хаб. Клиенты пересчитывают таблицу по своему состоянию.
type Event struct {
	Kind       string // EventLotUpsert / EventLotDelete
	ProductID  string
	BestBefore time.Time // для lot_delete: срок удалённого лота
	Lot        *Lot      // заполнен для lot_upsert
}

// Виды событий.
const (
	EventLotUpsert = "lot_upsert"
	EventLotDelete = "lot_delete"
)

// Метки источника действующей «простой» скидки сайта — значения колонки
// product_stock.discount_source (CHECK в схеме). Пустая строка = метки нет.
const (
	DiscountSourceManual  = "manual"  // скидка поставлена вручную (не движком)
	DiscountSourceExpiry  = "expiry"  // ступень лестницы по сроку годности
	DiscountSourceSurplus = "surplus" // скидка на избыток остатка
)

// Ошибки домена.
var (
	// ErrLotNotFound — нет строки product_stock с таким (product_id, best_before).
	ErrLotNotFound = errors.New("лот с таким сроком годности не найден")

	// ErrProductNotFound — товар не найден в остатках или каталоге.
	ErrProductNotFound = errors.New("товар не найден")

	// Ошибки разбора/проверки сканов «Обновить сроки».
	// ErrScanNotInternal — штрих-код не внутреннего формата (не 29/33 цифры).
	ErrScanNotInternal = errors.New("не внутренний штрих-код (ожидаются 29 или 33 цифры)")
	// ErrScanInvalid — внутренний формат, но данные невалидны.
	ErrScanInvalid = errors.New("неверные данные штрих-кода")
	// ErrScanGroupMismatch — код не относится к ожидаемой группе.
	ErrScanGroupMismatch = errors.New("код не относится к ожидаемой группе")
	// ErrScanProductMismatch — код не совпадает с ожидаемым товаром.
	ErrScanProductMismatch = errors.New("код не совпадает с ожидаемым товаром")
)

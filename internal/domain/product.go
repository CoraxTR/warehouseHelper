package domain

import (
	"errors"
	"time"
)

// ErrInternalCodeTaken — внутренний код (code из МС) уже занят другим товаром.
var ErrInternalCodeTaken = errors.New("внутренний код уже используется другим товаром")

// ErrProductNotFound — товар не найден в каталоге.
var ErrProductNotFound = errors.New("товар не найден в каталоге")

// Product — товар из справочника МойСклад (таблица products).
// Каталог — владелец записи; приёмка читает и передаёт average_weight.
type Product struct {
	ID            string   // UUID МойСклад
	InternalCode  string   // код МС (поле code), уникален
	Name          string   // название из МС
	UOM           string   // единица измерения (uom.name): "шт", "кг", ...
	GroupName     string   // полный путь группы из дерева МС (метаданные дерева)
	FolderID      string   // id папки МС (productfolder); "" — товар без группы; заполняет каталог из дерева папок
	AverageWeight *float64 // средний вес штуки, кг; NULL — не задан
	ShelfLife     *int16   // общий срок годности, дни; NULL — не задан
	PackSize      *int16   // размер пачки, штук; NULL — не пачками
	InventoryType string   // «Вид инвентаризации» из МС, копия строки
	ShortList     bool     // показывать в короткой версии сроков
	TrackWeekly   bool     // учитывать в недельном обороте
	// SiteURL — адрес позиции на сайте (steakhome.ru), ручное поле карточки;
	// "" — url не задан, модуль «Проверка сайта» позицию не смотрит. Читается
	// всеми выборками каталога, пишется только сохранением карточки
	// (синки из МС колонку не трогают — url ручной).
	SiteURL string

	// BuyPrice — закупочная цена, копейки. NULL (nil) — МойСклад не отдала
	// цену (объект buyPrice в ответе отсутствует). Заполняется при выгрузке
	// дерева и ресинке товара, а также фоновым обновителем цен.
	BuyPrice *int64
	// SalePrice — цена продажи, копейки: первый элемент массива salePrices
	// МС (в учётке тип цены ровно один — «Цена продажи»). NULL (nil) — МС
	// не отдала цену (массив пуст или отсутствует).
	SalePrice *int64
	// EffectiveVat — НДС, проценты. NULL (nil) — МС не отдала значение
	// (например, НДС наследуется от группы товаров: UseParentVat=true).
	// -1 — НАША метка «без НДС»: в МС такого значения нет, его ставит синк,
	// когда effectiveVat=0 и НДС выключен (vatEnabled=false).
	EffectiveVat *int16
}

// ProductPrice — цены и НДС одного товара для фонового обновления
// (колонки products.buy_price/sale_price/effective_vat). Значения — копейки;
// NULL-семантика полей один в один с domain.Product: nil — МС не отдала,
// -1 в EffectiveVat — наша метка «без НДС».
type ProductPrice struct {
	ID           string // UUID МойСклад
	BuyPrice     *int64 // закупочная цена, копейки; nil — не отдана
	SalePrice    *int64 // цена продажи, копейки; nil — не отдана
	EffectiveVat *int16 // НДС, %; -1 — без НДС; nil — не отдана
}

// ProductPriceCursor — состояние обновителя цен: время следующего инкремента и
// МСК-дата последнего полного прохода. Exists=false — строки курсора ещё нет
// (первый запуск).
type ProductPriceCursor struct {
	Next         time.Time
	LastFullScan string
	Exists       bool
}

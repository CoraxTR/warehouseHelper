package domain

import "errors"

// ErrInternalCodeTaken — внутренний код (code из МС) уже занят другим товаром.
var ErrInternalCodeTaken = errors.New("внутренний код уже используется другим товаром")

// Product — товар из справочника МойСклад (таблица products).
// Каталог — владелец записи; приёмка читает и передаёт average_weight.
type Product struct {
	ID            string   // UUID МойСклад
	InternalCode  string   // код МС (поле code), уникален
	Name          string   // название из МС
	UOM           string   // единица измерения (uom.name): "шт", "кг", ...
	GroupName     string   // полный путь группы из дерева МС (метаданные дерева)
	AverageWeight *float64 // средний вес штуки, кг; NULL — не задан
	ShelfLife     *int16   // общий срок годности, дни; NULL — не задан
	PackSize      *int16   // размер пачки, штук; NULL — не пачками
	InventoryType string   // «Вид инвентаризации» из МС, копия строки
	ShortList     bool     // показывать в короткой версии сроков
	TrackWeekly   bool     // учитывать в недельном обороте
}

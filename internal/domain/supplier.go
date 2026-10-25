package domain

import (
	"errors"
)

// ErrSupplierExists — поставщик с таким id уже существует (создание вместо редактирования).
var ErrSupplierExists = errors.New("поставщик с таким id уже существует")

// ErrSupplierNotFound — поставщик не найден (в т.ч. удалён конкурентно).
var ErrSupplierNotFound = errors.New("поставщик не найден")

// Supplier — поставщик (контрагент МойСклад), справочник для группировки товаров
// в заказы. id — uuid контрагента МС (counterparty.id), name — название из МС
// (перезапрашивается при каждом сохранении), остальные поля заполняются вручную.
// Расписаний заказа/доставки здесь НЕТ: они живут на паре (товар, поставщик) —
// domain.OrderSchedule, вариантов у пары может быть несколько.
type Supplier struct {
	ID   string // uuid контрагента МС
	Name string

	// Правила вычитки штрихкодов, формат "28-1-6-7-6-13-8-21-8".
	DecodeRules    []string // единичные товары
	BoxDecodeRules []string // коробки

	MinOrderAmount *int64 // минимальная сумма заказа, копейки; nil — не задана
}

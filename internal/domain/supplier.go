package domain

import (
	"errors"
	"time"
)

// ErrSupplierExists — поставщик с таким id уже существует (создание вместо редактирования).
var ErrSupplierExists = errors.New("поставщик с таким id уже существует")

// ErrSupplierNotFound — поставщик не найден (в т.ч. удалён конкурентно).
var ErrSupplierNotFound = errors.New("поставщик не найден")

// Supplier — поставщик (контрагент МойСклад), справочник для группировки товаров
// в заказы. id — uuid контрагента МС (counterparty.id), name — название из МС
// (перезапрашивается при каждом сохранении), остальные поля заполняются вручную.
type Supplier struct {
	ID   string // uuid контрагента МС
	Name string

	// Правила вычитки штрихкодов, формат "28-1-6-7-6-13-8-21-8".
	DecodeRules    []string // единичные товары
	BoxDecodeRules []string // коробки

	// Общее расписание (для всех товаров поставщика).
	OrderDays      []int16 // дни заказа, 1..7 (1=Пн ... 7=Вс)
	DeliveryDays   []int16 // дни доставки, 1..7
	DelayDays      *int16  // макс. дней между заказом и доставкой; nil — не задано
	MinOrderAmount *int64  // минимальная сумма заказа, копейки; nil — не задана

	// OrderCutoffTime — время, до которого можно сделать заказ в дни заказа;
	// общее для обычного и спец. расписания; nil — не задано.
	OrderCutoffTime *time.Time

	// Спец. расписание (для товаров с special_schedule = true).
	SpecialOrderDays    []int16 // дни заказа, 1..7
	SpecialDeliveryDays []int16 // дни доставки, 1..7
	SpecialDelayDays    *int16  // задержка; nil — не задано
}

// Пакет daystate — модуль состояния товара по дням.
// Владеет product_day_state: строка (товар, день) — дневной снимок состояния
// товара, который утром генерируется из product_stock и живёт событиями дня
// (изменения остатков стоком, доступность из календаря, возвраты).
// Клиенты ходят через интерфейсы usecase (сток — DayStateRecorder, календарь —
// SetOrderable, будущее расформирование — RollbackSoldOut); эмитенты фактов
// (SoldOut/Unavailable/откат) уходят в ordercoeff.
package daystate

import (
	"errors"
	"time"
)

// DayState — строка product_day_state (товар × день).
//
// InStock: true/false — состояние известно; nil — неизвестно (строки будущих
// дат, созданные календарём «Доступность»).
// DiscountStart — скидка на начало дня (max по лотам на момент создания
// строки); в течение дня НЕ меняется.
// Discount — актуальная скидка: пересчитывается при каждом событии из лотов;
// понижения не логируются (значение восстанавливается только из колонки).
// DiscountIncreases — только повышения скидки за день (значения, без времени).
// SoldOutToday — «позиция закончилась в течение дня»; обычный приход маркер
// не снимает, сбрасывается только явным возвратом (RollbackSoldOut).
type DayState struct {
	ProductID         string
	Date              time.Time
	InStock           *bool
	DiscountStart     *int16
	Discount          *int16
	DiscountIncreases []int16
	Orderable         bool
	SoldOutToday      bool
}

// LotState — срез лота из product_stock, нужный для пересчёта дня.
type LotState struct {
	Qty int64
	// EffectiveGeneral — эффективная скидка канала «сайт» (COALESCE(manual, plain));
	// nil — скидка не задана. Telegram в состоянии дня не участвует: модуль
	// расчёта скидок дублирует тг-скидку в general (правило владельца).
	EffectiveGeneral *int16
}

// ErrDayNotFound — нет строки состояния за день.
var ErrDayNotFound = errors.New("нет строки состояния за день")

// InStockFromLots — есть ли хоть один лот с qty > 0.
func InStockFromLots(lots []LotState) bool {
	for _, l := range lots {
		if l.Qty > 0 {
			return true
		}
	}
	return false
}

// DiscountFromLots — максимальная effective-скидка канала general по лотам;
// nil, если ни у одного лота скидка не задана.
func DiscountFromLots(lots []LotState) *int16 {
	var top *int16
	for _, l := range lots {
		if l.EffectiveGeneral == nil {
			continue
		}
		if top == nil || *l.EffectiveGeneral > *top {
			v := *l.EffectiveGeneral
			top = &v
		}
	}
	return top
}

// ApplyStockChange пересчитывает строку дня после изменения остатков.
// Возвращает обновлённую строку и soldOutNow — наблюдаемый переход
// «было в наличии → стало нет»: ставит sold_out_today и требует эмита
// SoldOut в ordercoeff.
//
// Правила:
//   - in_stock — пересчёт из лотов (всегда известен после события);
//   - discount — новое значение max по лотам; повышение append'ится
//     в discount_increases, понижение/снятие не логируется;
//   - sold_out_today — сохраняется; устанавливается при переходе в 0,
//     НЕ сбрасывается приходом (сброс — только RollbackSoldOut);
//   - orderable и discount_start событие не трогает.
func ApplyStockChange(cur DayState, lots []LotState) (next DayState, soldOutNow bool) {
	next = cur

	inStock := InStockFromLots(lots)
	next.InStock = &inStock
	if cur.InStock != nil && *cur.InStock && !inStock {
		next.SoldOutToday = true
		soldOutNow = true
	}

	newDiscount := DiscountFromLots(lots)
	if isDiscountIncrease(cur.Discount, newDiscount) {
		next.DiscountIncreases = append(append([]int16(nil), cur.DiscountIncreases...), *newDiscount)
	}
	next.Discount = newDiscount

	return next, soldOutNow
}

// isDiscountIncrease — повышение скидки: новое значение строго больше
// текущего. Текущего нет (NULL = скидка не задана) — повышением считается
// только значение > 0: заданная скидка ноль — это значение, но не повышение.
func isDiscountIncrease(cur, next *int16) bool {
	if next == nil {
		return false
	}
	if cur == nil {
		return *next > 0
	}
	return *next > *cur
}

package discounts

import "time"

// Швы модуля скидок, которые описывает потребитель (usecase) и связывает di.go
// (правило проекта: интерфейсы — на стороне потребителя, реализация — модуль,
// который владеет данными; модули не импортируют типы друг друга).

// DiscountWrite — правка «простой» скидки лота (колонки product_stock
// discount_general/discount_telegram). Записывает движок скидок через шов
// модуля «Сроки» (stock.SetDiscounts); ручные колонки не трогаются.
//
// nil = скидка не задана (в БД NULL), 0 допустим (правило «0 = NULL»:
// значение считается «скидки нет», поэтому движок пишет NULL, а не 0).
// Source — источник значения для подсветки на страницах («expiry», «surplus»);
// пустая строка — источник не маркируется.
type DiscountWrite struct {
	ProductID  string
	BestBefore time.Time
	General    *int16
	Telegram   *int16
	Source     string
}

// DayFlag — шаг модуля, который нужно выполнить один раз за день
// (таблица discount_day_flags: булево значение на сегодня). Нужен для «догона
// после сна»: после рестарта флаг false — шаг дорабатывается.
type DayFlag string

// Маркеры дня (имена колонок discount_day_flags).
const (
	// FlagSurplus — часовой пересчёт избытка за день сделан.
	FlagSurplus DayFlag = "surplus_done"
	// FlagExpiry — утренний пересчёт по сроку за день сделан.
	FlagExpiry DayFlag = "expiry_done"
	// FlagDigestSent — дайджест в общий чат отправлен.
	FlagDigestSent DayFlag = "digest_sent"
	// FlagPlan — план ТГ-слота собран и отправлен в чат склада.
	FlagPlan DayFlag = "tg_plan_done"
	// FlagRaise — подъём general до telegram выполнен.
	FlagRaise DayFlag = "tg_raise_done"
)

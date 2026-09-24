// Швы юзкейса модуля скидок: что расчёту нужно от внешнего мира. Реализации —
// чужие модули (stock, averagesales, telegram, repository/postgres), связка —
// di.go. Здесь только интерфейсы: usecase не импортирует типы других модулей
// (как receiving.ProductRef), адаптеры под конкретные реализации — в di.go.
package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/discounts"
)

// Repository — данные модуля: снапшот входа расчёта, история ТГ-слотов
// и маркеры дня.
type Repository interface {
	// LoadDiscountInput — все лоты остатков с товарными признаками (одна
	// выборка; today — начало дня расчёта). Оборота здесь НЕТ: это данные
	// модуля средних продаж, их расчёт берёт его методами (шов Turnover).
	LoadDiscountInput(ctx context.Context, today time.Time) ([]discounts.Input, error)
	// SaveDigest — сохранить рассылку с позициями (sent_at NULL: собрана, но
	// ещё не отправлена; факт отправки фиксирует MarkDigestSent).
	SaveDigest(ctx context.Context, d discounts.DigestRecord, items []discounts.DigestItem) error
	// MarkDigestSent — отметить сохранённую рассылку отправленной (по каналу и
	// дню плана: id наружу не отдаём, история пишется один раз за день).
	MarkDigestSent(ctx context.Context, chatKind string, plannedAt, at time.Time) error
	// LastDigestPairs — лоты последней ОТПРАВЛЕННОЙ рассылки (антидубль
	// «не было в предыдущей рассылке»; пустая карта — рассылок не было).
	LastDigestPairs(ctx context.Context) (map[discounts.LotKey]struct{}, error)
	// TodaySlot — позиции отправленной сегодня рассылки (план 14:00): скидка
	// плана плюс контроль «не продано» (остаток пары и план продаж у добора из
	// избытка). По ним поднимают general (16:00).
	TodaySlot(ctx context.Context, date time.Time) ([]discounts.SlotItem, error)
	// MarkGeneralRaised — отметить подъём general до telegram по позициям.
	MarkGeneralRaised(ctx context.Context, pairs []discounts.LotKey, at time.Time) error
	// DayFlagDone — сделан ли шаг дня (повтор после рестарта/сна пропускается).
	DayFlagDone(ctx context.Context, date time.Time, flag discounts.DayFlag) (bool, error)
	// MarkDayFlag — отметить шаг дня сделанным.
	MarkDayFlag(ctx context.Context, date time.Time, flag discounts.DayFlag) error
}

// Turnover — действующий средний оборот товара, шт за период (недельный ряд —
// за неделю, месячный — за месяц): шов к модулю средних продаж, который сам
// ходит в МС пачками (пачка товаров = один запрос на период) и считает среднее
// обычным правилом окна (завершённые периоды из БД + текущий незакрытый).
//
// Отсутствие товара в карте и неположительное значение = данных о продажах нет
// (избытка нет, решение владельца 14.09.2026).
type Turnover interface {
	// RefreshCurrent обновляет ТОЛЬКО текущий незакрытый период запрошенных
	// товаров — рабочий вызов тика (избыток, приёмка): свежие цифры без
	// перезапроса всего окна. Сохранённый оборот читается отдельным методом
	// Averages: без обращений в МС, батчем (пачка товаров — один запрос).
	RefreshCurrent(ctx context.Context, productIDs []string) (map[string]float64, error)
	// RefreshWindow обновляет ВСЕ периоды окна (13 месяцев / 6 недель) —
	// один раз в день (09:00) и на первом запуске дня: забирает возвраты
	// задним числом по старым заказам. Сохранённый оборот без МС читает
	// отдельный метод Averages (батчем): обновление периодов и чтение
	// среднего — разные вызовы шва.
	RefreshWindow(ctx context.Context, productIDs []string) (map[string]float64, error)
	// Averages — действующий средний оборот товаров по данным БД, без
	// обращений в МС: значение для всех, кому тик период не обновлял.
	// Оборот — данные модуля средних продаж, расчёт читает их только здесь.
	Averages(ctx context.Context, productIDs []string) (map[string]float64, error)
}

// DiscountWriter — запись «простых» скидок лотов (шов модуля «Сроки»):
// БД → кэш → событие делает модуль-владелец.
type DiscountWriter interface {
	SetDiscounts(ctx context.Context, writes []discounts.DiscountWrite) error
}

// WarehouseNotifier — список позиций слота в чат склада (14:00).
type WarehouseNotifier interface {
	NotifyWarehouse(text string) error
}

// CommonNotifier — уведомления об изменениях скидок и дайджест в общий канал,
// плюс ответ боту в чат отправителя (команда /discounts). Один шов: обе отправки
// делает один телеграм-уведомитель.
type CommonNotifier interface {
	NotifyCommon(ctx context.Context, text string) error
	// SendDetails — обычный текст в конкретный чат (ответ на /discounts).
	SendDetails(ctx context.Context, chatID int64, text string) error
}

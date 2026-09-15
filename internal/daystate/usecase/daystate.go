// Пакет usecase — сценарии модуля daystate: утренний снапшот состояний
// из product_stock, пересчёт строки дня по событиям стока (шов), доступность
// из календаря «Доступность товаров», откат sold_out при возврате.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"warehouseHelper/internal/daystate"
	"warehouseHelper/internal/metrics"
)

const trackPkg = "daystate"

// Repository — контракт хранилища состояний по дням, реализуется
// postgres-репозиторием (методы DayState*).
type Repository interface {
	// EnsureDay создаёт строку дня, если её нет (ON CONFLICT DO NOTHING);
	// snapshot-значения используются только при вставке.
	EnsureDay(ctx context.Context, d daystate.DayState) error
	// GetDay читает строку дня; строки нет — daystate.ErrDayNotFound.
	GetDay(ctx context.Context, productID string, date time.Time) (*daystate.DayState, error)
	// UpdateDay обновляет пересчитываемые поля (in_stock, discount,
	// discount_increases, sold_out_today); строки нет — daystate.ErrDayNotFound.
	UpdateDay(ctx context.Context, d daystate.DayState) error
	// SetOrderable обновляет доступность товара для набора дат (батч, одна
	// транзакция): строки создаются при необходимости (in_stock/discount NULL).
	SetOrderable(ctx context.Context, productID string, dates []time.Time, orderable bool) error
	// SnapshotDone — сделаны ли строки за дату (маркер утреннего снапшота).
	SnapshotDone(ctx context.Context, date time.Time) (bool, error)
	// SnapshotInsert делает утренний снимок за дату из product_stock одним
	// запросом; существующие строки не перезаписываются (COALESCE-дополнение).
	SnapshotInsert(ctx context.Context, date time.Time) error
	// LotsSnapshot читает лоты товара из product_stock (qty + effective
	// general-скидка) — срез для пересчёта дня.
	LotsSnapshot(ctx context.Context, productID string) ([]daystate.LotState, error)
	// LastKnownInStock — последнее известное наличие товара до даты before
	// (ближайшая строка дня с заполненным in_stock; строки календаря
	// «Доступность» пропускаются); истории нет — nil: у товара в полном
	// отсутствии строк в таблице нет вовсе (лот, списанный до нуля, удаляется,
	// снапшот дня такого товара не видит). Первый приход нового товара
	// уведомления не даёт — «появился» считается по последней известной строке.
	LastKnownInStock(ctx context.Context, productID string, before time.Time) (*bool, error)
	// ClearSoldOut сбрасывает маркер «закончилась» строки дня; строки нет —
	// не ошибка (нечего сбрасывать).
	ClearSoldOut(ctx context.Context, productID string, date time.Time) error
	// ListByRange читает строки за период [from, to]:
	// map product_id → map date → строка (для страниц календаря и отчёта).
	ListByRange(ctx context.Context, from, to time.Time) (map[string]map[time.Time]daystate.DayState, error)
}

// CatalogProvider — каталог товаров для страниц daystate; реализует
// goods (метод CatalogProducts), связка в di.go.
type CatalogProvider interface {
	CatalogProducts(ctx context.Context) ([]daystate.CatalogProduct, error)
}

// SoldOutNotifier — получатель события «позиция закончилась»; реализует
// ordercoeff (метод SoldOut), связка в di.go.
type SoldOutNotifier interface {
	SoldOut(ctx context.Context, productID string, at time.Time) error
}

// UnavailableNotifier — получатель события «недоступна для заказа»; реализует
// ordercoeff (метод Unavailable), связка в di.go.
type UnavailableNotifier interface {
	Unavailable(ctx context.Context, productID string, at time.Time) error
}

// SoldOutRollbackNotifier — получатель отката «закончилась»; реализует
// ordercoeff (метод RollbackSoldOut, bool — был ли живой), связка в di.go.
type SoldOutRollbackNotifier interface {
	RollbackSoldOut(ctx context.Context, productID string, at time.Time) (bool, error)
}

// StockStatusNotifier — получатель уведомлений о смене наличия в течение дня
// («закончился»/«появился»); реализует app-адаптер поверх telegram
// (общий канал, TG_COMMON_CHAT_ID), связка в di.go. Наблюдатель: ошибка
// уведомления логируется, операцию стока не роняет.
type StockStatusNotifier interface {
	// SoldOut — товар закончился (переход в ноль).
	SoldOut(ctx context.Context, productID string) error
	// BackInStock — товар появился (обратный переход).
	BackInStock(ctx context.Context, productID string) error
}

// UseCase — сценарии daystate. Сток — клиент (OnStockChanged); эмитенты —
// наблюдатели фактов для ordercoeff.
type UseCase struct {
	repo            Repository
	catalog         CatalogProvider
	soldOut         SoldOutNotifier
	unavailable     UnavailableNotifier
	soldOutRollback SoldOutRollbackNotifier
	stockStatus     StockStatusNotifier

	now func() time.Time // переопределяется в тестах
}

// NewUseCase собирает сценарий; catalog и notifier'ы обязательны
// (goods и ordercoeff реализуют, связка di.go).
func NewUseCase(repo Repository, catalog CatalogProvider, soldOut SoldOutNotifier, unavailable UnavailableNotifier, soldOutRollback SoldOutRollbackNotifier, stockStatus StockStatusNotifier) *UseCase {
	return &UseCase{
		repo:            repo,
		catalog:         catalog,
		soldOut:         soldOut,
		unavailable:     unavailable,
		soldOutRollback: soldOutRollback,
		stockStatus:     stockStatus,
		now:             time.Now,
	}
}

// EnsureSnapshot делает утренний снимок дня из product_stock, если его ещё нет
// (маркер — существование строк за дату). Идемпотентна: ретрай после ошибки БД
// и спящего ПК безопасен. Строки, созданные событиями/календарём, дополняются
// только недостающими полями (см. SnapshotInsert).
func (uc *UseCase) EnsureSnapshot(ctx context.Context, today time.Time) error {
	done := metrics.Track(trackPkg, "EnsureSnapshot")
	defer done()

	today = normalizeDate(today)
	ok, err := uc.repo.SnapshotDone(ctx, today)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if err := uc.repo.SnapshotInsert(ctx, today); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("daystate: снапшот дня %s создан", today.Format(time.DateOnly)))
	return nil
}

// Start выполняет фоновую задачу утреннего снапшота до отмены ctx (образец
// tempcleaner): раз в минуту проверяет, наступило ли время снапшота (время дня
// от полуночи, локальное время процесса) и не сделан ли он уже за сегодня.
// Ошибка БД — ретрай на следующем тике; ПК проспал 09:00 — снапшот делается
// первым же тиком после пробуждения.
//
// Блокируется до отмены ctx, то есть вызывать только из горутины: иначе здесь
// встанет старт приложения. Блокирующий вид — ради остановки: app.Shutdown
// ждёт фон целиком, а не «отменил ctx и надеюсь».
func (uc *UseCase) Start(ctx context.Context, snapshotTime time.Duration) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		uc.trySnapshot(ctx, snapshotTime)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// trySnapshot — один тик фоновой задачи; ошибка только логируется.
func (uc *UseCase) trySnapshot(ctx context.Context, snapshotTime time.Duration) {
	now := uc.now()
	if minutesFromMidnight(now) < snapshotTime {
		return
	}
	if err := uc.EnsureSnapshot(ctx, now); err != nil {
		slog.Info(fmt.Sprintf("daystate: снапшот дня: %v", err))
	}
}

// OnStockChanged — шов стока: вызывается после каждой записи остатков
// (приёмка, «Обновить сроки», ручная скидка). Страхует строку дня (создаёт,
// если её нет: in_stock — из последнего известного наличия, скидки — из
// лотов), пересчитывает из лотов; при переходе в «нет в наличии» ставит
// sold_out_today и эмитит SoldOut в ordercoeff. Наблюдатель: ошибка
// возвращается, сток её только логирует (операция стока не роняется).
//
// «Было» для дня без строки берётся из последнего известного наличия
// (LastKnownInStock), а не из текущих лотов: у товара в полном отсутствии
// строк нет вовсе (лот, списанный до нуля, удаляется — снапшот дня его не
// видит), поэтому посев из лотов терял переход «не было → появилось».
// Истории нет (первый приход нового товара) — прежнее поведение: посев из
// лотов, перехода нет; устаревшая последняя строка (обнуление не наблюдалось
// контуром: приложение не работало, остатки правились не через склад) даёт
// пропуск уведомления. История читается только там, где состояние дня
// неизвестно (строки нет или она от календаря) — на обычном событии дня
// лишнего запроса нет. Сбой UpdateDay после EnsureDay оставляет строку дня
// не пересчитанной — окно не-транзакционности, исправляется следующим
// событием дня.
func (uc *UseCase) OnStockChanged(ctx context.Context, productID string) error {
	done := metrics.Track(trackPkg, "OnStockChanged")
	defer done()

	if productID == "" {
		return errors.New("не указан товар")
	}
	lots, err := uc.repo.LotsSnapshot(ctx, productID)
	if err != nil {
		return err
	}

	today := normalizeDate(uc.now())
	cur, err := uc.repo.GetDay(ctx, productID, today)
	switch {
	case errors.Is(err, daystate.ErrDayNotFound):
		cur, err = uc.createDayRow(ctx, productID, today, lots)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		// Строка дня от календаря «Доступность» (состояние неизвестно):
		// «было» тоже берём из истории, иначе переход не увидеть.
		if cur.InStock == nil {
			prior, err := uc.repo.LastKnownInStock(ctx, productID, today)
			if err != nil {
				return err
			}
			cur.InStock = prior
		}
	}

	next, soldOutNow, backInStock := daystate.ApplyStockChange(*cur, lots)
	if err := uc.repo.UpdateDay(ctx, next); err != nil {
		return err
	}

	if soldOutNow {
		if err := uc.soldOut.SoldOut(ctx, productID, today); err != nil {
			// Строка дня уже записана; эмит — наблюдаемый факт, его потеря
			// не откатывает изменение остатков. Логируем для разбора.
			slog.Info(fmt.Sprintf("daystate: SoldOut %s: %v", productID, err))
		}
		if err := uc.stockStatus.SoldOut(ctx, productID); err != nil {
			slog.Info(fmt.Sprintf("daystate: уведомление SoldOut %s: %v", productID, err))
		}
	}
	if backInStock {
		if err := uc.stockStatus.BackInStock(ctx, productID); err != nil {
			slog.Info(fmt.Sprintf("daystate: уведомление BackInStock %s: %v", productID, err))
		}
	}
	return nil
}

// createDayRow — холодный путь шва стока: строки дня нет, создаём её.
// in_stock — последнее известное наличие (истории нет — из текущих лотов:
// прежнее поведение, перехода не будет), скидки — из лотов. Строка читается
// обратно: при гонке её мог создать кто-то ещё, а EnsureDay существующую
// строку не перезаписывает.
func (uc *UseCase) createDayRow(ctx context.Context, productID string, today time.Time, lots []daystate.LotState) (*daystate.DayState, error) {
	prior, err := uc.repo.LastKnownInStock(ctx, productID, today)
	if err != nil {
		return nil, err
	}
	seed := new(daystate.InStockFromLots(lots))
	if prior != nil {
		seed = prior
	}

	if err := uc.repo.EnsureDay(ctx, daystate.DayState{
		ProductID:     productID,
		Date:          today,
		InStock:       seed,
		DiscountStart: daystate.DiscountFromLots(lots),
		Discount:      daystate.DiscountFromLots(lots),
		Orderable:     true,
	}); err != nil {
		return nil, err
	}

	return uc.repo.GetDay(ctx, productID, today)
}

// SetOrderable — календарь «Доступность товаров»: даты доступны для заказа
// (любые, включая будущие). Строки создаются при необходимости, orderable
// перезаписывается. Без эмита — отката «недоступен» в ordercoeff нет.
func (uc *UseCase) SetOrderable(ctx context.Context, productID string, dates []time.Time) error {
	done := metrics.Track(trackPkg, "SetOrderable")
	defer done()

	dates, err := uc.normalizeDates(productID, dates)
	if err != nil {
		return err
	}
	return uc.repo.SetOrderable(ctx, productID, dates, true)
}

// SetUnavailable — календарь «Доступность товаров»: даты недоступны для
// заказа. На каждую дату эмитится Unavailable (вклад 0, держит цепочку
// коэффициента); ошибка эмита логируется, запись не откатывается.
func (uc *UseCase) SetUnavailable(ctx context.Context, productID string, dates []time.Time) error {
	done := metrics.Track(trackPkg, "SetUnavailable")
	defer done()

	dates, err := uc.normalizeDates(productID, dates)
	if err != nil {
		return err
	}
	if err := uc.repo.SetOrderable(ctx, productID, dates, false); err != nil {
		return err
	}
	for _, d := range dates {
		if err := uc.unavailable.Unavailable(ctx, productID, d); err != nil {
			slog.Info(fmt.Sprintf("daystate: Unavailable %s %s: %v", productID, d.Format(time.DateOnly), err))
		}
	}
	return nil
}

// normalizeDates проверяет товар и даты календаря, убирает дубликаты и
// приводит даты к единому представлению DATE.
func (uc *UseCase) normalizeDates(productID string, dates []time.Time) ([]time.Time, error) {
	if productID == "" {
		return nil, errors.New("не указан товар")
	}
	dates = uniqueDates(dates)
	if len(dates) == 0 {
		return nil, errors.New("не выбраны даты")
	}
	return dates, nil
}

// RollbackSoldOut — возврат товара в остаток через расформирование заказа
// (вызовет будущий модуль продукции/подбора): сбрасывает маркер дня и эмитит
// откат SoldOut в ordercoeff. Маркер сбрасывается первым; ошибка эмита
// возвращается вызывающему (событие отката могло потеряться).
func (uc *UseCase) RollbackSoldOut(ctx context.Context, productID string, at time.Time) error {
	done := metrics.Track(trackPkg, "RollbackSoldOut")
	defer done()

	at = normalizeDate(at)
	if err := uc.repo.ClearSoldOut(ctx, productID, at); err != nil {
		return err
	}
	if _, err := uc.soldOutRollback.RollbackSoldOut(ctx, productID, at); err != nil {
		return err
	}
	return nil
}

// normalizeDate приводит дату к UTC-полуночи (единое представление DATE).
func normalizeDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// minutesFromMidnight — время дня от полуночи (локальное), для сравнения
// с временем снапшота.
func minutesFromMidnight(t time.Time) time.Duration {
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

// uniqueDates убирает дубликаты дат, сохраняя порядок.
func uniqueDates(dates []time.Time) []time.Time {
	seen := map[time.Time]struct{}{}
	out := make([]time.Time, 0, len(dates))
	for _, d := range dates {
		d = normalizeDate(d)
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

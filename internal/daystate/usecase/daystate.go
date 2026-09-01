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

// UseCase — сценарии daystate. Сток — клиент (OnStockChanged); эмитенты —
// наблюдатели фактов для ordercoeff.
type UseCase struct {
	repo            Repository
	catalog         CatalogProvider
	soldOut         SoldOutNotifier
	unavailable     UnavailableNotifier
	soldOutRollback SoldOutRollbackNotifier

	now func() time.Time // переопределяется в тестах
}

// NewUseCase собирает сценарий; catalog и notifier'ы обязательны
// (goods и ordercoeff реализуют, связка di.go).
func NewUseCase(repo Repository, catalog CatalogProvider, soldOut SoldOutNotifier, unavailable UnavailableNotifier, soldOutRollback SoldOutRollbackNotifier) *UseCase {
	return &UseCase{
		repo:            repo,
		catalog:         catalog,
		soldOut:         soldOut,
		unavailable:     unavailable,
		soldOutRollback: soldOutRollback,
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

// Start запускает фоновую задачу утреннего снапшота (образец tempcleaner):
// раз в минуту проверяет, наступило ли время снапшота (время дня от полуночи,
// локальное время процесса) и не сделан ли он уже за сегодня. Ошибка БД —
// ретрай на следующем тике; ПК проспал 09:00 — снапшот делается первым же
// тиком после пробуждения.
func (uc *UseCase) Start(ctx context.Context, snapshotTime time.Duration) {
	go func() {
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
	}()
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
// (приёмка, «Обновить сроки», ручная скидка). Страхует строку дня (создаёт
// со снимком текущих значений, если её нет), пересчитывает из лотов; при
// переходе в «нет в наличии» ставит sold_out_today и эмитит SoldOut в
// ordercoeff. Наблюдатель: ошибка возвращается, сток её только логирует
// (операция стока не роняется).
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
	if err := uc.repo.EnsureDay(ctx, daystate.DayState{
		ProductID:     productID,
		Date:          today,
		InStock:       boolPtr(daystate.InStockFromLots(lots)),
		DiscountStart: daystate.DiscountFromLots(lots),
		Discount:      daystate.DiscountFromLots(lots),
		Orderable:     true,
	}); err != nil {
		return err
	}

	cur, err := uc.repo.GetDay(ctx, productID, today)
	if err != nil {
		return err
	}
	next, soldOutNow := daystate.ApplyStockChange(*cur, lots)
	if err := uc.repo.UpdateDay(ctx, next); err != nil {
		return err
	}

	if soldOutNow {
		if err := uc.soldOut.SoldOut(ctx, productID, today); err != nil {
			// Строка дня уже записана; эмит — наблюдаемый факт, его потеря
			// не откатывает изменение остатков. Логируем для разбора.
			slog.Info(fmt.Sprintf("daystate: SoldOut %s: %v", productID, err))
		}
	}
	return nil
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

// boolPtr копирует значение в указатель.
func boolPtr(v bool) *bool {
	return &v
}

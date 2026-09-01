// Пакет usecase — сценарии модуля «Коэффициент изменения заказа».
//
// Эмитенты событий (сток, каталог, скидки, расформирование заказов) объявляют
// узкие интерфейсы на своей стороне и получают реализацию через di.go;
// конкретика отсюда не импортируется. Чтение коэффициентов — для модуля
// формирования заказов (CoeffProvider на его стороне): серия по интервалам
// окна, окно и сумма — забота потребителя.
package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/ordercoeff"
)

const trackPkg = "ordercoeff"

// ProductReader — чтение периода отслеживания товара (реализует каталог).
type ProductReader interface {
	// TrackWeekly — товар отслеживается по неделям (products.track_weekly);
	// false = месячный.
	TrackWeekly(ctx context.Context, productID string) (bool, error)
}

// Repository — хранилище коэффициентов (реализует PGClient).
type Repository interface {
	// ApplyCoeffEvent — атомарно применяет событие к периоду товара:
	// одна транзакция (текущая строка + перенос/обнуление предыдущей).
	ApplyCoeffEvent(ctx context.Context, productID string, periodType ordercoeff.PeriodType, at time.Time, ev ordercoeff.EventType) (applied bool, err error)
	// Coefficients — coeff строк календаря для заданных интервалов
	// (нет строки — нет значения в map).
	Coefficients(ctx context.Context, productID string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[time.Time]int16, error)
}

// UseCase — сценарии модуля.
type UseCase struct {
	repo     Repository
	products ProductReader
}

// NewUseCase создаёт сценарий с хранилищем и чтением периода товара.
func NewUseCase(repo Repository, products ProductReader) *UseCase {
	return &UseCase{repo: repo, products: products}
}

// SoldOut — товар закончился (+1 к коэффициенту периода).
func (uc *UseCase) SoldOut(ctx context.Context, productID string, at time.Time) error {
	done := metrics.Track(trackPkg, "SoldOut")
	defer done()
	_, err := uc.apply(ctx, productID, at, ordercoeff.EventSoldOut)
	return err
}

// Discount — товар попал в скидки (−1).
func (uc *UseCase) Discount(ctx context.Context, productID string, at time.Time) error {
	done := metrics.Track(trackPkg, "Discount")
	defer done()
	_, err := uc.apply(ctx, productID, at, ordercoeff.EventDiscount)
	return err
}

// Frozen — товар был заморожен (−2).
func (uc *UseCase) Frozen(ctx context.Context, productID string, at time.Time) error {
	done := metrics.Track(trackPkg, "Frozen")
	defer done()
	_, err := uc.apply(ctx, productID, at, ordercoeff.EventFrozen)
	return err
}

// Unavailable — товар недоступен для заказа (0; держит цепочку живой).
func (uc *UseCase) Unavailable(ctx context.Context, productID string, at time.Time) error {
	done := metrics.Track(trackPkg, "Unavailable")
	defer done()
	_, err := uc.apply(ctx, productID, at, ordercoeff.EventUnavailable)
	return err
}

// RollbackSoldOut — расформирование заказа после «закончился»: откат +1.
// Возвращает false, если в текущем периоде нет живого «закончился»
// («возвращать нечего») — ничего не записано.
func (uc *UseCase) RollbackSoldOut(ctx context.Context, productID string, at time.Time) (bool, error) {
	done := metrics.Track(trackPkg, "RollbackSoldOut")
	defer done()
	return uc.apply(ctx, productID, at, ordercoeff.EventRollbackSoldOut)
}

// RollbackDiscount — отмена скидки: откат −1 (возвращает +1).
// false — если в текущем периоде нет живой скидки.
func (uc *UseCase) RollbackDiscount(ctx context.Context, productID string, at time.Time) (bool, error) {
	done := metrics.Track(trackPkg, "RollbackDiscount")
	defer done()
	return uc.apply(ctx, productID, at, ordercoeff.EventRollbackDiscount)
}

// Coefficients — значения коэффициентов по интервалам окна (для модуля
// формирования заказов; окно и сумма — на стороне потребителя).
func (uc *UseCase) Coefficients(ctx context.Context, productID string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[time.Time]int16, error) {
	done := metrics.Track(trackPkg, "Coefficients")
	defer done()
	return uc.repo.Coefficients(ctx, productID, periodType, intervals)
}

// apply — общий путь события: период товара из каталога + запись в хранилище.
func (uc *UseCase) apply(ctx context.Context, productID string, at time.Time, ev ordercoeff.EventType) (bool, error) {
	weekly, err := uc.products.TrackWeekly(ctx, productID)
	if err != nil {
		return false, err
	}
	pt := ordercoeff.PeriodMonth
	if weekly {
		pt = ordercoeff.PeriodWeek
	}
	return uc.repo.ApplyCoeffEvent(ctx, productID, pt, at, ev)
}

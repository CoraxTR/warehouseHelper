package usecase

import (
	"context"
	"sync"
	"time"

	"fmt"
	"log/slog"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

type ExcelExporter interface {
	ExportOrdersToExcel(orders []*domain.InternalOrder) (savepath string, err error)
}

type ExcelBarcodesExporter interface {
	ExportOrdersBarcodesToExcel(orders []*domain.InternalOrder) (savepath string, err error)
}

type OrdersShipper interface {
	SetOrderAsShippedToRefGo(ctx context.Context, id string) error
}

// OrdersShipmentEnsurer — гарант наличия корректной отгрузки у заказа.
// Реализуется OrderShipmentEnsurer (модуль msclient).
type OrdersShipmentEnsurer interface {
	EnsureOrderShipment(ctx context.Context, id string) error
}

// OrdersProvider — источник заказов для экспорта. Реализуется OrdersUseCase (модуль msclient).
type OrdersProvider interface {
	GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error)
}

// OrdersByIDsProvider — выборка заказов по id для штрих-кодов. Реализуется OrdersUseCase (модуль msclient).
type OrdersByIDsProvider interface {
	GetOrdersByIDs(ctx context.Context, ids []string) ([]*domain.InternalOrder, error)
}

// TempCleaner удаляет устаревшие файлы из временной директории.
type TempCleaner interface {
	CleanOlderThan(maxAge time.Duration) error
}

type ExportToExcelUseCase struct {
	exporter          ExcelExporter
	orders            OrdersProvider
	shipper           OrdersShipper
	shipmentEnsurer   OrdersShipmentEnsurer
	tempCleaner       TempCleaner
	tempCleanupMaxAge time.Duration

	// Жизненный цикл фоновой обработки отгрузок (см. startShipmentsProcessing и
	// Stop): mu защищает stopped и разводит старт фона с ожиданием в Stop —
	// без этого wg.Wait мог бы гоняться с wg.Add из нового экспорта.
	mu      sync.Mutex
	wg      sync.WaitGroup
	stopped bool
}

func NewExportToExcelUseCase(exporter ExcelExporter, orders OrdersProvider, shipper OrdersShipper,
	shipmentEnsurer OrdersShipmentEnsurer, tempCleaner TempCleaner, tempCleanupMaxAge time.Duration) *ExportToExcelUseCase {
	return &ExportToExcelUseCase{
		exporter:          exporter,
		orders:            orders,
		shipper:           shipper,
		shipmentEnsurer:   shipmentEnsurer,
		tempCleaner:       tempCleaner,
		tempCleanupMaxAge: tempCleanupMaxAge,
	}
}

type ExportSummary struct {
	TotalOrders     int
	MoscowPayByCard []string
	Comments        map[string]string
	SpbOrders       []string
	SpbOrdersByCard []string
	SpbComments     map[string]string
	FileName        string
}

func (uc *ExportToExcelUseCase) ExportOrders(ctx context.Context) (summary *ExportSummary, err error) {
	done := metrics.Track(trackPkg, "ExportOrders")
	defer done()
	defer func() {
		if cleanErr := uc.tempCleaner.CleanOlderThan(uc.tempCleanupMaxAge); cleanErr != nil {
			slog.Error(fmt.Sprintf("Failed to clean temp dir: %v", cleanErr))
		}
	}()

	orders, err := uc.orders.GetAllOrders(ctx)
	if err != nil {
		return nil, err
	}

	savepath, err := uc.exporter.ExportOrdersToExcel(orders)
	if err != nil {
		return nil, err
	}

	info := domain.CollectOrdersInfo(orders)

	summary = &ExportSummary{
		TotalOrders:     info.TotalOrders,
		MoscowPayByCard: info.MoscowPayByCard,
		Comments:        info.MoscowComments,
		SpbOrders:       info.SPBOrders,
		SpbOrdersByCard: info.SPBOrdersByCard,
		SpbComments:     info.SPBComments,
		FileName:        savepath,
	}

	// Отметка «отгружен в Реф» и создание отгрузок — в фоне: страница с итогами
	// не должна ждать обхода всех заказов в МС. Фон учитывается в wg и
	// закрывается Stop: иначе процесс уйдёт посреди пометок, и часть
	// заказов останется помеченной в базе, но не в МС.

	return summary, nil
}

// processOrdersShipments помечает заказы отгруженными в Реф и создаёт отгрузки
// для «Наличные»/«Терминал» в фоновом режиме после экспорта.
func (uc *ExportToExcelUseCase) processOrdersShipments(ctx context.Context, orders []*domain.InternalOrder) {
	wg := sync.WaitGroup{}

	for _, order := range orders {
		wg.Go(func() {
			err := uc.shipper.SetOrderAsShippedToRefGo(ctx, order.GetID())
			if err != nil {
				slog.Error(fmt.Sprintf("Error setting order as shipped: %s", err))
			}

			// Отгрузку обеспечиваем только для оплат «Наличные»/«Терминал»:
			// остальные заказы помечаем отгруженными как и раньше.
			if !domain.IsShippablePayment(order.GetPaymentMethod()) {
				return
			}

			err = uc.shipmentEnsurer.EnsureOrderShipment(ctx, order.GetID())
			if err != nil {
				slog.Error(fmt.Sprintf("Error ensuring order shipment: %s", err))
			}
		})
	}

	wg.Wait()
}

// startShipmentsProcessing запускает обработку отгрузок в фоне: страница с
// итогами не должна ждать обхода заказов в МС. Контекст запроса тут не годится
// (отменяется вместе с ответом), поэтому фон идёт от context.Background() — а
// закрывается он не таймаутом, а Stop.
func (uc *ExportToExcelUseCase) startShipmentsProcessing(orders []*domain.InternalOrder) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	if uc.stopped {
		return
	}

	// Регистрация горутины — под тем же мутексом, что и стоп-флаг: иначе Stop
	// успел бы вернуться (счётчик wg нулевой), а фон стартовал бы после него.
	uc.wg.Go(func() {
		//nolint:contextcheck // фоновый запуск после ответа — контекст запроса уже отменён
		uc.processOrdersShipments(context.Background(), orders)
	})
}

// Stop останавливает фоновую обработку отгрузок и ЖДЁТ её: заказ, помеченный в
// базе, но не помеченный в МС (или без созданной отгрузки), — расхождение с МС,
// которое всплывает уже на складе. Ждём ровно столько, сколько идёт текущий
// запрос в МС: свои таймауты ставит клиент МС.
//
// Новый фон после остановки не стартует (stopped под мутексом), поэтому
// wg.Wait не гоняется с wg.Add: экспорт, начавшийся после Stop, просто не
// запускает пометки. Повторный вызов безопасен.
func (uc *ExportToExcelUseCase) Stop() {
	uc.mu.Lock()
	uc.stopped = true
	uc.mu.Unlock()

	uc.wg.Wait()
}

type ExportBarcodesToExcelUseCase struct {
	exporter   ExcelBarcodesExporter
	repository OrdersByIDsProvider
}

func NewExportBarcodesToExcelUseCase(exporter ExcelBarcodesExporter, repository OrdersByIDsProvider) *ExportBarcodesToExcelUseCase {
	return &ExportBarcodesToExcelUseCase{
		exporter:   exporter,
		repository: repository,
	}
}

func (uc *ExportBarcodesToExcelUseCase) GetMultipleOrdersBarcodes(ctx context.Context, ids []string) (string, error) {
	done := metrics.Track(trackPkg, "GetMultipleOrdersBarcodes")
	defer done()
	orders, err := uc.repository.GetOrdersByIDs(ctx, ids)
	if err != nil {
		slog.Info(fmt.Sprintf("getMultipleOrdersBarcodes could not get orders from repository: %s", err))
	}

	savepath, err := uc.exporter.ExportOrdersBarcodesToExcel(orders)
	if err != nil {
		slog.Info(fmt.Sprintf("getMultipleOrdersBarcodes could not create barcodes: %s", err))
	}

	return savepath, nil
}

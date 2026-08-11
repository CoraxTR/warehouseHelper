package usecase

import (
	"context"
	"log"
	"sync"
	"time"

	"warehouseHelper/internal/domain"
)

type ExcelExporter interface {
	ExportOrdersToExcel(orders []*domain.InternalOrder) (savepath string, err error)
}

type ExcelBarcodesExporter interface {
	ExportOrdersBarcodesToExcel(orders []*domain.InternalOrder) (savepath string, err error)
}

type OrdersShipper interface {
	SetOrderAsShippedToRefGo(ctx context.Context, href string) error
}

// OrdersShipmentEnsurer — гарант наличия корректной отгрузки у заказа.
// Реализуется OrderShipmentEnsurer (модуль msclient).
type OrdersShipmentEnsurer interface {
	EnsureOrderShipment(ctx context.Context, href string) error
}

// OrdersProvider — источник заказов для экспорта. Реализуется OrdersUseCase (модуль msclient).
type OrdersProvider interface {
	GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error)
}

// OrdersByHREFsProvider — выборка заказов по href для штрих-кодов. Реализуется OrdersUseCase (модуль msclient).
type OrdersByHREFsProvider interface {
	GetOrdersByHREFs(ctx context.Context, hrefs []string) ([]*domain.InternalOrder, error)
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
	defer func() {
		if cleanErr := uc.tempCleaner.CleanOlderThan(uc.tempCleanupMaxAge); cleanErr != nil {
			log.Printf("Failed to clean temp dir: %v", cleanErr)
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
	// не должна ждать обработки всех заказов. Контекст запроса тут не годится
	// (отменяется после ответа), поэтому берём context.Background().
	go uc.processOrdersShipments(context.Background(), orders)

	return summary, nil
}

// processOrdersShipments помечает заказы отгруженными в Реф и создаёт отгрузки
// для «Наличные»/«Терминал» в фоновом режиме после экспорта.
func (uc *ExportToExcelUseCase) processOrdersShipments(ctx context.Context, orders []*domain.InternalOrder) {
	wg := sync.WaitGroup{}

	for _, order := range orders {
		wg.Go(func() {
			err := uc.shipper.SetOrderAsShippedToRefGo(ctx, order.GetHREF())
			if err != nil {
				log.Printf("Error setting order as shipped: %s", err)
			}

			// Отгрузку обеспечиваем только для оплат «Наличные»/«Терминал»:
			// остальные заказы помечаем отгруженными как и раньше.
			if !domain.IsShippablePayment(order.GetPaymentMethod()) {
				return
			}

			err = uc.shipmentEnsurer.EnsureOrderShipment(ctx, order.GetHREF())
			if err != nil {
				log.Printf("Error ensuring order shipment: %s", err)
			}
		})
	}

	wg.Wait()
}

type ExportBarcodesToExcelUseCase struct {
	exporter   ExcelBarcodesExporter
	repository OrdersByHREFsProvider
}

func NewExportBarcodesToExcelUseCase(exporter ExcelBarcodesExporter, repository OrdersByHREFsProvider) *ExportBarcodesToExcelUseCase {
	return &ExportBarcodesToExcelUseCase{
		exporter:   exporter,
		repository: repository,
	}
}

func (uc *ExportBarcodesToExcelUseCase) GetMultipleOrdersBarcodes(ctx context.Context, hrefs []string) (string, error) {
	orders, err := uc.repository.GetOrdersByHREFs(ctx, hrefs)
	if err != nil {
		log.Printf("getMultipleOrdersBarcodes could not get orders from repository: %s", err)
	}

	savepath, err := uc.exporter.ExportOrdersBarcodesToExcel(orders)
	if err != nil {
		log.Printf("getMultipleOrdersBarcodes could not create barcodes: %s", err)
	}

	return savepath, nil
}

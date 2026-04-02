package usecase

import (
	"context"
	"log"
	"warehouseHelper/internal/domain"
)

type ExcelExporter interface {
	ExportOrdersToExcel(orders []*domain.InternalOrder) (savepath string, err error)
}

type ExcelBarcodesExporter interface {
	ExportOrdersBarcodesToExcel(orders []*domain.InternalOrder) (savepath string, err error)
}

type OrdersShipper interface {
	SetOrderAsShippedToRefGo(ctx context.Context, href string, refGoNumber string) error
}

type ExportToExcelUseCase struct {
	exporter ExcelExporter
	orders   *OrdersUseCase
	shipper  OrdersShipper
}

func NewExportToExcelUseCase(exporter ExcelExporter, orders *OrdersUseCase, shipper OrdersShipper) *ExportToExcelUseCase {
	return &ExportToExcelUseCase{
		exporter: exporter,
		orders:   orders,
		shipper:  shipper,
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

	for _, order := range orders {
		err := uc.shipper.SetOrderAsShippedToRefGo(ctx, order.GetHREF(), order.GetRefGoNumber())
		if err != nil {
			return nil, err
		}
	}

	return summary, nil
}

type ExportBarcodesToExcelUseCase struct {
	exporter   ExcelBarcodesExporter
	repository OrderRepository
}

func NewExportBarcodeBarcodesToExcelUseCase(exporter ExcelBarcodesExporter, repository OrderRepository) *ExportBarcodesToExcelUseCase {
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

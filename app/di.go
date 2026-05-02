package app

import (
	"net/http"
	"warehouseHelper/internal/config"
	myhttp "warehouseHelper/internal/delivery/http"
	"warehouseHelper/internal/exporter/excel"
	"warehouseHelper/internal/exporter/pdf"
	"warehouseHelper/internal/msratelimiter"
	"warehouseHelper/internal/repository/msapiclient"
	"warehouseHelper/internal/repository/postgres"
	"warehouseHelper/internal/usecase"
)

type DIContainer struct {
	// Инфраструктура
	config      *config.Config
	msrl        *msratelimiter.MoySkladOutRateLimiter
	msc         *msapiclient.MSAPIClient
	orepo       *postgres.PGClient
	msconv      *msapiclient.MSConverter
	xlexporter  *excel.ExcelExporter
	pdfexporter *pdf.PDFExporter

	// Юзкейсы
	syncUC          *usecase.SyncUseCase
	ordersUC        *usecase.OrdersUseCase
	excelExportUC   *usecase.ExportToExcelUseCase
	pdfExportUC     *usecase.ExportOrderPDFUseCase
	barcodeExportUC *usecase.ExportBarcodesToExcelUseCase

	// Хэндлеры
	mux      *http.ServeMux
	handlers *myhttp.Handler
}

func NewDIContainer() *DIContainer {
	return &DIContainer{}
}

func (d *DIContainer) Config() *config.Config {
	if d.config == nil {
		d.config = config.NewConfig()
	}

	return d.config
}
func (d *DIContainer) MSRateLimiter() *msratelimiter.MoySkladOutRateLimiter {
	if d.msrl == nil {
		d.msrl = msratelimiter.NewMoySkladOutRateLimiter(d.Config().MoySkladConfig)
	}

	return d.msrl
}

func (d *DIContainer) MSClient() *msapiclient.MSAPIClient {
	if d.msc == nil {
		d.msc = msapiclient.NewMSAPIClient(d.Config(), d.MSRateLimiter())
	}

	return d.msc
}

func (d *DIContainer) OrdersRepository() *postgres.PGClient {
	if d.orepo == nil {
		d.orepo = postgres.NewPGClient(d.Config().PGConfig)
	}

	return d.orepo
}

func (d *DIContainer) MSConverter() *msapiclient.MSConverter {
	if d.msconv == nil {
		d.msconv = msapiclient.NewMSConverter()
	}

	return d.msconv
}

func (d *DIContainer) SyncUC() *usecase.SyncUseCase {
	if d.syncUC == nil {
		d.syncUC = usecase.NewSyncUsecase(d.MSClient(), d.OrdersRepository(), d.MSConverter(), d.Config().RefGoConfig)
	}

	return d.syncUC
}
func (d *DIContainer) OrdersUC() *usecase.OrdersUseCase {
	if d.ordersUC == nil {
		d.ordersUC = usecase.NewOrdersUseCase(d.OrdersRepository(), d.MSClient(), d.MSConverter())
	}

	return d.ordersUC
}

func (d *DIContainer) ExcelExporter() usecase.ExcelExporter {
	if d.xlexporter == nil {
		d.xlexporter = excel.NewExcelExporter()
	}

	return d.xlexporter
}

func (d *DIContainer) ExcelBarcodeExporter() usecase.ExcelBarcodesExporter {
	if d.xlexporter == nil {
		d.xlexporter = excel.NewExcelExporter()
	}

	return d.xlexporter
}

func (d *DIContainer) ExcelExportUC() *usecase.ExportToExcelUseCase {
	if d.excelExportUC == nil {
		d.excelExportUC = usecase.NewExportToExcelUseCase(d.ExcelExporter(), d.OrdersUC(), d.MSClient())
	}

	return d.excelExportUC
}

func (d *DIContainer) PDFExporter() usecase.PDFExporter {
	if d.pdfexporter == nil {
		d.pdfexporter = pdf.NewPDFExporter()
	}

	return d.pdfexporter
}

func (d *DIContainer) PdfExportUC() *usecase.ExportOrderPDFUseCase {
	if d.pdfExportUC == nil {
		d.pdfExportUC = usecase.NewExportOrderPDFUseCase(d.MSClient(), d.PDFExporter())
	}

	return d.pdfExportUC
}

func (d *DIContainer) BarcodeExportUC() *usecase.ExportBarcodesToExcelUseCase {
	if d.barcodeExportUC == nil {
		d.barcodeExportUC = usecase.NewExportBarcodesToExcelUseCase(d.ExcelBarcodeExporter(), d.OrdersRepository())
	}

	return d.barcodeExportUC
}

func (d *DIContainer) Handler() *myhttp.Handler {
	if d.handlers == nil {
		d.handlers = myhttp.NewHandler(d.SyncUC(), d.OrdersUC(), d.ExcelExportUC(), d.PdfExportUC(), d.BarcodeExportUC())
	}

	return d.handlers
}

func (d *DIContainer) MUX() *http.ServeMux {
	if d.mux == nil {
		d.mux = myhttp.NewRouter(d.Handler())
	}

	return d.mux
}

package app

import (
	"net/http"
	"warehouseHelper/internal/config"
	myhttp "warehouseHelper/internal/delivery/http"
	"warehouseHelper/internal/exporter/excel"
	"warehouseHelper/internal/exporter/pdf"
	"warehouseHelper/internal/msWorkerpool"
	"warehouseHelper/internal/repository/msapiclient"
	orderscache "warehouseHelper/internal/repository/ordercache"
	"warehouseHelper/internal/repository/pdfpreloader"
	"warehouseHelper/internal/repository/postgres"
	"warehouseHelper/internal/usecase"
)

type DIContainer struct {
	// Инфраструктура
	config       *config.Config
	msrl         *msWorkerpool.MSOutRateLimiter
	wp           *msWorkerpool.MSWorkerPool
	msc          *msapiclient.MSAPIClient
	orepo        *postgres.PGClient
	msconv       *msapiclient.MSConverter
	xlxsexporter *excel.ExcelExporter
	pdfexporter  *pdf.PDFExporter
	ordercache   *orderscache.OrderCache
	pdfpreloader *pdfpreloader.PDFPreloader

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
func (d *DIContainer) MSRateLimiter() *msWorkerpool.MSOutRateLimiter {
	if d.msrl == nil {
		d.msrl = msWorkerpool.NewMSOutRateLimiter(d.Config().MSConfig)
	}

	return d.msrl
}

func (d *DIContainer) MSWorkerPool() *msWorkerpool.MSWorkerPool {
	if d.wp == nil {
		d.wp = msWorkerpool.NewMSWorkerPool(d.Config().MSConfig)
	}

	return d.wp
}

func (d *DIContainer) MSClient() *msapiclient.MSAPIClient {
	if d.msc == nil {
		d.msc = msapiclient.NewMSAPIClient(d.Config(), d.MSWorkerPool(), d.OrderCache())
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

func (d *DIContainer) OrderCache() *orderscache.OrderCache {
	if d.ordercache == nil {
		d.ordercache = orderscache.NewOrderCache()
	}

	return d.ordercache
}

func (d *DIContainer) PdfPreloader() *pdfpreloader.PDFPreloader {
	if d.pdfpreloader == nil {
		d.pdfpreloader = pdfpreloader.NewPDFPreloader(d.MSClient())
	}

	return d.pdfpreloader
}

func (d *DIContainer) SyncUC() *usecase.SyncUseCase {
	if d.syncUC == nil {
		d.syncUC = usecase.NewSyncUsecase(d.MSClient(), d.OrdersRepository(), d.MSConverter(),
			d.Config().RefGoConfig, d.PdfPreloader())
	}

	return d.syncUC
}
func (d *DIContainer) OrdersUC() *usecase.OrdersUseCase {
	if d.ordersUC == nil {
		d.ordersUC = usecase.NewOrdersUseCase(d.OrdersRepository(), d.MSClient(), d.MSConverter(),
			d.PdfPreloader())
	}

	return d.ordersUC
}

func (d *DIContainer) ExcelExporter() usecase.ExcelExporter {
	if d.xlxsexporter == nil {
		d.xlxsexporter = excel.NewExcelExporter()
	}

	return d.xlxsexporter
}

func (d *DIContainer) ExcelBarcodeExporter() usecase.ExcelBarcodesExporter {
	if d.xlxsexporter == nil {
		d.xlxsexporter = excel.NewExcelExporter()
	}

	return d.xlxsexporter
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
		d.pdfExportUC = usecase.NewExportOrderPDFUseCase(d.MSClient(), d.PDFExporter(), d.PdfPreloader())
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

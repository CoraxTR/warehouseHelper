package app

import (
	"net/http"
	"warehouseHelper/internal/config"
	myhttp "warehouseHelper/internal/delivery/http"
	gucase "warehouseHelper/internal/goods/usecase"
	"warehouseHelper/internal/msclient/client"
	orderscache "warehouseHelper/internal/msclient/ordercache"
	"warehouseHelper/internal/msclient/pdfpreloader"
	msucase "warehouseHelper/internal/msclient/usecase"
	"warehouseHelper/internal/msclient/workerpool"
	msu "warehouseHelper/internal/mssuppliers/usecase"
	photostore "warehouseHelper/internal/qrcodes/photostore"
	qucase "warehouseHelper/internal/qrcodes/usecase"
	"warehouseHelper/internal/refgo/export/excel"
	"warehouseHelper/internal/refgo/export/pdf"
	"warehouseHelper/internal/refgo/registry"
	rgucase "warehouseHelper/internal/refgo/usecase"
	"warehouseHelper/internal/repository/postgres"
	"warehouseHelper/internal/telegram"
	tempcleaner "warehouseHelper/internal/tempcleaner"
	"warehouseHelper/internal/tempdir"
	wucase "warehouseHelper/internal/wiki/usecase"
)

type DIContainer struct {
	// Инфраструктура
	config       *config.Config
	msrl         *workerpool.MSOutRateLimiter
	wp           *workerpool.MSWorkerPool
	msc          *client.MSAPIClient
	orepo        *postgres.PGClient
	msconv       *client.MSConverter
	xlxsexporter *excel.ExcelExporter
	pdfexporter  *pdf.PDFExporter
	ordercache   *orderscache.OrderCache
	pdfpreloader *pdfpreloader.PDFPreloader
	tempcleaner  *tempcleaner.TempCleaner
	xlsximporter rgucase.RefGoXlsxParser
	tg           *telegram.Notifier

	// Юзкейсы
	syncUC          *msucase.SyncUseCase
	ordersUC        *msucase.OrdersUseCase
	shipmentEnsurer *msucase.OrderShipmentEnsurer
	excelExportUC   *rgucase.ExportToExcelUseCase
	pdfExportUC     *rgucase.ExportOrderPDFUseCase
	barcodeExportUC *rgucase.ExportBarcodesToExcelUseCase
	refGoCheckUC    *rgucase.RefGoCheckAgainstUseCase
	wikiUC          *wucase.WikiUseCase
	goodsUC         *gucase.GoodsUseCase
	qrUC            *qucase.QRUseCase
	msUC            *msu.MSSuppliersUseCase

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
func (d *DIContainer) MSRateLimiter() *workerpool.MSOutRateLimiter {
	if d.msrl == nil {
		d.msrl = workerpool.NewMSOutRateLimiter(d.Config().MSConfig)
	}

	return d.msrl
}

func (d *DIContainer) MSWorkerPool() *workerpool.MSWorkerPool {
	if d.wp == nil {
		d.wp = workerpool.NewMSWorkerPool(d.Config().MSConfig)
	}

	return d.wp
}

func (d *DIContainer) MSClient() *client.MSAPIClient {
	if d.msc == nil {
		d.msc = client.NewMSAPIClient(d.Config(), d.MSWorkerPool(), d.OrderCache())
	}

	return d.msc
}

func (d *DIContainer) OrdersRepository() *postgres.PGClient {
	if d.orepo == nil {
		d.orepo = postgres.NewPGClient(d.Config().PGConfig)
	}

	return d.orepo
}

func (d *DIContainer) MSConverter() *client.MSConverter {
	if d.msconv == nil {
		d.msconv = client.NewMSConverter()
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

func (d *DIContainer) SyncUC() *msucase.SyncUseCase {
	if d.syncUC == nil {
		d.syncUC = msucase.NewSyncUsecase(d.MSClient(), d.OrdersRepository(), d.MSConverter(),
			d.Config().RefGoConfig, d.PdfPreloader())
	}

	return d.syncUC
}
func (d *DIContainer) OrdersUC() *msucase.OrdersUseCase {
	if d.ordersUC == nil {
		d.ordersUC = msucase.NewOrdersUseCase(d.OrdersRepository(), d.MSClient(), d.MSConverter(),
			d.PdfPreloader())
	}

	return d.ordersUC
}

func (d *DIContainer) ExcelExporter() rgucase.ExcelExporter {
	if d.xlxsexporter == nil {
		d.xlxsexporter = excel.NewExcelExporter()
	}

	return d.xlxsexporter
}

func (d *DIContainer) ExcelBarcodeExporter() rgucase.ExcelBarcodesExporter {
	if d.xlxsexporter == nil {
		d.xlxsexporter = excel.NewExcelExporter()
	}

	return d.xlxsexporter
}

func (d *DIContainer) TempCleaner() rgucase.TempCleaner {
	if d.tempcleaner == nil {
		d.tempcleaner = tempcleaner.NewTempCleaner(tempdir.Dir)
	}

	return d.tempcleaner
}

func (d *DIContainer) XlsxImporter() rgucase.RefGoXlsxParser {
	if d.xlsximporter == nil {
		d.xlsximporter = registry.NewxlsxImporter()
	}

	return d.xlsximporter
}

func (d *DIContainer) TelegramNotifier() *telegram.Notifier {
	if d.tg == nil {
		d.tg = telegram.NewNotifier(d.Config().TelegramConfig)
	}

	return d.tg
}

func (d *DIContainer) ShipmentEnsurer() *msucase.OrderShipmentEnsurer {
	if d.shipmentEnsurer == nil {
		d.shipmentEnsurer = msucase.NewOrderShipmentEnsurer(d.MSClient(), d.TelegramNotifier())
	}

	return d.shipmentEnsurer
}

func (d *DIContainer) ExcelExportUC() *rgucase.ExportToExcelUseCase {
	if d.excelExportUC == nil {
		d.excelExportUC = rgucase.NewExportToExcelUseCase(d.ExcelExporter(), d.OrdersUC(), d.MSClient(),
			d.ShipmentEnsurer(), d.TempCleaner(), d.Config().TempCleanupMaxAge)
	}

	return d.excelExportUC
}

func (d *DIContainer) PDFExporter() rgucase.PDFExporter {
	if d.pdfexporter == nil {
		d.pdfexporter = pdf.NewPDFExporter()
	}

	return d.pdfexporter
}

func (d *DIContainer) PdfExportUC() *rgucase.ExportOrderPDFUseCase {
	if d.pdfExportUC == nil {
		d.pdfExportUC = rgucase.NewExportOrderPDFUseCase(d.MSClient(), d.PDFExporter(), d.PdfPreloader())
	}

	return d.pdfExportUC
}

func (d *DIContainer) BarcodeExportUC() *rgucase.ExportBarcodesToExcelUseCase {
	if d.barcodeExportUC == nil {
		d.barcodeExportUC = rgucase.NewExportBarcodesToExcelUseCase(d.ExcelBarcodeExporter(), d.OrdersRepository())
	}

	return d.barcodeExportUC
}

func (d *DIContainer) RefGoCheckAgainstUC() *rgucase.RefGoCheckAgainstUseCase {
	if d.refGoCheckUC == nil {
		d.refGoCheckUC = rgucase.NewRefGoCheckAgainstUseCase(d.OrdersRepository(), d.XlsxImporter(), d.Config().RefGoConfig)
	}

	return d.refGoCheckUC
}

// WikiUC — сценарий работы с вики-страницами; PGClient реализует WikiRepository.
func (d *DIContainer) WikiUC() *wucase.WikiUseCase {
	if d.wikiUC == nil {
		d.wikiUC = wucase.NewWikiUseCase(d.OrdersRepository())
	}

	return d.wikiUC
}

// GoodsUC — сценарий «выгрузить дерево папок товаров из МС»; MSClient реализует
// gucase.ProductFolderClient.
func (d *DIContainer) GoodsUC() *gucase.GoodsUseCase {
	if d.goodsUC == nil {
		d.goodsUC = gucase.NewGoodsUseCase(d.MSClient())
	}

	return d.goodsUC
}

// QRUC — сценарии модуля «Честный знак»; PGClient реализует qucase.QRRepository.
func (d *DIContainer) QRUC() *qucase.QRUseCase {
	if d.qrUC == nil {
		qrConfig := d.Config().QRConfig
		d.qrUC = qucase.NewQRUseCase(d.OrdersRepository(), photostore.NewStore(qrConfig.PhotosDir), qrConfig.PhotosDir, qrConfig.PhotosMaxAge)
	}

	return d.qrUC
}

// SuppliersUC — сценарии справочника поставщиков «МойСклад»; PGClient реализует
// msu.MSSuppliersRepository, MSClient — msu.CounterpartyClient, WikiUseCase —
// msu.WikiSupplierSynchronizer (страница вики поставщика создаётся/обновляется синком).
func (d *DIContainer) SuppliersUC() *msu.MSSuppliersUseCase {
	if d.msUC == nil {
		d.msUC = msu.NewMSSuppliersUseCase(d.OrdersRepository(), d.MSClient(), d.WikiUC())
	}

	return d.msUC
}

func (d *DIContainer) Handler() *myhttp.Handler {
	if d.handlers == nil {
		d.handlers = myhttp.NewHandler(d.SyncUC(), d.OrdersUC(), d.ExcelExportUC(), d.PdfExportUC(), d.BarcodeExportUC(), d.RefGoCheckAgainstUC(), d.WikiUC(), d.GoodsUC(), d.QRUC(), d.SuppliersUC())
	}

	return d.handlers
}

func (d *DIContainer) MUX() *http.ServeMux {
	if d.mux == nil {
		d.mux = myhttp.NewRouter(d.Handler())
	}

	return d.mux
}

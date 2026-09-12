package app

import (
	"context"
	"net/http"
	asucase "warehouseHelper/internal/averagesales/usecase"
	aucase "warehouseHelper/internal/avgweight/usecase"
	cphotos "warehouseHelper/internal/complaints/photostore"
	cucase "warehouseHelper/internal/complaints/usecase"
	"warehouseHelper/internal/config"
	ducecase "warehouseHelper/internal/daystate/usecase"
	myhttp "warehouseHelper/internal/delivery/http"
	gucase "warehouseHelper/internal/goods/usecase"
	"warehouseHelper/internal/msclient/client"
	orderscache "warehouseHelper/internal/msclient/ordercache"
	"warehouseHelper/internal/msclient/pdfpreloader"
	msucase "warehouseHelper/internal/msclient/usecase"
	"warehouseHelper/internal/msclient/workerpool"
	mordersuc "warehouseHelper/internal/msorders/usecase"
	msu "warehouseHelper/internal/mssuppliers/usecase"
	oucase "warehouseHelper/internal/ordercoeff/usecase"
	"warehouseHelper/internal/pdfexport"
	"warehouseHelper/internal/qrcodes/photostore"
	qucase "warehouseHelper/internal/qrcodes/usecase"
	rucase "warehouseHelper/internal/receiving/usecase"
	"warehouseHelper/internal/refgo/export/excel"
	"warehouseHelper/internal/refgo/registry"
	rgucase "warehouseHelper/internal/refgo/usecase"
	"warehouseHelper/internal/repository/postgres"
	rwucase "warehouseHelper/internal/reservewatch/usecase"
	retucase "warehouseHelper/internal/returns/usecase"
	sucase "warehouseHelper/internal/stock/usecase"
	stockws "warehouseHelper/internal/stock/ws"
	"warehouseHelper/internal/telegram"
	"warehouseHelper/internal/tempcleaner"
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
	pdfservice   *pdfexport.Service
	ordercache   *orderscache.OrderCache
	pdfpreloader *pdfpreloader.PDFPreloader
	tempcleaner  *tempcleaner.TempCleaner
	xlsximporter rgucase.RefGoXlsxParser
	tg           *telegram.Notifier
	stockStatus  *stockStatusNotifier
	complaintsUC *cucase.UseCase

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
	averageSalesUC  *asucase.UseCase
	orderCoeffUC    *oucase.UseCase
	dayStateUC      *ducecase.UseCase
	qrUC            *qucase.QRUseCase
	msUC            *msu.MSSuppliersUseCase
	msOrdersUC      *mordersuc.UseCase
	msFormsUC       *mordersuc.FormsUseCase
	returnsUC       *retucase.UseCase
	reserveWatchUC  *rwucase.UseCase
	stockUC         *sucase.StockUseCase
	stockHub        *stockws.Hub
	receiveBarcodes *rucase.BarcodeEditor
	receivingUC     *rucase.ReceivingUseCase
	avgWeightUC     *aucase.UseCase

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

// StockStatusNotifier — уведомления о смене наличия в общий канал telegram
// (имя товара — из каталога; без TG_COMMON_CHAT_ID — молчаливый no-op).
func (d *DIContainer) StockStatusNotifier() *stockStatusNotifier {
	if d.stockStatus == nil {
		d.stockStatus = NewStockStatusNotifier(d.TelegramNotifier(), d.GoodsUC())
	}

	return d.stockStatus
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

// PDFService — печать бланков заказов (нижний слой pdfexport): бланки качает
// MSClient (печатный шаблон МС), слияние пачки — экспортёр pdfcpu. Пакет общий
// для модулей refgo и msorders, поэтому связка живёт здесь.
func (d *DIContainer) PDFService() *pdfexport.Service {
	if d.pdfservice == nil {
		d.pdfservice = pdfexport.NewService(d.MSClient(), pdfexport.NewExporter())
	}

	return d.pdfservice
}

func (d *DIContainer) PdfExportUC() *rgucase.ExportOrderPDFUseCase {
	if d.pdfExportUC == nil {
		d.pdfExportUC = rgucase.NewExportOrderPDFUseCase(d.PDFService(), d.PdfPreloader())
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

// GoodsUC — сценарии «Продукция»: дерево папок/товаров из МС и выгрузка
// каталога; MSClient реализует gucase.ProductFolderClient и gucase.ProductClient,
// PGClient — gucase.ProductsRepository.
func (d *DIContainer) GoodsUC() *gucase.GoodsUseCase {
	if d.goodsUC == nil {
		d.goodsUC = gucase.NewGoodsUseCase(d.MSClient(), d.MSClient(), d.OrdersRepository(), d.WikiUC())
	}

	return d.goodsUC
}

// AverageSalesUC — сценарии «Средние продажи»: обороты из отчёта прибыльности МС
// и средние продажи по окну. PGClient реализует aucase.Repository, MSClient —
// aucase.SalesClient, GoodsUC — aucase.ProductReader. GoodsUC получает обратную
// ссылку для фонового бэкфилла (интерфейс в goods, цикла пакетов нет).
func (d *DIContainer) AverageSalesUC() *asucase.UseCase {
	if d.averageSalesUC == nil {
		d.averageSalesUC = asucase.NewUseCase(d.OrdersRepository(), d.MSClient(), d.GoodsUC())
		d.GoodsUC().SetTurnoverBackfiller(d.averageSalesUC)
	}

	return d.averageSalesUC
}

// OrderCoeffUC — сценарии «Коэффициент изменения заказа»: приём событий
// от модулей-владельцев фактов и чтение коэффициентов по интервалам.
// PGClient реализует oucase.Repository, GoodsUC — oucase.ProductReader.
func (d *DIContainer) OrderCoeffUC() *oucase.UseCase {
	if d.orderCoeffUC == nil {
		d.orderCoeffUC = oucase.NewUseCase(d.OrdersRepository(), d.GoodsUC())
	}

	return d.orderCoeffUC
}

// DayStateUC — сценарии состояния товара по дням: утренний снапшот из
// product_stock, пересчёт строки по событиям стока (шов StockUC), доступность
// из календаря «Доступность товаров», эмитенты фактов в ordercoeff.
// PGClient реализует ducecase.Repository; GoodsUC — ducecase.CatalogProvider
// (каталог для страниц); OrderCoeffUC — SoldOutNotifier / UnavailableNotifier /
// SoldOutRollbackNotifier (методы SoldOut, Unavailable, RollbackSoldOut).
func (d *DIContainer) DayStateUC() *ducecase.UseCase {
	if d.dayStateUC == nil {
		d.dayStateUC = ducecase.NewUseCase(d.OrdersRepository(), d.GoodsUC(), d.OrderCoeffUC(), d.OrderCoeffUC(), d.OrderCoeffUC(), d.StockStatusNotifier())
	}

	return d.dayStateUC
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
		d.msUC = msu.NewMSSuppliersUseCase(d.OrdersRepository(), d.MSClient(), d.WikiUC(), d.ReceiveBarcodes())
	}

	return d.msUC
}

// ReceiveBarcodes — сценарии ввода внешних кодов поставщика (приёмка);
// PGClient реализует rucase.BarcodeRepository / SupplierReader / CatalogReader,
// WikiUC — rucase.WikiBarcodeRef.
func (d *DIContainer) ReceiveBarcodes() *rucase.BarcodeEditor {
	if d.receiveBarcodes == nil {
		d.receiveBarcodes = rucase.NewBarcodeEditor(d.OrdersRepository(), d.OrdersRepository(), d.OrdersRepository(), d.WikiUC())
	}

	return d.receiveBarcodes
}

// AvgWeightUC — сценарии модуля «Средний вес»: приёмка передаёт единичные
// веса (RecordWeights), модуль пишет их, обрезает историю до лимита и через
// каталог (GoodsUC) и вики (WikiUC) обновляет средний вес. PGClient — aucase.Repository,
// GoodsUC — aucase.ProductWeightUpdater, WikiUC — aucase.WikiWeightUpdater.
func (d *DIContainer) AvgWeightUC() *aucase.UseCase {
	if d.avgWeightUC == nil {
		d.avgWeightUC = aucase.NewUseCase(d.OrdersRepository(), d.GoodsUC(), d.WikiUC(), d.Config().WeightsHistoryLimit)
	}

	return d.avgWeightUC
}

// ReceivingUC — сценарии приёмки (кеш поставщика, резолв сканов, сохранение);
// PGClient реализует rucase.ReceiveRepository, StockUC — rucase.StockAccepter,
// AvgWeightUC — rucase.WeightRecorder.
func (d *DIContainer) ReceivingUC() *rucase.ReceivingUseCase {
	if d.receivingUC == nil {
		d.receivingUC = rucase.NewReceivingUseCase(d.OrdersRepository(), d.StockUC(), d.AvgWeightUC())
	}

	return d.receivingUC
}

// StockHub — вебсокет-хаб модуля «Сроки» (клиенты обеих страниц).
func (d *DIContainer) StockHub() *stockws.Hub {
	if d.stockHub == nil {
		d.stockHub = stockws.NewHub()
	}

	return d.stockHub
}

// StockUC — сценарии модуля «Сроки»: кэш остатков и ручные скидки.
// PGClient реализует sucase.Repository, StockHub — sucase.Publisher,
// DayStateUC — sucase.DayStateRecorder (шов состояния по дням).
// Кэш прогревается при старте (app.initStockCache); шов «каталог изменился»:
// goods после выгрузки/правки товаров перечитывает кэш через ReloadCatalog
// (новые позиции видны на страницах без рестарта).
func (d *DIContainer) StockUC() *sucase.StockUseCase {
	if d.stockUC == nil {
		d.stockUC = sucase.NewStockUseCase(d.OrdersRepository(), d.StockHub(), d.DayStateUC(), d.TelegramNotifier())
		// GoodsUC() создаётся здесь же (DayStateUC → GoodsUC), рекурсии нет:
		// goods не тянет StockUC. Вызов до первого запроса — слушатель на месте.
		d.GoodsUC().SetCatalogChangeListener(d.stockUC)
	}

	return d.stockUC
}

// ComplaintsUC — сценарии модуля «Жалобы»: обращения клиентов с фото
// (zip-архивы), статусы, уведомления в common_chat. PGClient реализует
// ComplaintRepository и CatalogReader (GetProductsByIDs), photostore.Store —
// PhotoStore, telegram.Notifier — ComplaintNotifier.
func (d *DIContainer) ComplaintsUC() *cucase.UseCase {
	if d.complaintsUC == nil {
		cc := d.Config().ComplaintsConfig
		d.complaintsUC = cucase.NewUseCase(
			d.OrdersRepository(),
			d.OrdersRepository(),
			cphotos.NewStore(cc.PhotosDir),
			d.TelegramNotifier(),
			cc.PublicURL,
		)
	}

	return d.complaintsUC
}

// MSOrdersUC — сценарии раздела «Заказы» МойСклад: поиск заказа по номеру и
// детальная страница заказа (подбор). Схемы БД у модуля нет — MSClient
// реализует mordersuc.OrderClient, каталог склада подключается адаптером
// (PGClient отдаёт товары типом receiving.ProductRef, модулю нужен свой).
// Шов списания сроков — StockUC (PickStock): интерфейс совпадает дословно.
func (d *DIContainer) MSOrdersUC() *mordersuc.UseCase {
	if d.msOrdersUC == nil {
		d.msOrdersUC = mordersuc.NewUseCase(d.MSClient(), orderCatalogAdapter{pg: d.OrdersRepository()}, d.StockUC())
	}

	return d.msOrdersUC
}

// MSFormsUC — сценарии печати бланков заказов (страница «Печать бланков»):
// список заказов МС за день + слитый PDF по выделенным. Печать — нижний слой
// pdfexport (PDFService), поэтому шов печати отдельный от подбора.
func (d *DIContainer) MSFormsUC() *mordersuc.FormsUseCase {
	if d.msFormsUC == nil {
		d.msFormsUC = mordersuc.NewFormsUseCase(d.MSClient(), d.PDFService())
	}

	return d.msFormsUC
}

// orderCatalogAdapter — конвертация каталога на границе DI: PGClient
// реализует чужой контракт (receiving), msorders объявляет свой.
type orderCatalogAdapter struct {
	pg *postgres.PGClient
}

func (a orderCatalogAdapter) LoadCatalogProductsByCodes(ctx context.Context, codes []string) (map[string]mordersuc.CatalogProduct, error) {
	found, err := a.pg.LoadCatalogProductsByCodes(ctx, codes)
	if err != nil {
		return nil, err
	}

	out := make(map[string]mordersuc.CatalogProduct, len(found))
	for code, p := range found {
		out[code] = mordersuc.CatalogProduct{
			ProductID:    p.ProductID,
			InternalCode: p.InternalCode,
			Weighted:     p.Weighted,
		}
	}

	return out, nil
}

// LoadProductAverageWeights — средние веса штучных товаров (кг) по products.id;
// PGClient отдаёт примитивы, msorders использует их как есть.
func (a orderCatalogAdapter) LoadProductAverageWeights(ctx context.Context, productIDs []string) (map[string]float64, error) {
	return a.pg.LoadProductAverageWeights(ctx, productIDs)
}

// ReturnsUC — «Возврат в продажу»: наблюдатель журнала действий МС (audit)
// и страница расформирования отменённых/урезанных заказов. PGClient
// реализует Repo (return_events/return_cursor) и Catalog (чтение products),
// MSClient — AuditAPI (audit/positions), StockUC принимает остатки,
// TelegramNotifier шлёт сообщения складу и удаляет их после обработки.
func (d *DIContainer) ReturnsUC() *retucase.UseCase {
	if d.returnsUC == nil {
		msc := d.Config().MSConfig
		d.returnsUC = retucase.NewUseCase(
			retucase.Config{
				CancelledStateID: msc.CancelledStateID,
				SkipSources:      msc.SkipAuditSources,
				PublicURL:        d.Config().PublicURL,
			},
			d.MSClient(),
			d.OrdersRepository(),
			d.OrdersRepository(),
			d.StockUC(),
			d.TelegramNotifier(),
			d.MSClient(),
		)
	}

	return d.returnsUC
}

// ReserveWatchUC — «Контроль резервов заказов»: раз в минуту лист заказов
// в рабочих статусах с плановой отгрузкой в окне, сверка резерва позиций
// с quantity, уведомления в чат склада с кнопкой «Подобрать». PGClient
// реализует Repo (reserve_notices) и Catalog (чтение products), MSClient —
// Orders (лист окна + позиции), TelegramNotifier шлёт/удаляет сообщения.
func (d *DIContainer) ReserveWatchUC() *rwucase.UseCase {
	if d.reserveWatchUC == nil {
		msc := d.Config().MSConfig
		d.reserveWatchUC = rwucase.NewUseCase(
			rwucase.Config{
				StateIDs:  msc.ReserveWatchStates,
				PublicURL: d.Config().PublicURL,
			},
			d.MSClient(),
			d.OrdersRepository(),
			d.OrdersRepository(),
			d.TelegramNotifier(),
		)
	}

	return d.reserveWatchUC
}

func (d *DIContainer) Handler() *myhttp.Handler {
	if d.handlers == nil {
		d.handlers = myhttp.NewHandler(d.SyncUC(), d.OrdersUC(), d.ExcelExportUC(), d.PdfExportUC(), d.BarcodeExportUC(), d.RefGoCheckAgainstUC(), d.WikiUC(), d.GoodsUC(), d.DayStateUC(), d.QRUC(), d.SuppliersUC(), d.StockUC(), d.StockHub(), d.ReceiveBarcodes(), d.ReceivingUC(), d.ComplaintsUC(), d.MSOrdersUC(), d.MSFormsUC(), d.ReturnsUC())
	}

	return d.handlers
}

func (d *DIContainer) MUX() *http.ServeMux {
	if d.mux == nil {
		d.mux = myhttp.NewRouter(d.Handler())
	}

	return d.mux
}

// Close останавливает и закрывает созданные зависимости в порядке «от
// внешнего к внутреннему»: сначала те, кто сам что-то качает/пишет (и ждёт
// свои горутины), затем фоновые задачи модулей, пул БД — последним (им
// пользуются все). Порядок = корректность: каждый шаг останавливает того, кто
// может дёрнуть закрываемое ниже.
//
// Проверки на nil обязательны: геттеры ленивые и создают объект при вызове,
// поэтому звать их на остановке нельзя — это построило бы зависимости
// (и сходило в МС/БД) прямо в момент закрытия. Нет поля — значит, объект не
// создавался и закрывать нечего.
//
// Возвращает error для единого контракта с App.Shutdown (остановки сейчас
// ошибок не возвращают); если появятся шаги, которые могут упасть, —
// собирать их здесь через errors.Join.
func (d *DIContainer) Close() error {
	// 1. Предзагрузка PDF — первой и с ожиданием: StopPreloading ждёт воркеров,
	// а они качают бланки через воркерпул МС. Закрой пул раньше — докачка упадёт
	// с «клиент остановлен» (и оставит битые файлы в temp).
	if d.pdfpreloader != nil {
		d.pdfpreloader.StopPreloading()
	}

	// 2. Бэкфилл средних продаж: свой ctx от context.Background(), поэтому
	// отмена корневого ctx приложения (Shutdown) его НЕ гасит — останавливать
	// явно и раньше воркерпула МС: Stop отменяет задачи и ждёт горутины, а они
	// ходят в МС и пишут в БД. Иначе выживший бэкфилл упёрся бы в закрытый
	// воркерпул и закрытый пул БД.
	if d.averageSalesUC != nil {
		d.averageSalesUC.Stop()
	}

	// 3. Воркерпул МС: новых задач не принимает (Submit отдаёт ErrPoolStopped),
	// воркеров дожидается. Кто мог бы его дёрнуть, к этому шагу остановлен.
	if d.wp != nil {
		d.wp.Stop()
	}

	// 4. Пул БД — последним: им пользуются все остальные, после него закрывать
	// ничего не остаётся (новые запросы вернут «pool closed»).
	if d.orepo != nil {
		d.orepo.Close()
	}

	return nil
}

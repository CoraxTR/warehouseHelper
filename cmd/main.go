package main

import (
	"log"
	"net/http"

	"warehouseHelper/internal/config"
	myhttp "warehouseHelper/internal/delivery/http"
	"warehouseHelper/internal/exporter/excel"
	"warehouseHelper/internal/exporter/pdf"
	"warehouseHelper/internal/repository/msapiclient"
	"warehouseHelper/internal/repository/postgres"
	"warehouseHelper/internal/usecase"
)

func main() {
	cfg := config.LoadConfig()

	msAPIClient := msapiclient.NewMoySkladAPIClient(cfg)

	msAPIConverter := new(msapiclient.MoySkladConverter)

	db := postgres.NewPGClient(cfg.PGConfig)

	excelExporter := excel.NewExcelExporter()
	pdfExporter := pdf.NewPDFExporter()

	fs := http.FileServer(http.Dir("../internal/delivery/web/static"))

	syncUC := usecase.SyncUseCase{
		MSAPIClinet: msAPIClient,
		Converter:   msAPIConverter,
		DBClient:    db,
		Config:      cfg.RefGoConfig,
	}
	ordersUC := usecase.NewOrdersUseCase(db, msAPIClient, msAPIConverter)
	excelExportUC := usecase.NewExportToExcelUseCase(excelExporter, ordersUC, msAPIClient)
	pdfExportUC := usecase.NewExportOrderPDFUseCase(msAPIClient, pdfExporter)
	barcodeExportUC := usecase.NewExportBarcodeBarcodesToExcelUseCase(excelExporter, db)

	handler := myhttp.NewHandler(&syncUC, ordersUC, excelExportUC, pdfExportUC, barcodeExportUC)

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/", handler.Home)
	mux.HandleFunc("/sync", handler.Sync)     // POST
	mux.HandleFunc("/orders", handler.Orders) // GET
	mux.HandleFunc("/export", handler.ExportToExcel)
	mux.HandleFunc("/orders/update", handler.UpdateOrders) // POST
	mux.HandleFunc("/download", handler.DownloadFile)
	mux.HandleFunc("/update-from-ms", handler.UpdateFromMS) // POST
	mux.HandleFunc("/print-form", handler.PrintForm)
	mux.HandleFunc("/print-multiple-forms", handler.PrintMultipleForms) // POST
	mux.HandleFunc("/orders/delete", handler.DeleteOrder)               // DELETE
	mux.HandleFunc("/print-barcodes", handler.PrintBarcodes)            // POST

	log.Println("Сервер запущен на http://localhost:8080")

	err := http.ListenAndServe(":8080", mux)
	if err != nil {
		log.Fatal("Ошибка запуска сервера:", err)
	}
	//TODO: Добавить ленивую инициализацию
	//TODO: Добавить конфиг для сервера, обозначить таймауты
	//TODO: Добавить graceful shutdown с перехватом сигналов и закрытием ресурсов
	//TODO: Добавить автосоздание отгрузок в МС при выгрузке заказов
	//TODO: Добавить рейлимитер для МС как отдельное Runnable
	//TODO: Добавить в рейтлимитер возможность конфигурировать несколько АПИ-ключей, чтобы ускорить обработку запросов
	//TODO: Добавить ленивую подгрузку pdf-бланков заказа после выгрузки
	//TODO: Добавить в схему БД таймстамп последнего изменения при выгрузке заказа
	//TODO: Добавить проверку на наличие изменений в заказе в МС с момента выгрузки, чтобы не делать повторные запросы на уже актуализированные заказы
	//TODO: Переделать отдаваемые файлы в темп, вместо корневой
	//TODO: Добавить параллеллизм в обработку заказов
	//TODO: Обернуть работу с РефГо в отдельный Runnable
}

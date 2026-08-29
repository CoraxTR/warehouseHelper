package http

import (
	"net/http"
)

func NewRouter(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()

	fs := http.FileServer(http.Dir("../internal/delivery/web/static"))

	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/", h.Home)
	mux.HandleFunc("/sync", h.Sync)                     // POST
	mux.HandleFunc("/refgo", h.RefGoPage)               // GET
	mux.HandleFunc("/refgo-check", h.RefGoCheckAgainst) // GET — страница сверки, POST — запуск сверки
	mux.HandleFunc("/order-find", h.OrderFind)          // GET — форма поиска, POST — поиск заказа
	mux.HandleFunc("/orders", h.Orders)                 // GET
	mux.HandleFunc("/orders/update", h.UpdateOrders)    // POST
	mux.HandleFunc("/export", h.ExportToExcel)
	mux.HandleFunc("/download", h.DownloadFile)
	mux.HandleFunc("/update-from-ms", h.UpdateFromMS) // POST
	mux.HandleFunc("/print-form", h.PrintForm)
	mux.HandleFunc("/print-multiple-forms", h.PrintMultipleForms) // POST
	mux.HandleFunc("/orders/delete", h.DeleteOrder)               // DELETE
	mux.HandleFunc("/print-barcodes", h.PrintBarcodes)            // POST
	mux.HandleFunc("/wiki", h.WikiIndex)                          // GET
	mux.HandleFunc("/wiki/page", h.WikiPage)                      // GET
	mux.HandleFunc("/wiki/edit", h.WikiEdit)                      // GET — форма, POST — сохранение
	mux.HandleFunc("/wiki/delete", h.WikiDelete)                  // POST
	mux.HandleFunc("/wiki/photo", h.WikiPhoto)                    // GET
	mux.HandleFunc("GET /goods", h.GoodsPage)                     // хаб «Продукция» (поиск + добавление)
	mux.HandleFunc("POST /goods/search", h.GoodsSearch)           // поиск позиции в каталоге
	mux.HandleFunc("GET /goods/edit", h.GoodsEditPage)            // редактирование позиции
	mux.HandleFunc("POST /goods/edit", h.GoodsEditSave)           // сохранение ручных правок
	mux.HandleFunc("POST /goods/resync", h.GoodsResync)           // ресинк позиции из МС
	mux.HandleFunc("GET /goods/tree", h.GoodsTreePage)            // дерево папок для добавления
	mux.HandleFunc("POST /goods/tree", h.GoodsExport)             // выгрузка отмеченных товаров в каталог
	mux.HandleFunc("/qrcodes", h.QRPage)                          // GET — модуль «Честный знак»
	mux.HandleFunc("/qrcodes/add", h.QRAdd)                       // GET — форма, POST — сохранение фото
	mux.HandleFunc("/qrcodes/list", h.QRList)                     // GET — таблица заказов с фото
	mux.Handle("/qrcodes/photos/", http.StripPrefix("/qrcodes/photos/", qrPhotosHandler(h.qrUC.PhotosDir())))

	// Модуль «МойСклад»: хаб и справочник поставщиков.
	mux.HandleFunc("/ms", h.MsPage)                          // GET — хаб
	mux.HandleFunc("/ms/suppliers", h.SuppliersList)         // GET — список поставщиков
	mux.HandleFunc("/ms/suppliers/new", h.SupplierNew)       // GET — форма создания
	mux.HandleFunc("/ms/suppliers/edit", h.SupplierEdit)     // GET — форма редактирования
	mux.HandleFunc("/ms/suppliers/save", h.SupplierSave)     // POST — создание/обновление
	mux.HandleFunc("/ms/suppliers/delete", h.SupplierDelete) // POST — удаление

	// Модуль «Сроки» (остатки по срокам годности).
	mux.HandleFunc("GET /ms/dates", h.StockDatesPage)          // страница «Сроки»
	mux.HandleFunc("GET /ms/dates/short", h.StockShortPage)    // страница «Шорт-лист»
	mux.HandleFunc("GET /ms/dates/ws", h.StockDatesWS)         // вебсокет: снапшот и дельты
	mux.HandleFunc("POST /ms/dates/discount", h.StockDiscount) // запись ручной скидки

	return mux
}

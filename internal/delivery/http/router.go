//nolint:revive //we alias the package to avoid conflict with the standard library "http" package
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
	mux.HandleFunc("/goods", h.GoodsPage)                         // GET — страница, POST — выгрузка дерева папок товаров
	mux.HandleFunc("/qrcodes", h.QRPage)                          // GET — модуль «Честный знак»
	mux.HandleFunc("/qrcodes/add", h.QRAdd)                       // GET — форма, POST — сохранение фото
	mux.HandleFunc("/qrcodes/list", h.QRList)                     // GET — таблица заказов с фото
	mux.Handle("/qrcodes/photos/", http.StripPrefix("/qrcodes/photos/", qrPhotosHandler(h.qrUC.PhotosDir())))

	return mux
}

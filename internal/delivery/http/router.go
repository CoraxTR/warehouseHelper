package http

import (
	"net/http"
)

func NewRouter(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()

	fs := http.FileServer(http.Dir("../internal/delivery/web/static"))

	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/", h.Home)
	mux.HandleFunc("/sync", h.Sync)                  // POST
	mux.HandleFunc("/orders", h.Orders)              // GET
	mux.HandleFunc("/orders/update", h.UpdateOrders) // POST
	mux.HandleFunc("/export", h.ExportToExcel)
	mux.HandleFunc("/download", h.DownloadFile)
	mux.HandleFunc("/update-from-ms", h.UpdateFromMS) // POST
	mux.HandleFunc("/print-form", h.PrintForm)
	mux.HandleFunc("/print-multiple-forms", h.PrintMultipleForms) // POST
	mux.HandleFunc("/orders/delete", h.DeleteOrder)               // DELETE
	mux.HandleFunc("/print-barcodes", h.PrintBarcodes)            // POST

	return mux
}

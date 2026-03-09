package http

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/usecase"
)

type Handler struct {
	syncUC   *usecase.SyncUseCase
	ordersUC *usecase.OrdersUseCase
	exportUC *usecase.ExportToExcelUseCase
}

func NewHandler(syncUC *usecase.SyncUseCase, ordersUC *usecase.OrdersUseCase, exportUC *usecase.ExportToExcelUseCase) *Handler {
	return &Handler{
		syncUC:   syncUC,
		ordersUC: ordersUC,
		exportUC: exportUC,
	}
}

func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	http.ServeFile(w, r, "../internal/delivery/web/templates/index.html")
}

func (h *Handler) Sync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	h.syncUC.SyncDeliverableOrders(r.Context())

	http.Redirect(w, r, "/orders", http.StatusSeeOther)
}

func (h *Handler) Orders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	orders, err := h.ordersUC.GetAllOrders(r.Context())
	if err != nil {
		http.Error(w, "Failed to load orders: "+err.Error(), http.StatusInternalServerError)

		return
	}

	for _, o := range orders {
		log.Printf("Order %s errors: %v", o.GetName(), o.GetErrors())
	}

	tmpl := template.Must(template.ParseFiles("../internal/delivery/web/templates/orders.html"))

	err = tmpl.Execute(w, orders)
	if err != nil {
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)

		return
	}
}

func (h *Handler) ExportToExcel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	summary, err := h.exportUC.ExportOrders(r.Context())
	if err != nil {
		http.Error(w, "Export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl := template.Must(template.ParseFiles("../internal/delivery/web/templates/summary.html"))
	if err := tmpl.Execute(w, summary); err != nil {
		http.Error(w, "Failed to render summary: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) UpdateOrders(w http.ResponseWriter, r *http.Request) {
	var dtos []UpdateOrderRequest

	err := json.NewDecoder(r.Body).Decode(&dtos)
	if err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)

		return
	}

	domains := make([]*domain.InternalOrder, len(dtos))
	for i, dto := range dtos {
		domains[i] = ToDomainOrder(&dto)
	}

	err = h.ordersUC.UpdateOrders(r.Context(), domains)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	fileName := r.URL.Query().Get("file")
	if fileName == "" {
		http.Error(w, "File not specified", http.StatusBadRequest)

		return
	}

	if strings.Contains(fileName, "/") || strings.Contains(fileName, "\\") {
		http.Error(w, "Invalid file name", http.StatusBadRequest)

		return
	}

	filePath := filepath.Join("..", fileName)

	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+fileName)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, filePath)
}

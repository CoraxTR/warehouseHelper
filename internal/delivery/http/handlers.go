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
	pdfUC    *usecase.ExportOrderPDFUseCase
}

func NewHandler(syncUC *usecase.SyncUseCase, ordersUC *usecase.OrdersUseCase, exportUC *usecase.ExportToExcelUseCase, pdfUC *usecase.ExportOrderPDFUseCase) *Handler {
	return &Handler{
		syncUC:   syncUC,
		ordersUC: ordersUC,
		exportUC: exportUC,
		pdfUC:    pdfUC,
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
		log.Printf("GetAllOrders error: %v", err) // добавьте логирование
		http.Error(w, "Failed to load orders: "+err.Error(), http.StatusInternalServerError)

		return
	}
	log.Printf("Orders handler loaded %d orders", len(orders))
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

func (h *Handler) UpdateFromMS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req UpdateFromMSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Href == "" {
		http.Error(w, "href is required", http.StatusBadRequest)
		return
	}

	err := h.ordersUC.UpdateOrderFromMS(r.Context(), req.Href)
	if err != nil {
		log.Printf("UpdateFromMS error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (h *Handler) PrintForm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	href := r.URL.Query().Get("href")
	if href == "" {
		http.Error(w, "href parameter required", http.StatusBadRequest)
		return
	}

	filePath, err := h.pdfUC.GetOrderPDF(r.Context(), href)
	if err != nil {
		log.Printf("Error getting PDF: %v", err)
		http.Error(w, "Failed to get PDF: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=order_form.pdf")
	w.Header().Set("Content-Type", "application/pdf")

	log.Println(filePath)
	http.ServeFile(w, r, filePath)
}

func (h *Handler) PrintMultipleForms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PrintMultipleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Hrefs) == 0 {
		http.Error(w, "No hrefs provided", http.StatusBadRequest)
		return
	}

	filePath, err := h.pdfUC.GetMultipleOrdersPDF(r.Context(), req.Hrefs)
	if err != nil {
		log.Printf("Error merging PDFs: %v", err)
		http.Error(w, "Failed to merge PDFs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=merged_forms.pdf")
	w.Header().Set("Content-Type", "application/pdf")
	http.ServeFile(w, r, filePath)
}

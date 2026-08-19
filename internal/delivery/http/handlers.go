package http

import (
	"context"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"warehouseHelper/internal/domain"
	msucase "warehouseHelper/internal/msclient/usecase"
	rgucase "warehouseHelper/internal/refgo/usecase"
	"warehouseHelper/internal/tempdir"
	wucase "warehouseHelper/internal/wiki/usecase"
)

type Handler struct {
	syncUC    *msucase.SyncUseCase
	ordersUC  *msucase.OrdersUseCase
	exportUC  *rgucase.ExportToExcelUseCase
	pdfUC     *rgucase.ExportOrderPDFUseCase
	barcodeUC *rgucase.ExportBarcodesToExcelUseCase
	refGoUC   *rgucase.RefGoCheckAgainstUseCase
	wikiUC    *wucase.WikiUseCase
}

func NewHandler(syncUC *msucase.SyncUseCase, ordersUC *msucase.OrdersUseCase, exportUC *rgucase.ExportToExcelUseCase, pdfUC *rgucase.ExportOrderPDFUseCase, barcodeUC *rgucase.ExportBarcodesToExcelUseCase, refGoUC *rgucase.RefGoCheckAgainstUseCase, wikiUC *wucase.WikiUseCase) *Handler {
	return &Handler{
		syncUC:    syncUC,
		ordersUC:  ordersUC,
		exportUC:  exportUC,
		pdfUC:     pdfUC,
		barcodeUC: barcodeUC,
		refGoUC:   refGoUC,
		wikiUC:    wikiUC,
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

func (h *Handler) RefGoPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	http.ServeFile(w, r, "../internal/delivery/web/templates/refgo.html")
}

func (h *Handler) RefGoCheckAgainst(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.refGoUC.Enabled() {
			http.Error(w, "Модуль сверки РефГо отключён: заполните его параметры в .env", http.StatusForbidden)

			return
		}

		http.ServeFile(w, r, "../internal/delivery/web/templates/refgo_checkagainst.html")

		return
	case http.MethodPost:
		// логика запуска сверки ниже
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if !h.refGoUC.Enabled() {
		http.Error(w, "Модуль сверки РефГо отключён: заполните его параметры в .env", http.StatusForbidden)

		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)

	err := r.ParseMultipartForm(32 << 20)
	if err != nil {
		http.Error(w, "Invalid form data: "+err.Error(), http.StatusBadRequest)

		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File required: "+err.Error(), http.StatusBadRequest)

		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read file: "+err.Error(), http.StatusBadRequest)

		return
	}

	dateFrom := strings.TrimSpace(r.FormValue("dateFrom"))
	dateTo := strings.TrimSpace(r.FormValue("dateTo"))
	if dateFrom == "" || dateTo == "" {
		http.Error(w, "Date interval required", http.StatusBadRequest)

		return
	}

	result, err := h.refGoUC.Check(r.Context(), dateFrom, dateTo, data)
	if err != nil {
		log.Printf("RefGoCheckAgainst error: %v", err)
		http.Error(w, "Failed to run check: "+err.Error(), http.StatusInternalServerError)

		return
	}

	tmpl := template.Must(template.ParseFiles("../internal/delivery/web/templates/refgo_result.html"))

	err = tmpl.Execute(w, result)
	if err != nil {
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)

		return
	}
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

// OrderFindPageData — данные для страницы поиска заказа по номеру.
type OrderFindPageData struct {
	MsNumber    string
	RefGoNumber string
	Order       *domain.InternalOrder
	Error       string
}

// orderFindTmpl — шаблон страницы поиска, парсится один раз при старте.
var orderFindTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/order_find.html"))

// OrderFind — GET: страница поиска заказа (поддерживает ?msNumber= / ?refGoNumber=);
// POST: поиск по одному из номеров.
func (h *Handler) OrderFind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data: "+err.Error(), http.StatusBadRequest)

			return
		}

		data := OrderFindPageData{
			MsNumber:    strings.TrimSpace(r.FormValue("msNumber")),
			RefGoNumber: strings.TrimSpace(r.FormValue("refGoNumber")),
		}

		h.searchOrder(r.Context(), &data)

		// PRG: после успешного поиска редиректим на GET с параметрами,
		// чтобы F5 не повторял POST.
		if data.Error == "" && data.Order != nil {
			target := "/order-find?" + orderFindQuery(data.MsNumber, data.RefGoNumber)
			http.Redirect(w, r, target, http.StatusSeeOther)

			return
		}

		h.renderOrderFindPage(w, data)

		return
	}

	data := OrderFindPageData{
		MsNumber:    strings.TrimSpace(r.URL.Query().Get("msNumber")),
		RefGoNumber: strings.TrimSpace(r.URL.Query().Get("refGoNumber")),
	}

	if data.MsNumber != "" || data.RefGoNumber != "" {
		h.searchOrder(r.Context(), &data)
	}

	h.renderOrderFindPage(w, data)
}

// orderFindQuery формирует query-строку с заполненным полем поиска.
func orderFindQuery(msNumber, refgoNumber string) string {
	if msNumber != "" {
		return "msNumber=" + url.QueryEscape(msNumber)
	}

	return "refGoNumber=" + url.QueryEscape(refgoNumber)
}

func (h *Handler) searchOrder(ctx context.Context, data *OrderFindPageData) {
	// Заполнено должно быть ровно одно поле.
	if (data.MsNumber == "") == (data.RefGoNumber == "") {
		data.Error = "Заполните одно из полей: «Номер в МС» или «Номер в РефГо»"

		return
	}

	var (
		order *domain.InternalOrder
		err   error
	)

	if data.MsNumber != "" {
		order, err = h.ordersUC.GetOrderByName(ctx, data.MsNumber)
	} else {
		order, err = h.ordersUC.GetOrderByRefGoNumber(ctx, data.RefGoNumber)
	}

	if err != nil {
		log.Printf("OrderFind search error: %v", err)
		data.Error = "Ошибка поиска заказа"

		return
	}

	if order == nil {
		data.Error = "Заказ не найден"

		return
	}

	data.Order = order
}

func (h *Handler) renderOrderFindPage(w http.ResponseWriter, data OrderFindPageData) {
	err := orderFindTmpl.Execute(w, data)
	if err != nil {
		log.Printf("OrderFind template error: %v", err)
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

	err = tmpl.Execute(w, summary)
	if err != nil {
		http.Error(w, "Failed to render summary: "+err.Error(), http.StatusInternalServerError)

		return
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

	name := filepath.Base(fileName)
	if name == "." || name == ".." || name == "/" || name == "" {
		http.Error(w, "Invalid file name", http.StatusBadRequest)

		return
	}

	filePath := filepath.Join(tempdir.Dir, name)

	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(fileName)}))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, filePath)
}

func (h *Handler) UpdateFromMS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req UpdateFromMSRequest

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)

		return
	}

	if req.Href == "" {
		http.Error(w, "href is required", http.StatusBadRequest)

		return
	}

	err = h.ordersUC.UpdateOrderFromMS(r.Context(), req.Href)
	if err != nil {
		log.Printf("UpdateFromMS error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.WriteHeader(http.StatusOK)

	_, err = w.Write([]byte("OK"))
	if err != nil {
		log.Printf("Error writing response: %v", err)

		return
	}
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

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
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

func (h *Handler) DeleteOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	href := r.URL.Query().Get("href")
	if href == "" {
		http.Error(w, "href is required", http.StatusBadRequest)

		return
	}

	err := h.ordersUC.DeleteOrder(r.Context(), href)
	if err != nil {
		log.Printf("DeleteOrder error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) PrintBarcodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req PrintMultipleRequest

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)

		return
	}

	if len(req.Hrefs) == 0 {
		http.Error(w, "No hrefs provided", http.StatusBadRequest)

		return
	}

	filePath, err := h.barcodeUC.GetMultipleOrdersBarcodes(r.Context(), req.Hrefs)
	if err != nil {
		log.Printf("Error exporting barcodes: %v", err)
		http.Error(w, "Failed to export barcodes: "+err.Error(), http.StatusInternalServerError)

		return
	}

	fileName := filepath.Base(filePath)
	w.Header().Set("Content-Disposition", "attachment; filename="+fileName)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, filePath)
}

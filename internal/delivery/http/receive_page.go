package http

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/receiving"
)

var receiveTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/receive.html"))

// receivePageData — данные страницы приёмки.
type receivePageData struct {
	Suppliers []domain.Supplier // для выбора (когда поставщик ещё не выбран)
	Supplier  *domain.Supplier  // выбранный поставщик (nil — выбор)
	CacheJSON template.JS       // кеш приёмки для клиента (JSON в <script>)
	Error     string
}

// ReceivePage — GET /ms/receive[?id=...]: выбор поставщика или страница
// сканирования с кешем приёмки (правила, маппинг кодов, каталог).
func (h *Handler) ReceivePage(w http.ResponseWriter, r *http.Request) {
	data := receivePageData{}

	suppliers, err := h.msUC.List(r.Context())
	if err != nil {
		log.Printf("receive: список поставщиков: %v", err)
		http.Error(w, "не удалось получить список поставщиков", http.StatusInternalServerError)
		return
	}
	data.Suppliers = suppliers

	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		if err := receiveTmpl.Execute(w, data); err != nil {
			log.Printf("receive template: %v", err)
		}
		return
	}

	var supplier *domain.Supplier
	for i := range suppliers {
		if suppliers[i].ID == id {
			supplier = &suppliers[i]
			break
		}
	}
	if supplier == nil {
		data.Error = "поставщик не найден"
		if err := receiveTmpl.Execute(w, data); err != nil {
			log.Printf("receive template: %v", err)
		}
		return
	}
	data.Supplier = supplier

	cache, err := h.receivingUC.GetCache(r.Context(), id)
	if err != nil {
		data.Error = err.Error()
		if err := receiveTmpl.Execute(w, data); err != nil {
			log.Printf("receive template: %v", err)
		}
		return
	}
	if err := h.receivingUC.AddCatalogCodes(r.Context(), cache); err != nil {
		data.Error = err.Error()
		if err := receiveTmpl.Execute(w, data); err != nil {
			log.Printf("receive template: %v", err)
		}
		return
	}

	cacheJSON, err := json.Marshal(cache)
	if err != nil {
		http.Error(w, "не удалось собрать кеш приёмки", http.StatusInternalServerError)
		return
	}
	data.CacheJSON = template.JS(cacheJSON)

	if err := receiveTmpl.Execute(w, data); err != nil {
		log.Printf("receive template: %v", err)
	}
}

// receiveSaveScan — DTO запроса Save: даты строками YYYY-MM-DD (time.Time
// из JSON в этом формате не разобрать).
type receiveSaveScan struct {
	Raw              string            `json:"raw"`
	ManualProductID  string            `json:"manual_product_id"`
	ManualWeightG    *int64            `json:"manual_weight_g"`
	ManualProducedOn string            `json:"manual_produced_on"`
	ManualBestBefore string            `json:"manual_best_before"`
	Children         []receiveSaveScan `json:"children"`
}

type receiveSaveRequest struct {
	SupplierID string            `json:"supplier_id"`
	Scans      []receiveSaveScan `json:"scans"`
}

// ReceiveSave — POST /ms/receive/save: принять приёмку (JSON), вернуть
// отчёт и данные для печати. Ошибка резолва — 400 с текстом: клиент
// подсвечивает проблемную карточку и не теряет введённое.
func (h *Handler) ReceiveSave(w http.ResponseWriter, r *http.Request) {
	var req receiveSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "невалидный JSON запроса", http.StatusBadRequest)
		return
	}

	saveReq := receiving.SaveRequest{
		SupplierID: strings.TrimSpace(req.SupplierID),
		Scans:      toScanEntries(req.Scans),
	}

	result, err := h.receivingUC.Save(r.Context(), saveReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("receive save encode: %v", err)
	}
}

// toScanEntries конвертирует DTO (даты строками) в домен.
func toScanEntries(in []receiveSaveScan) []receiving.ScanEntry {
	out := make([]receiving.ScanEntry, 0, len(in))
	for _, s := range in {
		out = append(out, receiving.ScanEntry{
			Raw:              s.Raw,
			ManualProductID:  s.ManualProductID,
			ManualWeightG:    s.ManualWeightG,
			ManualProducedOn: parseSaveDate(s.ManualProducedOn),
			ManualBestBefore: parseSaveDate(s.ManualBestBefore),
			Children:         toScanEntries(s.Children),
		})
	}
	return out
}

// parseSaveDate разбирает дату YYYY-MM-DD; пустая строка — nil.
func parseSaveDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &t
}

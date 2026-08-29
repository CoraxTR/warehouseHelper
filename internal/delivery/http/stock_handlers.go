package http

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"time"

	"warehouseHelper/internal/stock"
	stockws "warehouseHelper/internal/stock/ws"
)

var stockDatesTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/stock_dates.html"))

// stockPageData — флаг «Шорт-лист» для единого шаблона страниц «Сроки».
type stockPageData struct {
	ShortList bool
}

// StockDatesPage — GET /ms/dates: страница «Сроки».
func (h *Handler) StockDatesPage(w http.ResponseWriter, _ *http.Request) {
	if err := stockDatesTmpl.Execute(w, stockPageData{}); err != nil {
		log.Printf("stock_dates template: %v", err)
	}
}

// StockShortPage — GET /ms/dates/short: «Шорт-лист» (только short_list=true).
func (h *Handler) StockShortPage(w http.ResponseWriter, _ *http.Request) {
	if err := stockDatesTmpl.Execute(w, stockPageData{ShortList: true}); err != nil {
		log.Printf("stock_dates template: %v", err)
	}
}

// StockDatesWS — GET /ms/dates/ws: вебсокет «Сроки»/«Шорт-лист».
// При подключении клиент получает полный снапшот, дальше — дельты.
func (h *Handler) StockDatesWS(w http.ResponseWriter, r *http.Request) {
	snapshot, err := json.Marshal(stockws.Message{Type: "snapshot", Rows: h.stockUC.Snapshot()})
	if err != nil {
		log.Printf("stock ws snapshot: %v", err)
		http.Error(w, "не удалось собрать снапшот", http.StatusInternalServerError)

		return
	}
	h.stockHub.ServeConn(w, r, snapshot)
}

// StockDiscount — POST /ms/dates/discount: запись ручной скидки лота.
// body: {"product_id":"...","best_before":"2026-09-01","discount_general_manual":7,"discount_telegram_manual":null}
// null = сброс (NULL в БД). После записи клиенты обновятся по вебсокету.
func (h *Handler) StockDiscount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProductID      string `json:"product_id"`
		BestBefore     string `json:"best_before"` // YYYY-MM-DD
		GeneralManual  *int16 `json:"discount_general_manual"`
		TelegramManual *int16 `json:"discount_telegram_manual"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)

		return
	}
	if req.ProductID == "" {
		http.Error(w, "product_id обязателен", http.StatusBadRequest)

		return
	}
	bb, err := time.Parse(time.DateOnly, req.BestBefore)
	if err != nil {
		http.Error(w, "best_before должен быть YYYY-MM-DD", http.StatusBadRequest)

		return
	}

	if err := h.stockUC.SetManualDiscount(r.Context(), req.ProductID, bb, req.GeneralManual, req.TelegramManual); err != nil {
		switch {
		case errors.Is(err, stock.ErrProductNotFound), errors.Is(err, stock.ErrLotNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			log.Printf("stock discount: %v", err)
			http.Error(w, "не удалось сохранить скидку", http.StatusInternalServerError)
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

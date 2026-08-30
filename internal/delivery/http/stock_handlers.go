package http

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/stock"
	sucase "warehouseHelper/internal/stock/usecase"
	stockws "warehouseHelper/internal/stock/ws"
)

var (
	stockDatesTmpl  = template.Must(template.ParseFiles("../internal/delivery/web/templates/stock_dates.html"))
	stockUpdateTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/stock_update.html"))
)

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

// StockUpdatePage — GET /ms/dates/update: страница «Обновить сроки» (сканирование
// штрих-кодов и замена остатков). Ограничения: ?product_id= (одна позиция —
// ожидаемый internal_code) или ?group= (группа — три цифры internal_code[1:4]).
// Без параметров — полное обновление (без проверки кода).
func (h *Handler) StockUpdatePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pc, err := h.stockUC.UpdatePageContext(r.Context(), q.Get("product_id"), q.Get("group"))
	if err != nil {
		log.Printf("stock update page: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}
	lengths := make([]string, 0, 2)
	for _, l := range h.stockUC.ValidLengths() {
		lengths = append(lengths, strconv.Itoa(l))
	}
	data := stockUpdateData{
		ProductID: pc.ProductID,
		GroupCode: pc.GroupCode,
		Name:      pc.Name,
		Code:      pc.Code,
		Lengths:   strings.Join(lengths, ","),
	}
	if err := stockUpdateTmpl.Execute(w, data); err != nil {
		log.Printf("stock_update template: %v", err)
	}
}

// StockUpdateSave — POST /ms/dates/update/save: применить батч сканов.
// body: {"scans":["0021...","0021..."],"product_id":"...","group":"021"}
// Сервер сам разбирает все коды (авторитетно), резолвит каталог одним запросом,
// проверяет ограничения и заменяет остатки сканированных товаров одной
// транзакцией. Любая ошибка → ничего не записано (ответ 400 с сообщением).
func (h *Handler) StockUpdateSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scans     []string `json:"scans"`
		ProductID string   `json:"product_id"`
		Group     string   `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)

		return
	}
	if len(req.Scans) == 0 {
		http.Error(w, "нет сканов", http.StatusBadRequest)

		return
	}

	err := h.stockUC.ReplaceStock(r.Context(), sucase.ReplaceRequest{
		Scans:             req.Scans,
		ExpectedProductID: req.ProductID,
		ExpectedGroupCode: req.Group,
	})
	if err != nil {
		switch {
		case errors.Is(err, stock.ErrScanNotInternal), errors.Is(err, stock.ErrScanInvalid),
			errors.Is(err, stock.ErrScanGroupMismatch), errors.Is(err, stock.ErrScanProductMismatch),
			errors.Is(err, stock.ErrProductNotFound):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			log.Printf("stock update save: %v", err)
			http.Error(w, "не удалось обновить сроки", http.StatusInternalServerError)
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// stockUpdateData — данные страницы «Обновить сроки»: ограничения страницы,
// баннер и допустимые длины штрих-кодов (правила разбирателя для клиента).
type stockUpdateData struct {
	ProductID string // ограничение: ожидаемый товар (пусто — нет)
	GroupCode string // ограничение: ожидаемая группа (пусто — нет)
	Name      string // отображаемое имя (товар/группа) для баннера
	Code      string // ожидаемый internal_code (для ограничения по товару)
	Lengths   string // допустимые длины, через запятую: "29,33"
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

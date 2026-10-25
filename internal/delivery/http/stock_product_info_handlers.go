package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/domain"
)

// Мини-окно карточки позиции на странице «Сроки»: поставщики (только чтение —
// их состав задают внешние коды), варианты заказа, цены пар и ссылка на сайт
// (редактируются). Данные собираются из модулей-владельцев:
//   - поставщики с внешним кодом товара — приёмка (product_supplier_barcodes);
//   - имена поставщиков и цены пар — справочник поставщиков (product_supplier_prices);
//   - варианты заказа — каталог (product_order_schedules);
//   - ссылка на сайт — каталог (products.site_url).

// ProductInfoSupplier — поставщик товара в мини-окне.
type ProductInfoSupplier struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PriceKop int64  `json:"price_kop"` // 0 — цена не задана
}

// ProductInfoSchedule — вариант заказа пары (товар, поставщик).
type ProductInfoSchedule struct {
	ID              int64   `json:"id"` // 0 — новый вариант
	SupplierID      string  `json:"supplier_id"`
	Name            string  `json:"name"`
	OrderDays       []int16 `json:"order_days"`
	DeliveryDays    []int16 `json:"delivery_days"`
	DelayDays       *int16  `json:"delay_days"`
	OrderCutoffTime string  `json:"order_cutoff_time"` // "HH:MM" или ""
}

// ProductInfo — ответ GET /ms/dates/product-info.
type ProductInfo struct {
	ProductID    string                `json:"product_id"`
	ProductName  string                `json:"product_name"`
	InternalCode string                `json:"internal_code"`
	SiteURL      string                `json:"site_url"`
	Suppliers    []ProductInfoSupplier `json:"suppliers"`
	Schedules    []ProductInfoSchedule `json:"schedules"`
	Error        string                `json:"error,omitempty"`
}

// ProductInfoSaveRequest — тело POST /ms/dates/product-info/save: сохраняется
// то, что пришло; ссылка и цены — по наличию полей, варианты — полным набором.
type ProductInfoSaveRequest struct {
	ProductID  string                `json:"product_id"`
	SiteURL    *string               `json:"site_url"`
	Prices     []ProductInfoSupplier `json:"prices"`
	Schedules  []ProductInfoSchedule `json:"schedules"`
	SaveOrders bool                  `json:"save_orders"`
}

// StockProductInfo — GET /ms/dates/product-info?id=<productID>.
func (h *Handler) StockProductInfo(w http.ResponseWriter, r *http.Request) {
	productID := strings.TrimSpace(r.URL.Query().Get("id"))
	if productID == "" {
		writeJSON(w, http.StatusBadRequest, ProductInfo{Error: "не выбран товар"})
		return
	}

	product, err := h.goodsUC.GetProduct(r.Context(), productID)
	if err != nil || product == nil {
		slog.Info("product info: get product", "product_id", productID, "err", err)
		writeJSON(w, http.StatusNotFound, ProductInfo{Error: "товар не найден"})
		return
	}

	info := ProductInfo{
		ProductID:    product.ID,
		ProductName:  product.Name,
		InternalCode: product.InternalCode,
		SiteURL:      product.SiteURL,
	}

	info.Suppliers = h.productInfoSuppliers(r, productID)
	info.Schedules = h.productInfoSchedules(r, productID)

	writeJSON(w, http.StatusOK, info)
}

// productInfoSuppliers — поставщики с внешним кодом товара + их цены.
func (h *Handler) productInfoSuppliers(r *http.Request, productID string) []ProductInfoSupplier {
	ids, err := h.receiveUC.SuppliersByProduct(r.Context(), productID)
	if err != nil {
		slog.Info("product info: suppliers by product", "product_id", productID, "err", err)
	}

	names := make(map[string]string, len(ids))
	if all, err := h.msUC.List(r.Context()); err == nil {
		for _, s := range all {
			names[s.ID] = s.Name
		}
	} else {
		slog.Info("product info: list suppliers", "err", err)
	}

	prices := make(map[string]int64, len(ids))
	if rows, err := h.msUC.ProductPrices(r.Context(), productID); err == nil {
		for _, p := range rows {
			prices[p.SupplierID] = p.Price
			if p.SupplierName != "" {
				names[p.SupplierID] = p.SupplierName
			}
		}
	} else {
		slog.Info("product info: product prices", "product_id", productID, "err", err)
	}

	out := make([]ProductInfoSupplier, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if name == "" {
			name = id
		}
		out = append(out, ProductInfoSupplier{ID: id, Name: name, PriceKop: prices[id]})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// productInfoSchedules — варианты заказа товара (ошибку не скрываем: без
// вариантов окно бессмысленно, поэтому она уходит в текст ответа).
func (h *Handler) productInfoSchedules(r *http.Request, productID string) []ProductInfoSchedule {
	list, _, err := h.goodsUC.ListOrderSchedules(r.Context(), productID)
	if err != nil {
		slog.Info("product info: schedules", "product_id", productID, "err", err)
		return nil
	}

	out := make([]ProductInfoSchedule, 0, len(list))
	for _, s := range list {
		out = append(out, ProductInfoSchedule{
			ID:              s.ID,
			SupplierID:      s.SupplierID,
			Name:            s.Name,
			OrderDays:       s.OrderDays,
			DeliveryDays:    s.DeliveryDays,
			DelayDays:       s.DelayDays,
			OrderCutoffTime: formatCutoff(s.OrderCutoffTime),
		})
	}

	return out
}

// StockProductInfoSave — POST /ms/dates/product-info/save (JSON).
//
// Сохраняет то, что пришло: ссылка (если поле передано), цены (каждая строка —
// отдельная пара), варианты заказа (полный набор при save_orders). Ошибки по
// частям не прерывают остальные: ответ перечисляет, что не удалось.
func (h *Handler) StockProductInfoSave(w http.ResponseWriter, r *http.Request) {
	var req ProductInfoSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не разобрать запрос"})
		return
	}

	productID := strings.TrimSpace(req.ProductID)
	if productID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не выбран товар"})
		return
	}

	problems := make([]string, 0, 4)

	if req.SiteURL != nil {
		if err := h.goodsUC.SetSiteURL(r.Context(), productID, *req.SiteURL); err != nil {
			slog.Info("product info save: site url", "product_id", productID, "err", err)
			problems = append(problems, "ссылка: "+err.Error())
		}
	}

	pricesSaved := 0
	for _, p := range req.Prices {
		if strings.TrimSpace(p.ID) == "" {
			continue
		}
		if err := h.msUC.SetSupplierPrice(r.Context(), p.ID, productID, p.PriceKop); err != nil {
			slog.Info("product info save: price", "product_id", productID, "supplier_id", p.ID, "err", err)
			problems = append(problems, "цена: "+err.Error())
			continue
		}
		pricesSaved++
	}

	if req.SaveOrders {
		schedules := make([]domain.OrderSchedule, 0, len(req.Schedules))
		for _, s := range req.Schedules {
			schedules = append(schedules, domain.OrderSchedule{
				ID:              s.ID,
				ProductID:       productID,
				SupplierID:      s.SupplierID,
				Name:            s.Name,
				OrderDays:       s.OrderDays,
				DeliveryDays:    s.DeliveryDays,
				DelayDays:       s.DelayDays,
				OrderCutoffTime: parseCutoff(s.OrderCutoffTime),
			})
		}
		if err := h.goodsUC.SaveOrderSchedules(r.Context(), productID, schedules); err != nil {
			slog.Info("product info save: schedules", "product_id", productID, "err", err)
			problems = append(problems, "варианты: "+err.Error())
		}
	}

	resp := map[string]any{"ok": len(problems) == 0, "prices_saved": pricesSaved}
	if len(problems) > 0 {
		resp["problems"] = problems
	}
	status := http.StatusOK
	if len(problems) > 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, resp)
}

// formatCutoff — время дедлайна в "HH:MM" для формы; nil — пустая строка.
func formatCutoff(t *time.Time) string {
	if t == nil {
		return ""
	}

	return t.Format("15:04")
}

// parseCutoff — "HH:MM" из формы в *time.Time; пустое/битое — nil.
func parseCutoff(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}

	parsed, err := time.Parse("15:04", v)
	if err != nil {
		hour, mErr := strconv.Atoi(strings.SplitN(v, ":", 2)[0])
		if mErr != nil || hour < 0 || hour > 23 {
			return nil
		}
		parsed = time.Date(0, 1, 1, hour, 0, 0, 0, time.UTC)
	}

	return &parsed
}

// writeJSON — единая запись JSON-ответа.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Info("write json", "err", err)
	}
}

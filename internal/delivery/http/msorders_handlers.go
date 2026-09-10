// Пакет http — хендлеры раздела «Заказы» МойСклад (internal/msorders):
// страница раздела и поиск заказа по номеру для подбора. Своей схемы БД
// у модуля нет — поиск идёт по API МС, результат показывается таблицей.
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"log/slog"
	"warehouseHelper/internal/msclient/client"
	msordersuc "warehouseHelper/internal/msorders/usecase"
)

// OrderPickData — данные страницы «Подобрать»: форма поиска + результат.
type OrderPickData struct {
	Name     string // введённый номер (сохраняется в форме)
	Searched bool   // поиск выполнялся (GET с ?name=)
	Rows     []msordersuc.OrderRow
	Error    string
}

// OrderDetailData — данные страницы заказа (подбор позиций).
type OrderDetailData struct {
	Order *msordersuc.Order
	Error string
}

// Шаблоны раздела «Заказы», парсятся один раз при старте.
var (
	msOrdersTmpl     = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_orders.html", "../internal/delivery/web/templates/_nav.html"))
	msOrdersPickTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_orders_pick.html", "../internal/delivery/web/templates/_nav.html"))
	msOrderTmpl      = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_order.html", "../internal/delivery/web/templates/_nav.html"))
)

// MSOrdersPage — GET /ms/orders: раздел «Заказы» (кнопка «Подобрать»;
// здесь появятся остальные действия раздела).
func (h *Handler) MSOrdersPage(w http.ResponseWriter, _ *http.Request) {
	if err := msOrdersTmpl.Execute(w, nil); err != nil {
		slog.Info(fmt.Sprintf("ms_orders template: %v", err))
	}
}

// MSOrdersPickForm — GET /ms/orders/pick: форма поиска; при ?name= — сразу
// поиск и рендер с таблицей результата (цель PRG-редиректа после POST).
func (h *Handler) MSOrdersPickForm(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	d := &OrderPickData{Name: name}
	if name == "" {
		h.renderOrderPick(w, d)
		return
	}

	d.Searched = true
	rows, err := h.msOrdersUC.Search(r.Context(), name)
	if err != nil {
		slog.Info(fmt.Sprintf("search ms orders %q: %v", name, err))
		d.Error = "не удалось выполнить поиск"
	} else {
		d.Rows = rows
	}

	h.renderOrderPick(w, d)
}

// MSOrdersPickSearch — POST /ms/orders/pick: поиск по номеру (PRG —
// после валидации редирект на GET ?name=, чтобы F5 не дублировал отправку).
func (h *Handler) MSOrdersPickSearch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		h.renderOrderPick(w, &OrderPickData{Error: msordersuc.ErrEmptyName.Error()})
		return
	}

	http.Redirect(w, r, "/ms/orders/pick?name="+url.QueryEscape(name), http.StatusSeeOther)
}

// renderOrderPick рендерит страницу «Подобрать».
func (h *Handler) renderOrderPick(w http.ResponseWriter, d *OrderPickData) {
	if err := msOrdersPickTmpl.Execute(w, d); err != nil {
		slog.Info(fmt.Sprintf("ms_orders_pick template: %v", err))
	}
}

// MSOrderSubmit — POST /ms/orders/{id}/submit: отправка подбора в МС.
// Тело — JSON msordersuc.SubmitRequest (только набранные сканы; сервер
// пересобирает positions из кэша страницы). 200 — заказ обновлён
// (stock_warn — если списание сроков не прошло и нужен ручной пересчёт);
// 400 — ошибка валидации (текст под кнопкой); 502 — МС не принял заказ
// (текст его errors); 500 — внутренняя ошибка. Конвенция проекта: клиенту —
// общее/существенное сообщение, детали в лог.
func (h *Handler) MSOrderSubmit(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "не указан id заказа", http.StatusBadRequest)
		return
	}

	var req msordersuc.SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "не удалось разобрать запрос", http.StatusBadRequest)
		return
	}

	res, err := h.msOrdersUC.Submit(r.Context(), id, req)
	if err != nil {
		slog.Info(fmt.Sprintf("ms order submit %q: %v", id, err))
		var apiErr *client.MSAPIError
		switch {
		case isSubmitValidationErr(err):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.As(err, &apiErr):
			http.Error(w, "МойСклад не принял заказ: "+apiErr.Error(), http.StatusBadGateway)
		default:
			http.Error(w, "не удалось отправить заказ в МойСклад", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		slog.Info(fmt.Sprintf("ms order submit %q: encode: %v", id, err))
	}
}

// isSubmitValidationErr отличает ошибки валидации submit (400) от ошибок
// клиента МС (502) и внутренних (500).
func isSubmitValidationErr(err error) bool {
	for _, e := range []error{
		msordersuc.ErrEmptyOrderID,
		msordersuc.ErrSubmitEmptyRows,
		msordersuc.ErrSubmitBadRow,
		msordersuc.ErrSubmitNoRecords,
		msordersuc.ErrSubmitDuplicateID,
		msordersuc.ErrSubmitRowMissing,
		msordersuc.ErrSubmitRowUnavailable,
		msordersuc.ErrSubmitBadBB,
		msordersuc.ErrSubmitBadWeight,
		msordersuc.ErrSubmitOverpick,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// MSOrderDetailPage — GET /ms/orders/{id}: детальная страница заказа
// (подбор позиций по штрих-кодам). Ошибка клиенту — общим сообщением,
// детали в лог (конвенция проекта).
func (h *Handler) MSOrderDetailPage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	d := &OrderDetailData{}

	if id == "" {
		d.Error = "не указан id заказа"
	} else {
		order, err := h.msOrdersUC.Detail(r.Context(), id)
		if err != nil {
			slog.Info(fmt.Sprintf("ms order detail %q: %v", id, err))
			d.Error = "не удалось загрузить заказ (МойСклад недоступен или заказ удалён)"
		} else {
			d.Order = order
		}
	}

	if err := msOrderTmpl.Execute(w, d); err != nil {
		slog.Info(fmt.Sprintf("ms_order template: %v", err))
	}
}

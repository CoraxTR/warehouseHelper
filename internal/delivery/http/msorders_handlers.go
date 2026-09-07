// Пакет http — хендлеры раздела «Заказы» МойСклад (internal/msorders):
// страница раздела и поиск заказа по номеру для подбора. Своей схемы БД
// у модуля нет — поиск идёт по API МС, результат показывается таблицей.
package http

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"log/slog"
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
	msOrdersTmpl     = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_orders.html"))
	msOrdersPickTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_orders_pick.html"))
	msOrderTmpl      = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_order.html"))
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

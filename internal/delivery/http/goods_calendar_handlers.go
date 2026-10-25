package http

import (
	"html/template"
	"log/slog"
	"net/http"
)

// Шаблон страницы «Календарь продаж» (раздел «Продукция»).
var goodsCalendarTmpl = template.Must(template.ParseFiles(
	"../internal/delivery/web/templates/goods_calendar.html",
	"../internal/delivery/web/templates/_nav.html",
))

// GoodsCalendar — GET /goods/calendar: продажи по периодам (недели/месяцы),
// кг и штуки в ячейке, коэффициент периода в подсказке, окно среднего —
// красной обводкой.
func (h *Handler) GoodsCalendar(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	month := r.URL.Query().Get("month")

	page, err := h.goodsUC.CalendarPage(r.Context(), tab, month)
	if err != nil {
		slog.Info("goods calendar", "tab", tab, "month", month, "err", err)
		http.Error(w, "не удалось собрать календарь продаж", http.StatusInternalServerError)

		return
	}

	if err := goodsCalendarTmpl.ExecuteTemplate(w, "goods_calendar.html", page); err != nil {
		slog.Info("render goods calendar", "err", err)
	}
}

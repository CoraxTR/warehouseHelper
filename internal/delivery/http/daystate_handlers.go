package http

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"
)

var (
	availabilityTmpl = template.Must(
		template.New("availability.html").
			Funcs(template.FuncMap{
				"seq":       seqDays,
				"minus":     minus,
				"plus":      plus,
				"monthName": monthName,
				"prevMonth": prevMonth,
				"nextMonth": nextMonth,
				"todayDay":  todayDay,
			}).
			ParseFiles("../internal/delivery/web/templates/availability.html"))
	stockReportTmpl = template.Must(
		template.New("stock_report.html").
			Funcs(template.FuncMap{
				"seq":       seqDays,
				"minus":     minus,
				"plus":      plus,
				"monthName": monthName,
				"prevMonth": prevMonth,
				"nextMonth": nextMonth,
				"todayDay":  todayDay,
			}).
			ParseFiles("../internal/delivery/web/templates/stock_report.html"))
)

var monthNames = [...]string{
	"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
	"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь",
}

// seqDays — числа 1..n (дни месяца для шапки таблицы).
func seqDays(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func minus(a, b int) int { return a - b }

func plus(a, b int) int { return a + b }

// monthName — «Сентябрь 2026».
func monthName(t time.Time) string {
	return fmt.Sprintf("%s %d", monthNames[t.Month()-1], t.Year())
}

// prevMonth/nextMonth — «YYYY-MM» соседних месяцев для навигации.
func prevMonth(t time.Time) string { return t.AddDate(0, -1, 0).Format("2006-01") }

func nextMonth(t time.Time) string { return t.AddDate(0, 1, 0).Format("2006-01") }

// todayDay — номер сегодняшнего дня, если месяц — текущий; иначе 0
// (подсветка колонки «сегодня»).
func todayDay(month time.Time) int {
	now := time.Now()
	if now.Year() == month.Year() && now.Month() == month.Month() {
		return now.Day()
	}
	return 0
}

// monthParam — месяц из ?month=YYYY-MM; пусто/невалидно — текущий месяц
// (локальное время процесса).
func monthParam(r *http.Request) time.Time {
	now := time.Now()
	if v := r.URL.Query().Get("month"); v != "" {
		if t, err := time.Parse("2006-01", v); err == nil {
			return t
		}
	}
	return now
}

// AvailabilityPage — GET /goods/availability: календарь «Доступность товаров».
func (h *Handler) AvailabilityPage(w http.ResponseWriter, r *http.Request) {
	page, err := h.dayStateUC.Availability(r.Context(), monthParam(r))
	if err != nil {
		slog.Info(fmt.Sprintf("availability page: %v", err))
		http.Error(w, "не удалось собрать календарь", http.StatusInternalServerError)

		return
	}
	if err := availabilityTmpl.Execute(w, page); err != nil {
		slog.Error(fmt.Sprintf("availability template: %v", err))
	}
}

// availabilitySaveReq — сохранение доступности: товар, даты, целевое значение.
type availabilitySaveReq struct {
	ProductID string   `json:"product_id"`
	Dates     []string `json:"dates"` // YYYY-MM-DD
	Orderable bool     `json:"orderable"`
}

// AvailabilitySave — POST /goods/availability/save: запись доступности товара
// на выбранные даты (SetOrderable/SetUnavailable; недоступность эмитит
// Unavailable в ordercoeff).
func (h *Handler) AvailabilitySave(w http.ResponseWriter, r *http.Request) {
	var req availabilitySaveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)

		return
	}
	if req.ProductID == "" || len(req.Dates) == 0 {
		http.Error(w, "product_id и dates обязательны", http.StatusBadRequest)

		return
	}
	dates := make([]time.Time, 0, len(req.Dates))
	for _, s := range req.Dates {
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			http.Error(w, "дата должна быть YYYY-MM-DD: "+s, http.StatusBadRequest)

			return
		}
		dates = append(dates, d)
	}

	var err error
	if req.Orderable {
		err = h.dayStateUC.SetOrderable(r.Context(), req.ProductID, dates)
	} else {
		err = h.dayStateUC.SetUnavailable(r.Context(), req.ProductID, dates)
	}
	if err != nil {
		slog.Info(fmt.Sprintf("availability save: %v", err))
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// StockReportPage — GET /goods/stock-report: «Отчёт по наличию» за месяц.
func (h *Handler) StockReportPage(w http.ResponseWriter, r *http.Request) {
	page, err := h.dayStateUC.StockReport(r.Context(), monthParam(r))
	if err != nil {
		slog.Info(fmt.Sprintf("stock report page: %v", err))
		http.Error(w, "не удалось собрать отчёт", http.StatusInternalServerError)

		return
	}
	if err := stockReportTmpl.Execute(w, page); err != nil {
		slog.Error(fmt.Sprintf("stock_report template: %v", err))
	}
}

// StockReportExport — POST /goods/stock-report/export: xlsx «Отчёта по
// наличию» за месяц (tempdir, очистка tempcleaner).
func (h *Handler) StockReportExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Month string `json:"month"` // YYYY-MM
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)

		return
	}
	month, err := time.Parse("2006-01", req.Month)
	if err != nil {
		http.Error(w, "month должен быть YYYY-MM", http.StatusBadRequest)

		return
	}
	path, err := h.dayStateUC.ExportStockReport(r.Context(), month)
	if err != nil {
		slog.Info(fmt.Sprintf("stock report export: %v", err))
		http.Error(w, "не удалось выгрузить отчёт", http.StatusInternalServerError)

		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}

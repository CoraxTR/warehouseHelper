package http

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"warehouseHelper/internal/discounts"
	ducase "warehouseHelper/internal/discounts/usecase"
)

// discountsTmpl — страница «Скидки»: окно распродажи и очередь избытка.
// Данные — тот же реестр, что у дайджеста и бота (один строитель отчёта,
// разные выводы): шаблон ничего не считает, только печатает строки.
var discountsTmpl = template.Must(
	template.New("discounts.html").
		Funcs(template.FuncMap{
			"date":   discountDate,
			"source": discountSourceLabel,
			"coeff":  discountCoeff,
		}).
		ParseFiles("../internal/delivery/web/templates/discounts.html", "../internal/delivery/web/templates/_nav.html"))

// discountsPage — данные страницы «Скидки».
type discountsPage struct {
	Date           string
	WindowCap      int
	SurplusPercent int16
	Window         []discounts.Row
	Queue          []discounts.Row
}

// DiscountsPage — GET /ms/discounts: актуальные скидки и очередь избытка.
// Реестр живёт в памяти процесса: страница показывает последний расчёт
// (после рестарта — до первого тика он пуст).
func (h *Handler) DiscountsPage(w http.ResponseWriter, _ *http.Request) {
	uc := h.discountsUC
	if uc == nil {
		http.Error(w, "модуль скидок не подключён", http.StatusServiceUnavailable)

		return
	}

	page := discountsPage{
		// Время — из часов юзкейса (инжектированы): своих часов страница
		// не заводит, чтобы шапка жила по тем же часам, что и расчёт.
		Date:           uc.Now().Format("02.01.2006 15:04"),
		WindowCap:      h.discountWindowCap,
		SurplusPercent: discounts.SurplusPercent(),
		Window:         uc.Window(h.discountWindowCap),
		Queue:          uc.Queue(h.discountWindowCap),
	}
	if err := discountsTmpl.Execute(w, page); err != nil {
		slog.Error(fmt.Sprintf("discounts template: %v", err))
	}
}

// dash — прочерк для пустых значений страницы (дата, коэффициент, источник).
const dash = "—"

// discountDate — срок годности лота «02.01.2006» (как в текстах уведомлений).
func discountDate(t time.Time) string {
	if t.IsZero() {
		return dash
	}
	return t.Format("02.01.2006")
}

// sourceLabels — русские названия источников скидки для колонки «Источник».
var sourceLabels = map[discounts.Source]string{
	discounts.SourceManual:  "ручная",
	discounts.SourceExpiry:  "по сроку",
	discounts.SourceSurplus: "избыток",
}

// discountSourceLabel — источник скидки по-русски. SourceNone и неизвестные
// источники — прочерк.
func discountSourceLabel(s discounts.Source) string {
	if label, ok := sourceLabels[s]; ok {
		return label
	}
	return dash
}

// discountCoeff — коэффициент избытка тем же форматом, что в дайджесте
// (один знак, запятая — `discounts.FormatCoeff`); без избытка — «—».
func discountCoeff(v float64) string {
	if v <= 0 {
		return dash
	}
	return discounts.FormatCoeff(v)
}

// убеждаемся, что юзкейс модуля скидок удовлетворяет тому, что зовёт страница
// (окно, очередь реестра и часы модуля) — проверка компилятором, а не
// договорённостью.
var _ interface {
	Window(n int) []discounts.Row
	Queue(windowSize int) []discounts.Row
	Now() time.Time
} = (*ducase.UseCase)(nil)

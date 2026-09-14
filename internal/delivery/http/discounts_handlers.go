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
func (h *Handler) DiscountsPage(w http.ResponseWriter, r *http.Request) {
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

// discountDate — срок годности лота «02.01.2006» (как в текстах уведомлений).
func discountDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("02.01.2006")
}

// discountSourceLabel — источник скидки по-русски (для колонки «Источник»).
func discountSourceLabel(s discounts.Source) string {
	switch s {
	case discounts.SourceManual:
		return "ручная"
	case discounts.SourceExpiry:
		return "по сроку"
	case discounts.SourceSurplus:
		return "избыток"
	default:
		return "—"
	}
}

// discountCoeff — коэффициент избытка тем же форматом, что в дайджесте
// (один знак, запятая — `discounts.FormatCoeff`); без избытка — «—».
func discountCoeff(v float64) string {
	if v <= 0 {
		return "—"
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

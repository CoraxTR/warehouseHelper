package http

import (
	"encoding/json"
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
			"dates":  discounts.DatesText,
			"source": discountSourceLabel,
			"coeff":  discountCoeff,
		}).
		ParseFiles("../internal/delivery/web/templates/discounts.html", "../internal/delivery/web/templates/_nav.html"))

// discountsPage — данные страницы «Скидки».
type discountsPage struct {
	Date           string
	WindowCap      int
	SurplusPercent int16
	// Plan — предпросмотр плана 14:00: что встанет с рассылкой и сразу на сайте.
	// Считается на момент открытия страницы и ничего не пишет — до 14:00 состав
	// может измениться. Дальше блок обновляет страница (раз в 10 минут,
	// GET /ms/discounts/plan).
	Plan ducase.DayPlanPreview
	// PlanAt — время расчёта плана (ЧЧ:ММ): страница показывает, насколько
	// свежий план у неё на экране.
	PlanAt string
	// Window — активные позиции (не больше ёмкости окна): группа избытка идёт
	// ОДНОЙ строкой с перечислением сроков.
	Window []discounts.Row
	// Queue — «доступно для допродажи»: всё, что за ёмкостью, независимо от
	// источника (и сроковые, и избыточные).
	Queue []discounts.Row
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

	// Секции страницы — тем же разбором, что дайджест: активные позиции (не
	// больше ёмкости окна) и «доступно для допродажи» (всё за ёмкостью).
	// Группа избытка занимает в активных одну строку — страница и дайджест
	// считаются одним кодом и разойтись не могут (решение владельца 23.09.2026).
	digest := uc.Digest(h.discountWindowCap)

	page := discountsPage{
		// Время — из часов юзкейса (инжектированы): своих часов страница
		// не заводит, чтобы шапка жила по тем же часам, что и расчёт.
		Date:           uc.Now().Format("02.01.2006 15:04"),
		WindowCap:      h.discountWindowCap,
		SurplusPercent: discounts.SurplusPercent(),
		Window:         digest.Discounts,
		Queue:          digest.Surplus,
	}
	// Предпросмотр плана 14:00: отчёт собирается по факту (стоящие скидки), а
	// что встанет днём — видно только здесь. Ошибка расчёта страницу не роняет:
	// блока не будет, остальное покажем.
	if prev, err := uc.PlanPreview(r.Context(), h.discountTelegramCap); err != nil {
		slog.Error(fmt.Sprintf("предпросмотр плана скидок: %v", err))
	} else {
		page.Plan = prev
		page.PlanAt = uc.Now().Format("15:04")
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

// lotKeyFormat — формат ключа пары в JSON активных: как даты лота на странице
// «Сроки» (ключ = «<товар>|<ГГГГ-ММ-ДД>»).
const lotKeyFormat = time.DateOnly

// planResponse — ответ GET /ms/discounts/plan: предпросмотр плана 14:00 для
// блока страницы «Скидки» (страница обновляет его раз в 10 минут: план — снимок
// на момент расчёта и до 14:00 меняется).
type planResponse struct {
	Text  string `json:"text"`
	Slot  int    `json:"slot"`
	Extra int    `json:"extra"`
	// At — время расчёта (ЧЧ:ММ) в часах модуля: страница показывает, насколько
	// свежий план перед ней.
	At string `json:"at"`
}

// DiscountsPlan — GET /ms/discounts/plan: предпросмотр плана 14:00 (JSON).
// Расчёт ничего не пишет (см. usecase.PlanPreview); при ошибке — 503, страница
// оставляет прошлый план на экране.
func (h *Handler) DiscountsPlan(w http.ResponseWriter, r *http.Request) {
	uc := h.discountsUC
	if uc == nil {
		http.Error(w, "модуль скидок не подключён", http.StatusServiceUnavailable)

		return
	}

	prev, err := uc.PlanPreview(r.Context(), h.discountTelegramCap)
	if err != nil {
		slog.Error(fmt.Sprintf("предпросмотр плана скидок: %v", err))
		http.Error(w, "предпросмотр плана недоступен", http.StatusServiceUnavailable)

		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(planResponse{
		Text:  prev.Text,
		Slot:  prev.SlotCount,
		Extra: prev.ExtraCount,
		At:    uc.Now().Format("15:04"),
	}); err != nil {
		slog.Error(fmt.Sprintf("предпросмотр плана: ответ: %v", err))
	}
}

// activeLotsResponse — ответ GET /ms/discounts/active: ёмкость окна и пары
// активных позиций окна.
type activeLotsResponse struct {
	Cap  int      `json:"cap"`
	Lots []string `json:"lots"`
}

// DiscountsActive — GET /ms/discounts/active: пары активных позиций окна (JSON)
// для страницы «Сроки». Список ведёт модуль скидок — он владелец окна; страница
// «Сроки» только читает его и решает, показывать ли скидку в ячейке: у пар за
// ёмкостью подсветка остаётся, а значение видно в карточке количества (решение
// владельца 23.09.2026).
func (h *Handler) DiscountsActive(w http.ResponseWriter, _ *http.Request) {
	uc := h.discountsUC
	if uc == nil {
		http.Error(w, "модуль скидок не подключён", http.StatusServiceUnavailable)

		return
	}

	lots := uc.ActiveLots(h.discountWindowCap)
	keys := make([]string, 0, len(lots))
	for _, k := range lots {
		keys = append(keys, k.ProductID+"|"+k.BestBefore.Format(lotKeyFormat))
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(activeLotsResponse{Cap: h.discountWindowCap, Lots: keys}); err != nil {
		slog.Error(fmt.Sprintf("discounts active lots: %v", err))
	}
}

// убеждаемся, что юзкейс модуля скидок удовлетворяет тому, что зовёт страница
// (разбор отчёта, активные пары, окно, очередь реестра и часы модуля) — проверка
// компилятором, а не договорённостью.
var _ interface {
	Window(n int) []discounts.Row
	Queue(windowSize int) []discounts.Row
	Digest(capacity int) discounts.Digest
	ActiveLots(capacity int) []discounts.LotKey
	Now() time.Time
} = (*ducase.UseCase)(nil)

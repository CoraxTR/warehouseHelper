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
	"time"

	"log/slog"
	"warehouseHelper/internal/msclient/client"
	msordersuc "warehouseHelper/internal/msorders/usecase"
	"warehouseHelper/internal/scanmatch"
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
	msOrdersTmpl      = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_orders.html", "../internal/delivery/web/templates/_nav.html"))
	msOrdersPickTmpl  = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_orders_pick.html", "../internal/delivery/web/templates/_nav.html"))
	msOrdersFormsTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_orders_forms.html", "../internal/delivery/web/templates/_nav.html"))
	msOrderTmpl       = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_order.html", "../internal/delivery/web/templates/_nav.html"))
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
		slog.Info("search ms orders", "name", name, "err", err)
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
		slog.Info("ms order submit", "id", id, "err", err)
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
		slog.Info("ms order submit: encode", "id", id, "err", err)
	}
}

// MSOrderSubmitManual — POST /ms/orders/{id}/submit-manual: ручное подтверждение
// подбора (оператор вводит вес/количество строки вместо сканирования кусков).
// Заказ обновляется (quantity = reserve = введённое значение + статус «Вес
// подобран»), сроки НЕ списываются — складу уходит уведомление о пересчёте.
func (h *Handler) MSOrderSubmitManual(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "не указан id заказа", http.StatusBadRequest)

		return
	}

	var req msordersuc.ManualRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "не удалось разобрать запрос", http.StatusBadRequest)

		return
	}

	res, err := h.msOrdersUC.SubmitManual(r.Context(), id, req)
	if err != nil {
		slog.Info("ms order submit-manual", "id", id, "err", err)
		var apiErr *client.MSAPIError
		switch {
		case isSubmitValidationErr(err):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.As(err, &apiErr):
			http.Error(w, "МойСклад не принял заказ: "+apiErr.Error(), http.StatusBadGateway)
		default:
			http.Error(w, "не удалось подтвердить подбор вручную", http.StatusInternalServerError)
		}

		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		slog.Info("ms order submit-manual: encode", "id", id, "err", err)
	}
}

// isSubmitValidationErr отличает ошибки валидации подбора/возврата в сроки
// (400) от ошибок клиента МС (502) и внутренних (500). Ошибки сверки сканов
// (scanmatch.ValidationError) обрабатываются вызывающим кодом отдельно — это не
// сентинелы, а тип с текстом отказа.
func isSubmitValidationErr(err error) bool {
	for _, e := range []error{
		msordersuc.ErrManualEmptyRows,
		msordersuc.ErrManualBadQty,
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
		msordersuc.ErrReturnEmptyRows,
		msordersuc.ErrReturnNoScans,
		msordersuc.ErrReturnCodeMissing,
		msordersuc.ErrReturnCodeChanged,
		msordersuc.ErrReturnNoReserve,
		msordersuc.ErrReturnOverReserve,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// MSOrderReturnSave — POST /ms/orders/{id}/return: возврат в сроки при
// переподборе — приём отсканированных кусков отложенных позиций заказа.
// Тело — JSON msordersuc.PickReturnRequest (строки заказа со сканами); сервер
// перечитывает заказ из МС, сверяет сканы с резервом строк правилами
// scanmatch и пишет остатки (AcceptStock) по срокам этикеток. 204 — принято;
// 400 — сканы не сошлись/валидация (текст в теле); 502 — МС не отдал заказ;
// 500 — внутренняя ошибка (шов остатков не подключён).
func (h *Handler) MSOrderReturnSave(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "не указан id заказа", http.StatusBadRequest)

		return
	}

	var req msordersuc.PickReturnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "не удалось разобрать запрос", http.StatusBadRequest)

		return
	}

	if _, err := h.msOrdersUC.SavePickReturn(r.Context(), id, req); err != nil {
		h.writeMSOrderReturnErr(w, "return save", id, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// MSOrderReturnClose — POST /ms/orders/{id}/return/close: ручное закрытие
// возврата в сроки (куски не вернулись либо вес не совпал с резервом строки).
// Тело — JSON msordersuc.PickReturnRequest: в остатки ничего НЕ пишется, в чат
// склада уходит список незакрытых строк — склад пересчитывает сроки построчно.
// 204 — закрыто; 400 — валидация (текст в теле); 502 — МС не отдал заказ;
// 500 — внутренняя ошибка (уведомления не подключены/не ушли).
func (h *Handler) MSOrderReturnClose(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "не указан id заказа", http.StatusBadRequest)

		return
	}

	var req msordersuc.PickReturnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "не удалось разобрать запрос", http.StatusBadRequest)

		return
	}

	if err := h.msOrdersUC.ClosePickReturn(r.Context(), id, req); err != nil {
		h.writeMSOrderReturnErr(w, "return close", id, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeMSOrderReturnErr отдаёт ошибку сценария возврата в сроки: отказ сверки и
// валидация — 400 с текстом для оператора, ошибка МС — 502, остальное — 500
// (детали в лог, клиенту общее сообщение).
func (h *Handler) writeMSOrderReturnErr(w http.ResponseWriter, action, id string, err error) {
	slog.Info("ms order", "action", action, "id", id, "err", err)

	var (
		apiErr *client.MSAPIError
		ve     *scanmatch.ValidationError
	)
	switch {
	case errors.As(err, &ve):
		http.Error(w, ve.Error(), http.StatusBadRequest)
	case isSubmitValidationErr(err):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.As(err, &apiErr):
		http.Error(w, "МойСклад не отдал заказ: "+apiErr.Error(), http.StatusBadGateway)
	default:
		http.Error(w, "не удалось сохранить возврат в сроки — попробуйте позже", http.StatusInternalServerError)
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
			slog.Info("ms order detail", "id", id, "err", err)
			d.Error = "не удалось загрузить заказ (МойСклад недоступен или заказ удалён)"
		} else {
			d.Order = order
		}
	}

	if err := msOrderTmpl.Execute(w, d); err != nil {
		slog.Info(fmt.Sprintf("ms_order template: %v", err))
	}
}

// OrderFormsData — данные страницы «Печать бланков»: фильтр по дате и список
// заказов с плановой датой доставки на выбранный день.
type OrderFormsData struct {
	Date      string // выбранная дата (ГГГГ-ММ-ДД), возвращается в форму
	DateHuman string // та же дата по-русски (ДД.ММ.ГГГГ) для сообщений
	Rows      []msordersuc.FormRow
	Error     string
}

// MSOrdersFormsForm — GET /ms/orders/forms: форма фильтра по дате и список
// заказов МС за этот день (цель PRG-редиректа после POST). Без ?date — сегодня
// по МСК (даты заказов МС живут в TZ учётки).
func (h *Handler) MSOrdersFormsForm(w http.ResponseWriter, r *http.Request) {
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = formsToday()
	}

	d := &OrderFormsData{Date: date}

	day, err := time.ParseInLocation(time.DateOnly, date, mskLoc)
	if err != nil {
		d.Error = "Дата указана неверно — выберите дату в календаре"
		h.renderOrderForms(w, d)

		return
	}

	d.DateHuman = day.Format("02.01.2006")

	rows, err := h.msFormsUC.FormsByDate(r.Context(), day)
	if err != nil {
		slog.Info("ms orders forms", "date", date, "err", err)
		d.Error = "не удалось получить заказы из МойСклад"
	} else {
		d.Rows = rows
	}

	h.renderOrderForms(w, d)
}

// MSOrdersFormsSearch — POST /ms/orders/forms: смена даты фильтра. После
// разбора формы — редирект на GET ?date= (PRG: F5 не повторяет отправку).
func (h *Handler) MSOrdersFormsSearch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)

		return
	}

	date := strings.TrimSpace(r.FormValue("date"))
	if date == "" {
		date = formsToday()
	}

	http.Redirect(w, r, "/ms/orders/forms?date="+url.QueryEscape(date), http.StatusSeeOther)
}

// MSOrdersFormsPrint — POST /ms/orders/forms/print: бланки выделенных заказов
// одним PDF. Тело — JSON {ids} (PrintMultipleRequest). Бланки, которые МС не
// отдал, в файл не попадают, но и не пропадают молча: их id уезжают заголовком
// X-Skipped-Orders — страница показывает их оператору. 400 — ничего не выбрано;
// 502 — не удалось получить ни одного бланка; 500 — внутренняя ошибка.
func (h *Handler) MSOrdersFormsPrint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req PrintMultipleRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "не удалось разобрать запрос", http.StatusBadRequest)

		return
	}

	filePath, skipped, err := h.msFormsUC.PrintForms(r.Context(), req.IDs)
	if err != nil {
		slog.Info(fmt.Sprintf("ms orders forms print: %v", err))

		if errors.Is(err, msordersuc.ErrNoOrdersSelected) {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		http.Error(w, "не удалось напечатать бланки: МойСклад не отдал ни одного бланка", http.StatusBadGateway)

		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=order_forms.pdf")
	w.Header().Set("Content-Type", "application/pdf")

	setSkippedOrdersHeader(w, skipped)

	http.ServeFile(w, r, filePath)
}

// formsToday — сегодняшняя дата по МСК: плановые даты заказов МС трактуются в
// TZ учётки, а не в TZ машины (на сервере UTC).
func formsToday() string {
	return time.Now().In(mskLoc).Format(time.DateOnly)
}

// renderOrderForms рендерит страницу «Печать бланков».
func (h *Handler) renderOrderForms(w http.ResponseWriter, d *OrderFormsData) {
	if err := msOrdersFormsTmpl.Execute(w, d); err != nil {
		slog.Info(fmt.Sprintf("ms_orders_forms template: %v", err))
	}
}

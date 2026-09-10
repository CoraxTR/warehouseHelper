package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"time"

	"warehouseHelper/internal/returns"
	retucase "warehouseHelper/internal/returns/usecase"
)

// Страница «Возврат в продажу» (хаб «Продукция»): список активных событий
// аудита (GET /goods/return), карточка события с ожиданиями (?e=<id>),
// приём возврата сканированием (POST /goods/return/save) и ручное закрытие
// (POST /goods/return/close). Страница открывается без авторизации — по
// URL-кнопке «Расформировать» из Telegram-сообщения склада.

var returnTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/return.html"))

// mskLoc — момент события отображается в TZ склада (МСК; сервер в UTC).
var mskLoc = time.FixedZone("MSK", 3*60*60)

// returnPageData — данные шаблона: карточка события (?e=) или список (?e нет).
type returnPageData struct {
	HasEvent   bool
	EventID    string
	OrderName  string
	KindText   string // «Заказ отменён» / «Из заказа удалены позиции»
	Moment     string // момент события, МСК
	Done       bool
	Manual     bool // закрыто вручную (в остатки не писано)
	Empty      bool // ожиданий нет: событие «опустело» — только ручное закрытие
	Rows       []returnRowData
	ExpectJSON string // ожидания для клиентской сверки (data-expect)
	ActiveRows []returnActiveRow
	// RemainderRows — что осталось в живом заказе (read-only ориентир
	// оператору, где искать товар); только для события удаления позиций.
	RemainderRows []returnRemainderRow
}

// returnRowData — строка ожидания возврата для шаблона и JS (data-expect).
type returnRowData struct {
	Idx      int    `json:"idx"`     // порядок строки в отчёте — строки различаются по нему
	Code     string `json:"code"`    // internal_code — по нему резолвится скан
	Name     string `json:"name"`    // название товара (из диффа/заказа)
	QtyText  string `json:"qtyText"` // «0.657 кг» / «2 шт» — ожидание для показа
	Target   int64  `json:"target"`  // ожидание для сверки: граммы (весовой) или штуки
	Weighted bool   `json:"weighted"`
}

// returnRemainderRow — строка остатка заказа для ориентира оператора
// (не редактируется, в сверке не участвует).
type returnRemainderRow struct {
	Code    string
	Name    string
	QtyText string
}

type returnActiveRow struct {
	ID        string
	OrderName string
	KindText  string
	Moment    string // МСК
	Status    string // «уведомлено» / «ожидает отправки»
}

func returnKindText(k returns.EventKind) string {
	switch k {
	case returns.KindCancelled:
		return "Заказ отменён"
	case returns.KindRemoved:
		return "Из заказа удалены позиции"
	default:
		return string(k)
	}
}

// qtyTextFor — ожидание строки для показа: кг (3 знака) или штуки.
func qtyTextFor(e returns.Expected) string {
	if e.Weighted {
		return fmt.Sprintf("%.3f кг", float64(e.ExpectedQty)/1000)
	}
	return fmt.Sprintf("%d шт", e.ExpectedQty)
}

// remainderQtyText — количество остатка заказа для показа. Тип учёта берём из
// каталога склада; товара нет в каталоге — по дробности количества (весовой
// МС отдаёт кг с тремя знаками, штучный — целым числом).
func remainderQtyText(r returns.RemainingPosition) string {
	if r.Weighted || r.Quantity != math.Trunc(r.Quantity) {
		return fmt.Sprintf("%.3f кг", r.Quantity)
	}
	return fmt.Sprintf("%d шт", int64(r.Quantity))
}

// ReturnsPage — GET /goods/return: список активных событий (без ?e=) или
// карточка события (?e=<id>). Данные события перечитываются из МС.
func (h *Handler) ReturnsPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("e") == "" {
		h.returnsListPage(w, r)

		return
	}
	h.returnsEventCard(w, r, r.URL.Query().Get("e"))
}

// returnsListPage — GET /goods/return (без ?e=): активные события аудита
// (new/sent), свежие сверху; карточка открывается по ссылке.
func (h *Handler) returnsListPage(w http.ResponseWriter, r *http.Request) {
	active, err := h.returnsUC.ListEvents(r.Context())
	if err != nil {
		slog.Error(fmt.Sprintf("returns list: %v", err))
		http.Error(w, "не удалось загрузить список", http.StatusInternalServerError)

		return
	}

	data := returnPageData{}
	for _, ev := range active {
		data.ActiveRows = append(data.ActiveRows, returnActiveRow{
			ID:        ev.ID,
			OrderName: ev.OrderName,
			KindText:  returnKindText(ev.Kind),
			Moment:    ev.Moment.In(mskLoc).Format("02.01.2006 15:04:05"),
			Status:    eventStatusText(ev),
		})
	}

	if err := returnTmpl.Execute(w, data); err != nil {
		slog.Error(fmt.Sprintf("return template: %v", err))
	}
}

// returnsEventCard — GET /goods/return?e=<id>: карточка события с ожиданиями
// (клиентская сверка по data-expect; сервер — авторитет). Обработанное
// событие — страница «возврат готов» без запросов к МС.
func (h *Handler) returnsEventCard(w http.ResponseWriter, r *http.Request, eventID string) {
	state, err := h.returnsUC.EventPage(r.Context(), eventID)
	if err != nil {
		if errors.Is(err, returns.ErrEventNotFound) {
			http.Error(w, "событие не найдено (удалено из журнала?)", http.StatusNotFound)

			return
		}
		slog.Error(fmt.Sprintf("returns event page %s: %v", eventID, err))
		http.Error(w, "не удалось загрузить событие — попробуйте позже", http.StatusInternalServerError)

		return
	}

	data := returnPageData{
		HasEvent:  true,
		EventID:   state.Event.ID,
		OrderName: state.Event.OrderName,
		KindText:  returnKindText(state.Event.Kind),
		Moment:    state.Event.Moment.In(mskLoc).Format("02.01.2006 15:04:05"),
		Done:      state.Done,
		Manual:    state.Event.Manual,
	}

	if state.Done {
		if err := returnTmpl.Execute(w, data); err != nil {
			slog.Error(fmt.Sprintf("return template: %v", err))
		}

		return
	}

	expect := make([]returnRowData, 0, len(state.Expected))
	for _, e := range state.Expected {
		expect = append(expect, returnRowData{
			Idx:      e.Idx,
			Code:     e.InternalCode,
			Name:     e.Name,
			QtyText:  qtyTextFor(e),
			Target:   e.ExpectedQty,
			Weighted: e.Weighted,
		})
	}
	data.Empty = len(expect) == 0
	data.Rows = expect
	expectJSON, err := json.Marshal(expect)
	if err != nil {
		slog.Error(fmt.Sprintf("returns expect json: %v", err))
		http.Error(w, "не удалось собрать ожидания", http.StatusInternalServerError)

		return
	}
	data.ExpectJSON = string(expectJSON)

	remainder := make([]returnRemainderRow, 0, len(state.Remaining))
	for _, rp := range state.Remaining {
		remainder = append(remainder, returnRemainderRow{
			Code:    rp.InternalCode,
			Name:    rp.Name,
			QtyText: remainderQtyText(rp),
		})
	}
	data.RemainderRows = remainder

	if err := returnTmpl.Execute(w, data); err != nil {
		slog.Error(fmt.Sprintf("return template: %v", err))
	}
}

func eventStatusText(ev returns.ReturnEvent) string {
	if ev.Status == returns.StatusSent {
		return "уведомлено"
	}
	return "ожидает отправки"
}

// ReturnsSave — POST /goods/return/save: приём возврата. body:
// {"event_id":"...","scans":["0021...","..."]} Сервер сверяет сканы с
// ожиданиями (авторитетно, строгое равенство), пишет остатки (AcceptStock)
// и закрывает событие. 204 — принято; 400 — сканы не сошлись; 409 — уже обработано.
func (h *Handler) ReturnsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventID string   `json:"event_id"`
		Scans   []string `json:"scans"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventID == "" {
		http.Error(w, "некорректный запрос", http.StatusBadRequest)

		return
	}
	if len(req.Scans) == 0 {
		http.Error(w, "нет сканов", http.StatusBadRequest)

		return
	}

	if _, err := h.returnsUC.AcceptReturn(r.Context(), req.EventID, req.Scans); err != nil {
		var ve *retucase.ValidationError
		switch {
		case errors.As(err, &ve):
			http.Error(w, ve.Error(), http.StatusBadRequest)
		case errors.Is(err, returns.ErrAlreadyDone):
			http.Error(w, returns.ErrAlreadyDone.Error(), http.StatusConflict)
		case errors.Is(err, returns.ErrEventNotFound):
			http.Error(w, returns.ErrEventNotFound.Error(), http.StatusNotFound)
		default:
			slog.Error(fmt.Sprintf("returns accept %s: %v", req.EventID, err))
			http.Error(w, "не удалось принять возврат — попробуйте позже", http.StatusInternalServerError)
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Ручной возврат (без события аудита) ────────────────────────────────────

var manualTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/manual_return.html"))

// ReturnsManualPage — GET /goods/return/manual: пустая страница сканирования
// кусков. Возврат не привязан к заказу: каждый принятый скан = один кусок
// в остатки (лот по сроку этикетки).
func (h *Handler) ReturnsManualPage(w http.ResponseWriter, _ *http.Request) {
	if err := manualTmpl.Execute(w, nil); err != nil {
		slog.Error(fmt.Sprintf("manual return template: %v", err))
	}
}

// ReturnsManualSave — POST /goods/return/manual/save: приём сканов ручного
// возврата. body: {"scans":[...]}. 200 {"returned":N} — куски записаны в
// остатки; 400 — батч отклонён целиком (текст причины); 500 — сбой.
func (h *Handler) ReturnsManualSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scans []string `json:"scans"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Scans) == 0 {
		http.Error(w, "нет сканов", http.StatusBadRequest)

		return
	}

	n, err := h.returnsUC.ManualReturn(r.Context(), req.Scans)
	if err != nil {
		var ve *retucase.ValidationError
		switch {
		case errors.As(err, &ve):
			http.Error(w, ve.Error(), http.StatusBadRequest)
		default:
			slog.Error(fmt.Sprintf("returns manual accept: %v", err))
			http.Error(w, "не удалось принять возврат — попробуйте позже", http.StatusInternalServerError)
		}

		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int{"returned": n}); err != nil {
		slog.Error(fmt.Sprintf("returns manual save: %v", err))
	}
}

// ReturnsClose — POST /goods/return/close: ручное закрытие (куски не
// вернулись: потеряны/списаны; либо строку не гасит ни один скан — вес не
// совпал, позиция «слита»). В остатки не пишется, в МС ничего не меняется,
// сообщение удаляется, а в чат склада уходит список незакрытых позиций для
// пересчёта сроков. body: {"event_id":"...","scans":["...","..."]} — scans
// оператора нужны только для состава списка. 204 — закрыто; 409 — уже обработано.
func (h *Handler) ReturnsClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventID string   `json:"event_id"`
		Scans   []string `json:"scans"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventID == "" {
		http.Error(w, "некорректный запрос", http.StatusBadRequest)

		return
	}

	if err := h.returnsUC.CloseManual(r.Context(), req.EventID, req.Scans); err != nil {
		switch {
		case errors.Is(err, returns.ErrAlreadyDone):
			http.Error(w, returns.ErrAlreadyDone.Error(), http.StatusConflict)
		case errors.Is(err, returns.ErrEventNotFound):
			http.Error(w, returns.ErrEventNotFound.Error(), http.StatusNotFound)
		default:
			slog.Error(fmt.Sprintf("returns close %s: %v", req.EventID, err))
			http.Error(w, "не удалось закрыть возврат", http.StatusInternalServerError)
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

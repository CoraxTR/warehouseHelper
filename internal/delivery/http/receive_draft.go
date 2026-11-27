package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/receiving"
	rucase "warehouseHelper/internal/receiving/usecase"
)

// Приёмка после «Сохранить»: черновик живёт на сервере (в памяти), его можно
// вернуть на правку, а по «Завершить» — закрыть: остатки, веса и уведомления
// пишутся только там. Завершённая приёмка остаётся на странице приёмок до
// 21:00 МСК: вход в неё — отчёт и наклейки без правки (решение владельца
// 27.11.2026).

var receiveViewTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/receive_view.html", "../internal/delivery/web/templates/_nav.html"))

// mskZone — часовой пояс склада: «до 21:00» считается по Москве, сервер живёт
// в UTC.
var mskZone = time.FixedZone("MSK", 3*60*60)

// receiveDraftCookieName — ключ машины, ведущей приёмку. Своя cookie у каждой
// приёмки: правит и завершает только та машина, которая её начала (как с
// комнатой совместной приёмки).
func receiveDraftCookieName(draftID string) string { return "rcv_host_" + draftID }

// setDraftHostCookie отдаёт ключ машине, создавшей черновик. Secure не
// выставляем: приложение ходит по HTTP в локальной сети, иначе браузер cookie
// не пришлёт и хозяин потерял бы правку.
func setDraftHostCookie(w http.ResponseWriter, draft rucase.Draft) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // локальная сеть, только HTTP
		Name:     receiveDraftCookieName(draft.ID),
		Value:    draft.HostToken,
		Path:     "/",
		MaxAge:   int((12 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// draftHostToken — ключ хозяина приёмки из cookie запроса.
func draftHostToken(r *http.Request, draftID string) string {
	cookie, err := r.Cookie(receiveDraftCookieName(draftID))
	if err != nil {
		return ""
	}

	return cookie.Value
}

// continueDraft открывает сохранённый черновик на правку: приёмку отдаём только
// хозяину этой машины, остальным — причину отказа. Строки страница восстановит
// из /ms/receive/draft.
func (h *Handler) continueDraft(w http.ResponseWriter, r *http.Request, data receivePageData, supplierID, draftID string) {
	draft, ok := h.receivingUC.DraftByID(draftID)

	switch {
	case !ok:
		data.Error = rucase.ErrDraftNotFound.Error()
	case draft.Finished():
		data.Error = rucase.ErrDraftFinished.Error()
	case draft.SupplierID != supplierID:
		data.Error = "приёмка открыта у другого поставщика"
	case draftHostToken(r, draftID) != draft.HostToken:
		data.Error = rucase.ErrDraftHost.Error()
	default:
		data.DraftID = draft.ID
	}

	h.renderReceive(w, data)
}

// ReceiveDraft — GET /ms/receive/draft?id=...: сканы черновика JSON'ом, чтобы
// страница восстановила строки для правки (F5, «продолжить» из списка). Коды и
// имена пользовательские — в страницу JSON не встраиваем (как кеш приёмки).
func (h *Handler) ReceiveDraft(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))

	draft, ok := h.receivingUC.DraftByID(id)
	if !ok {
		http.Error(w, rucase.ErrDraftNotFound.Error(), http.StatusNotFound)

		return
	}

	if draft.Finished() {
		http.Error(w, rucase.ErrDraftFinished.Error(), http.StatusConflict)

		return
	}

	if draftHostToken(r, id) != draft.HostToken {
		http.Error(w, rucase.ErrDraftHost.Error(), http.StatusForbidden)

		return
	}

	resp := struct {
		SupplierID string          `json:"supplier_id"`
		Scans      []draftScanJSON `json:"scans"`
	}{SupplierID: draft.SupplierID, Scans: draftScansJSON(draft.Scans)}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Info(fmt.Sprintf("receive draft encode: %v", err))
	}
}

// draftScanJSON — скан черновика для страницы: даты строками, как их ждут
// поля ввода (YYYY-MM-DD).
type draftScanJSON struct {
	Raw              string          `json:"raw"`
	ManualProductID  string          `json:"manual_product_id,omitempty"`
	ManualWeightG    *int64          `json:"manual_weight_g,omitempty"`
	ManualProducedOn string          `json:"manual_produced_on,omitempty"`
	ManualBestBefore string          `json:"manual_best_before,omitempty"`
	Children         []draftScanJSON `json:"children,omitempty"`
}

// draftScansJSON переводит сканы черновика в вид страницы (даты — строкой).
func draftScansJSON(in []receiving.ScanEntry) []draftScanJSON {
	out := make([]draftScanJSON, 0, len(in))

	for _, s := range in {
		out = append(out, draftScanJSON{
			Raw:              s.Raw,
			ManualProductID:  s.ManualProductID,
			ManualWeightG:    s.ManualWeightG,
			ManualProducedOn: scanDateText(s.ManualProducedOn),
			ManualBestBefore: scanDateText(s.ManualBestBefore),
			Children:         draftScansJSON(s.Children),
		})
	}

	return out
}

// scanDateText — дата скана строкой YYYY-MM-DD; nil — пусто.
func scanDateText(t *time.Time) string {
	if t == nil {
		return ""
	}

	return t.In(mskZone).Format(time.DateOnly)
}

// ReceiveFinish — POST /ms/receive/finish: «Завершить». Пишет остатки, веса и
// уведомления, закрывает приёмку для правки. Ошибка — 400 с текстом: страница
// остаётся на отчёте, приёмку можно исправить и повторить.
func (h *Handler) ReceiveFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DraftID string `json:"draft_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "невалидный JSON запроса", http.StatusBadRequest)

		return
	}

	draftID := strings.TrimSpace(req.DraftID)
	if draftID == "" {
		http.Error(w, "не указана приёмка", http.StatusBadRequest)

		return
	}

	draft, err := h.receivingUC.FinishDraft(r.Context(), draftID, draftHostToken(r, draftID))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, rucase.ErrDraftHost) {
			status = http.StatusForbidden
		}

		http.Error(w, err.Error(), status)

		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if err := json.NewEncoder(w).Encode(struct {
		DraftID string `json:"draft_id"`
		URL     string `json:"url"`
	}{DraftID: draft.ID, URL: "/ms/receive/view?id=" + draft.ID}); err != nil {
		slog.Info(fmt.Sprintf("receive finish encode: %v", err))
	}
}

// receiveViewData — данные страницы финала приёмки: отчёт, наклейки, без правки.
type receiveViewData struct {
	ID           string
	SupplierID   string
	SupplierName string
	Rows         []receiveViewRow
	Warnings     []string
	Mismatch     int // коробок с расхождением: приняты как есть
	NoProducedOn int // кусков без выработки: этикетка не соберётся
	NoBoxBB      int // коробок без срока: наклейка не соберётся
	HasBoxes     bool
	FinishedAt   string
	EndsAt       string
}

// receiveViewRow — строка отчёта с готовой суммой («1.24 кг» / «3 шт»).
type receiveViewRow struct {
	Name       string
	BestBefore string
	Amount     string
}

// ReceiveDraftView — GET /ms/receive/view?id=...: финал приёмки. Доступен и
// хозяину, и остальным машинам склада: печатать наклейки можно с любой, править
// — нельзя (правка только у хозяина по ключу).
func (h *Handler) ReceiveDraftView(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))

	draft, ok := h.receivingUC.DraftByID(id)
	if !ok {
		http.Error(w, rucase.ErrDraftNotFound.Error(), http.StatusNotFound)

		return
	}

	// Незавершённый черновик на страницу финала не отдаём: он ещё правится, и
	// «приёмка завершена» на нём было бы враньём — ведём на правку.
	if !draft.Finished() {
		http.Redirect(w, r, "/ms/receive?id="+draft.SupplierID+"&d="+draft.ID, http.StatusSeeOther)

		return
	}

	data := receiveViewData{
		ID:           draft.ID,
		SupplierID:   draft.SupplierID,
		SupplierName: draft.SupplierName,
		FinishedAt:   draft.FinishedAtText(),
		EndsAt:       draft.EndsAt(),
	}
	if draft.Result != nil {
		data.Rows = make([]receiveViewRow, 0, len(draft.Result.Rows))
		for _, row := range draft.Result.Rows {
			data.Rows = append(data.Rows, receiveViewRow{
				Name:       row.ProductName,
				BestBefore: row.BestBefore,
				Amount:     reportAmount(row),
			})
		}
		data.Warnings = draft.Result.Warnings
		for _, box := range draft.Result.Boxes {
			if box.Mismatch {
				data.Mismatch++
			}
			if box.BestBefore == nil {
				data.NoBoxBB++
			}
		}
		data.HasBoxes = len(draft.Result.Boxes) > 0
		for _, unit := range draft.Result.Units {
			if unit.ProducedOn == nil {
				data.NoProducedOn++
			}
		}
	}

	if err := receiveViewTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("receive view template: %v", err))
	}
}

// reportAmount — «Принято» строкой отчёта: весовой в кг, штучный в штуках.
func reportAmount(row receiving.ReportRow) string {
	if !row.Weighted {
		return strconv.FormatInt(row.Qty, 10) + " шт"
	}

	text := strconv.FormatFloat(row.QtyKg, 'f', 3, 64)
	text = strings.TrimRight(text, "0")
	text = strings.TrimSuffix(text, ".")

	return text + " кг"
}

// draftLabelData — единицы и коробки сохранённой приёмки для печати: страница
// финала шлёт только draft_id, файл собирается по самой приёмке (после F5
// наклейки печатаются те же).
func (h *Handler) draftLabelData(id string) ([]receiving.Unit, []receiving.Box, error) {
	draft, ok := h.receivingUC.DraftByID(id)
	if !ok {
		return nil, nil, rucase.ErrDraftNotFound
	}

	if draft.Result == nil {
		return nil, nil, errors.New("в приёмке нет принятых позиций")
	}

	return draft.Result.Units, draft.Result.Boxes, nil
}

// liveReceive — приёмка в списке страницы приёмок.
type liveReceive struct {
	ID           string
	SupplierID   string
	SupplierName string
	Finished     bool
	FinishedAt   string
	EndsAt       string
	Positions    int
	// Editable — черновик этой машины: из списка можно продолжить правку.
	Editable bool
}

// liveReceives собирает живые приёмки для страницы: черновики (правка — только
// своя машина) и завершённые до 21:00 (финал и наклейки всем).
func (h *Handler) liveReceives(r *http.Request) []liveReceive {
	drafts := h.receivingUC.Drafts()
	out := make([]liveReceive, 0, len(drafts))

	for _, d := range drafts {
		item := liveReceive{
			ID:           d.ID,
			SupplierID:   d.SupplierID,
			SupplierName: d.SupplierName,
			Finished:     d.Finished(),
			FinishedAt:   d.FinishedAtText(),
			EndsAt:       d.EndsAt(),
			Editable:     !d.Finished() && draftHostToken(r, d.ID) == d.HostToken,
		}
		if d.Result != nil {
			item.Positions = len(d.Result.Units)
		}
		out = append(out, item)
	}

	return out
}

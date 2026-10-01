package http

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"

	"fmt"
	"log/slog"
	"warehouseHelper/internal/collab"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/receiving"
)

var receiveTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/receive.html", "../internal/delivery/web/templates/_nav.html"))

// receivePageData — данные страницы приёмки.
type receivePageData struct {
	Suppliers []domain.Supplier // для выбора (когда поставщик ещё не выбран)
	Supplier  *domain.Supplier  // выбранный поставщик (nil — выбор)
	Error     string

	// Open — открытые совместные приёмки: на выборе поставщика их видно списком,
	// из него же подключаются гости (кодов подключения нет, решение владельца
	// 01.10.2026).
	Open []collab.Session

	// Room — комната страницы: nil — обычная приёмка; иначе хост (IsGuest=false)
	// или гость (IsGuest=true).
	Room    *collab.Session
	IsGuest bool
}

// ReceivePage — GET /ms/receive[?id=...]: выбор поставщика или страница
// сканирования (кеш приёмки клиент грузит отдельным JSON-запросом —
// встраивать JSON в страницу нельзя: имена товаров пользовательские).
//
// Совместная приёмка: `?id=<поставщик>&collab=1` открывает комнату для этой
// работы (хост), `?c=<комната>` заходит в уже открытую комнату как гость.
func (h *Handler) ReceivePage(w http.ResponseWriter, r *http.Request) {
	data := receivePageData{}

	suppliers, err := h.msUC.List(r.Context())
	if err != nil {
		slog.Info(fmt.Sprintf("receive: список поставщиков: %v", err))
		http.Error(w, "не удалось получить список поставщиков", http.StatusInternalServerError)

		return
	}

	data.Suppliers = suppliers

	query := r.URL.Query()

	id := strings.TrimSpace(query.Get("id"))
	roomID := strings.TrimSpace(query.Get("c"))
	hostMode := query.Get("collab") == "1"

	// Страница гостя: комната пришла ссылкой из списка открытых приёмок.
	if roomID != "" {
		room, err := h.collabUC.State(roomID)
		if err != nil || room.Closed() {
			data.Error = "совместная приёмка уже сохранена или закрыта"
			data.Open = h.collabUC.List(collab.KindReceive)

			h.renderReceive(w, data)

			return
		}

		id = room.Ref
		data.Room = &room
		data.IsGuest = true
	}

	if id == "" {
		data.Open = h.collabUC.List(collab.KindReceive)

		h.renderReceive(w, data)

		return
	}

	var supplier *domain.Supplier

	for i := range suppliers {
		if suppliers[i].ID == id {
			supplier = &suppliers[i]

			break
		}
	}

	if supplier == nil {
		data.Error = "поставщик не найден"

		h.renderReceive(w, data)

		return
	}

	data.Supplier = supplier

	// Хост: комната открывается (или берётся уже открытая — открытие
	// идемпотентно) прямо при отрисовке страницы, чтобы гости видели её в списке
	// сразу, без отдельного нажатия.
	if hostMode && data.Room == nil {
		room, err := h.collabUC.Open(collab.KindReceive, supplier.ID, supplier.Name)
		if err != nil {
			slog.Info("receive: открыть совместную приёмку", "supplier_id", supplier.ID, "err", err)

			data.Error = "не удалось открыть совместную приёмку: " + err.Error()
		} else {
			data.Room = &room
		}
	}

	h.renderReceive(w, data)
}

// renderReceive отдаёт страницу приёмки; ошибка исполнения уже не чинится (часть
// тела могла уйти) — только в лог.
func (h *Handler) renderReceive(w http.ResponseWriter, data receivePageData) {
	if err := receiveTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("receive template: %v", err))
	}
}

// ReceiveCache — GET /ms/receive/cache?id=...: кеш приёмки поставщика
// (правила, маппинг кодов, каталог) для резолва «на лету» в JS.
func (h *Handler) ReceiveCache(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "не указан поставщик", http.StatusBadRequest)

		return
	}

	cache, err := h.receivingUC.GetCache(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	if err := h.receivingUC.AddCatalogCodes(r.Context(), cache); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if err := json.NewEncoder(w).Encode(cache); err != nil {
		slog.Info(fmt.Sprintf("receive cache encode: %v", err))
	}
}

// receiveSaveScan — DTO запроса Save: даты строками YYYY-MM-DD (time.Time
// из JSON в этом формате не разобрать).
type receiveSaveScan struct {
	Raw              string            `json:"raw"`
	ManualProductID  string            `json:"manual_product_id"`
	ManualWeightG    *int64            `json:"manual_weight_g"`
	ManualProducedOn string            `json:"manual_produced_on"`
	ManualBestBefore string            `json:"manual_best_before"`
	Children         []receiveSaveScan `json:"children"`
}

type receiveSaveRequest struct {
	SupplierID string            `json:"supplier_id"`
	Scans      []receiveSaveScan `json:"scans"`

	// SessionID — совместная приёмка: строки подключённых гостей доклеиваются к
	// строкам хоста, и вся работа сохраняется одним вызовом. Пусто — обычная
	// приёмка одной машины.
	SessionID string `json:"session_id"`
}

// ReceiveSave — POST /ms/receive/save: принять приёмку (JSON), вернуть
// отчёт и данные для печати. Ошибка резолва — 400 с текстом: клиент
// подсвечивает проблемную карточку и не теряет введённое.
//
// Совместная приёмка: пока не все гости прислали сканы, сохранение отвергается
// (409) — кнопка на странице хоста заблокирована, но устаревшая страница не
// должна закрыть приёмку без чужих строк.
func (h *Handler) ReceiveSave(w http.ResponseWriter, r *http.Request) {
	var req receiveSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "невалидный JSON запроса", http.StatusBadRequest)

		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	supplierID := strings.TrimSpace(req.SupplierID)
	scans := req.Scans
	claimed := false

	if sessionID != "" {
		guestScans, taken, err := h.claimGuestScans(sessionID, supplierID)
		if err != nil {
			collabError(w, err)

			return
		}

		claimed = taken
		scans = append(scans, guestScans...)
	}

	saveReq := receiving.SaveRequest{
		SupplierID: supplierID,
		Scans:      toScanEntries(scans),
	}

	result, err := h.receivingUC.Save(r.Context(), saveReq)
	if err != nil {
		// Приёмка не прошла — комнату освобождаем: строки гостей на месте, хост
		// может повторить (в том числе сохранить без гостей).
		if claimed {
			h.releaseRoom(sessionID)
		}

		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	// Приёмка сохранена — комнату закрываем: гости увидят это опросом и получат
	// «приёмка сохранена». Отказ закрытия работу не отменяет.
	if sessionID != "" {
		if _, err := h.collabUC.Close(sessionID); err != nil {
			slog.Info("receive: закрыть совместную приёмку", "session", sessionID, "err", err)
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if err := json.NewEncoder(w).Encode(result); err != nil {
		slog.Info(fmt.Sprintf("receive save encode: %v", err))
	}
}

// toScanEntries конвертирует DTO (даты строками) в домен.
func toScanEntries(in []receiveSaveScan) []receiving.ScanEntry {
	out := make([]receiving.ScanEntry, 0, len(in))

	for _, s := range in {
		out = append(out, receiving.ScanEntry{
			Raw:              s.Raw,
			ManualProductID:  s.ManualProductID,
			ManualWeightG:    s.ManualWeightG,
			ManualProducedOn: parseSaveDate(s.ManualProducedOn),
			ManualBestBefore: parseSaveDate(s.ManualBestBefore),
			Children:         toScanEntries(s.Children),
		})
	}

	return out
}

// parseSaveDate разбирает дату YYYY-MM-DD; пустая строка — nil.
func parseSaveDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil
	}

	return &t
}

package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	ausecase "warehouseHelper/internal/auditsearch/usecase"
)

// auditSearchTmpl — страница «Поиск удалений»: форма (товар + окно дат),
// прогресс-бар и таблица найденных удалений позиций из заказов. Состояние
// скана — в памяти процесса (модуль auditsearch), страница его только
// показывает и опрашивает раз в 2 секунды.
var auditSearchTmpl = template.Must(
	template.New("audit_search.html").
		ParseFiles("../internal/delivery/web/templates/audit_search.html", "../internal/delivery/web/templates/_nav.html"))

// jsonErrorField — ключ сообщения об ошибке в JSON-ответах пакета (одна строка,
// а не литерал в каждом месте: иначе goconst).
const jsonErrorField = "error"

// writeJSONErrorMsg — короткий JSON с ошибкой для ручек, которым нужен текст,
// а не голый статус.
func writeJSONErrorMsg(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{jsonErrorField: msg})
}

// auditSearchService — шов страницы: юзкейс поиска удалений (в тестах —
// фейковый сканер, без сети и МойСклада).
type auditSearchService interface {
	Start(ctx context.Context, productIDs []string, from, to time.Time) (string, error)
	Stop(jobID string) error
	Progress(jobID string) (ausecase.Progress, error)
	Export(jobID string) (string, error)
}

// auditSearchPage — данные страницы: окно по умолчанию (30 дней назад —
// сегодня) в МСК, чтобы форма открывалась с осмысленными датами.
type auditSearchPage struct {
	From string
	To   string
}

// auditSearchMSK — TZ учётки МойСклада для дат по умолчанию (МСК без
// перехода на летнее время).
var auditSearchMSK = time.FixedZone("MSK", 3*60*60)

// AuditSearchPage — GET /ms/audit-search: форма и пустая таблица.
func (h *Handler) AuditSearchPage(w http.ResponseWriter, _ *http.Request) {
	if h.auditSearchUC == nil {
		http.Error(w, "модуль поиска удалений не подключён", http.StatusServiceUnavailable)

		return
	}

	today := time.Now().In(auditSearchMSK)
	page := auditSearchPage{
		From: today.AddDate(0, 0, -30).Format(time.DateOnly),
		To:   today.Format(time.DateOnly),
	}

	if err := auditSearchTmpl.Execute(w, page); err != nil {
		slog.Info(fmt.Sprintf("audit_search template: %v", err))
	}
}

// AuditSearchProducts — GET /ms/audit-search/products?q=...: autocomplete
// товара по нашей базе (id — legacy uuidHref, резолвит его юзкейс).
func (h *Handler) AuditSearchProducts(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeAuditSearchJSON(w, http.StatusOK, []any{})

		return
	}

	if h.goodsUC == nil {
		writeAuditSearchError(w, http.StatusServiceUnavailable, "каталог товаров не подключён")

		return
	}

	products, err := h.goodsUC.SearchProducts(r.Context(), q)
	if err != nil {
		slog.Info(fmt.Sprintf("audit search products: %v", err))
		writeAuditSearchError(w, http.StatusInternalServerError, "не удалось выполнить поиск товара")

		return
	}

	// Не больше 20 подсказок: список нужен для выбора, а не для чтения.
	const limit = 20
	if len(products) > limit {
		products = products[:limit]
	}

	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	out := make([]item, 0, len(products))
	for _, p := range products {
		out = append(out, item{ID: p.ID, Name: p.Name})
	}

	writeAuditSearchJSON(w, http.StatusOK, out)
}

// auditSearchStartRequest — тело POST /ms/audit-search/start (form).
type auditSearchStartRequest struct {
	Products []string `json:"product_id"`
	From     string   `json:"from"`
	To       string   `json:"to"`
}

// AuditSearchStart — POST /ms/audit-search/start: создать задачу скана.
// Ответ {"job": "<uuid>"}; вторая одновременная задача — 409.
func (h *Handler) AuditSearchStart(w http.ResponseWriter, r *http.Request) {
	if h.auditSearchUC == nil {
		writeAuditSearchError(w, http.StatusServiceUnavailable, "модуль поиска удалений не подключён")

		return
	}

	if r.Method != http.MethodPost {
		writeAuditSearchError(w, http.StatusMethodNotAllowed, "метод не поддерживается")

		return
	}

	req, err := parseAuditSearchStart(r)
	if err != nil {
		writeAuditSearchError(w, http.StatusBadRequest, err.Error())

		return
	}

	from, err := time.ParseInLocation(time.DateOnly, req.From, auditSearchMSK)
	if err != nil {
		writeAuditSearchError(w, http.StatusBadRequest, "дата «с» — не дата (ожидается ГГГГ-ММ-ДД)")

		return
	}

	to, err := time.ParseInLocation(time.DateOnly, req.To, auditSearchMSK)
	if err != nil {
		writeAuditSearchError(w, http.StatusBadRequest, "дата «по» — не дата (ожидается ГГГГ-ММ-ДД)")

		return
	}

	jobID, err := h.auditSearchUC.Start(r.Context(), req.Products, from, to)
	if err != nil {
		var invalid *ausecase.ValidationError
		switch {
		case errors.As(err, &invalid):
			writeAuditSearchError(w, http.StatusBadRequest, invalid.Reason)
		case errors.Is(err, ausecase.ErrJobActive):
			writeAuditSearchError(w, http.StatusConflict, "уже идёт поиск — дождитесь окончания или остановите его")
		default:
			slog.Error(fmt.Sprintf("audit search start: %v", err))
			writeAuditSearchError(w, http.StatusInternalServerError, "не удалось запустить поиск")
		}

		return
	}

	writeAuditSearchJSON(w, http.StatusOK, map[string]string{"job": jobID})
}

// parseAuditSearchStart — поля запуска из form (страница шлёт form-urlencoded).
func parseAuditSearchStart(r *http.Request) (auditSearchStartRequest, error) {
	if err := r.ParseForm(); err != nil {
		return auditSearchStartRequest{}, fmt.Errorf("разбор формы: %w", err)
	}

	// product_id повторяется столько раз, сколько товаров добавили к поиску.
	req := auditSearchStartRequest{
		Products: r.PostForm["product_id"],
		From:     strings.TrimSpace(r.PostFormValue("from")),
		To:       strings.TrimSpace(r.PostFormValue("to")),
	}

	if len(req.Products) == 0 {
		return req, errors.New("товар не выбран")
	}

	if req.From == "" || req.To == "" {
		return req, errors.New("заполните обе даты")
	}

	return req, nil
}

// auditSearchResultDTO — строка найденного удаления (JSON страницы).
type auditSearchResultDTO struct {
	Moment   string  `json:"moment"`
	Order    string  `json:"order"`
	OrderID  string  `json:"order_id"`
	Product  string  `json:"product"`
	Query    string  `json:"query"`
	Quantity float64 `json:"quantity"`
	UOM      string  `json:"uom"`
	UID      string  `json:"uid"`
	AuditID  string  `json:"audit_id"`
}

// auditSearchProgressDTO — ответ GET /ms/audit-search/progress (ключи из
// брифа: total/done/found/percent/elapsed_sec/eta_sec/state/error + results).
type auditSearchProgressDTO struct {
	Job        string                 `json:"job"`
	Total      int                    `json:"total"`
	Done       int                    `json:"done"`
	Found      int                    `json:"found"`
	Percent    int                    `json:"percent"`
	ElapsedSec int                    `json:"elapsed_sec"`
	EtaSec     int                    `json:"eta_sec"`
	State      string                 `json:"state"`
	Error      string                 `json:"error"`
	Truncated  bool                   `json:"truncated"`
	Product    string                 `json:"product"`
	Results    []auditSearchResultDTO `json:"results"`
}

// AuditSearchProgress — GET /ms/audit-search/progress?job=...: прогресс,
// ETA и найденные строки (последние 200) для поллинга страницы.
func (h *Handler) AuditSearchProgress(w http.ResponseWriter, r *http.Request) {
	if h.auditSearchUC == nil {
		writeAuditSearchError(w, http.StatusServiceUnavailable, "модуль поиска удалений не подключён")

		return
	}

	jobID := strings.TrimSpace(r.URL.Query().Get("job"))
	if jobID == "" {
		writeAuditSearchError(w, http.StatusBadRequest, "не указана задача")

		return
	}

	p, err := h.auditSearchUC.Progress(jobID)
	if err != nil {
		if errors.Is(err, ausecase.ErrJobNotFound) {
			writeAuditSearchError(w, http.StatusNotFound, "задача не найдена (перезапуск приложения сбрасывает поиск)")

			return
		}

		slog.Error(fmt.Sprintf("audit search progress: %v", err))
		writeAuditSearchError(w, http.StatusInternalServerError, "не удалось прочитать прогресс")

		return
	}

	out := auditSearchProgressDTO{
		Job:        p.Job,
		Total:      p.Total,
		Done:       p.Done,
		Found:      p.Found,
		Percent:    p.Percent,
		ElapsedSec: p.ElapsedSec,
		EtaSec:     p.EtaSec,
		State:      p.State,
		Error:      p.Error,
		Truncated:  p.Truncated,
		Product:    p.Product,
		Results:    make([]auditSearchResultDTO, 0, len(p.Results)),
	}
	for _, res := range p.Results {
		uom := res.UOM
		if uom == "" {
			uom = ausecase.UnknownUOM
		}

		out.Results = append(out.Results, auditSearchResultDTO{
			Moment:   res.MomentText(),
			Order:    res.OrderName,
			OrderID:  res.OrderID,
			Product:  res.ProductName,
			Query:    res.Query,
			Quantity: res.Quantity,
			UOM:      uom,
			UID:      res.UID,
			AuditID:  res.AuditID,
		})
	}

	writeAuditSearchJSON(w, http.StatusOK, out)
}

// AuditSearchStop — POST /ms/audit-search/stop?job=...: флаг отмены, скан
// встаёт на ближайшей итерации, найденное остаётся.
func (h *Handler) AuditSearchStop(w http.ResponseWriter, r *http.Request) {
	if h.auditSearchUC == nil {
		writeAuditSearchError(w, http.StatusServiceUnavailable, "модуль поиска удалений не подключён")

		return
	}

	if r.Method != http.MethodPost {
		writeAuditSearchError(w, http.StatusMethodNotAllowed, "метод не поддерживается")

		return
	}

	jobID := strings.TrimSpace(r.URL.Query().Get("job"))
	if jobID == "" {
		jobID = strings.TrimSpace(r.PostFormValue("job"))
	}

	if jobID == "" {
		writeAuditSearchError(w, http.StatusBadRequest, "не указана задача")

		return
	}

	if err := h.auditSearchUC.Stop(jobID); err != nil {
		if errors.Is(err, ausecase.ErrJobNotFound) {
			writeAuditSearchError(w, http.StatusNotFound, "задача не найдена")

			return
		}

		slog.Error(fmt.Sprintf("audit search stop: %v", err))
		writeAuditSearchError(w, http.StatusInternalServerError, "не удалось остановить поиск")

		return
	}

	writeAuditSearchJSON(w, http.StatusOK, map[string]string{"job": jobID, "state": ausecase.StateStopped})
}

// AuditSearchExport — GET /ms/audit-search/export.xlsx?job=...: выгрузка
// всех найденных строк задачи в xlsx (excelize, tempdir).
func (h *Handler) AuditSearchExport(w http.ResponseWriter, r *http.Request) {
	if h.auditSearchUC == nil {
		http.Error(w, "модуль поиска удалений не подключён", http.StatusServiceUnavailable)

		return
	}

	jobID := strings.TrimSpace(r.URL.Query().Get("job"))
	if jobID == "" {
		http.Error(w, "не указана задача", http.StatusBadRequest)

		return
	}

	path, err := h.auditSearchUC.Export(jobID)
	if err != nil {
		if errors.Is(err, ausecase.ErrJobNotFound) {
			http.Error(w, "задача не найдена", http.StatusNotFound)

			return
		}

		slog.Error(fmt.Sprintf("audit search export: %v", err))
		http.Error(w, "не удалось собрать xlsx", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "audit_deletions.xlsx"))
	//nolint:gosec // путь собран юзкейсом в его tempdir (uuid.xlsx), из запроса не приходит
	http.ServeFile(w, r, path)
}

// writeAuditSearchJSON — JSON-ответ страницы (ошибку кодирования только
// логируем: статус уже отправлен).
func writeAuditSearchJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Info(fmt.Sprintf("audit search json: encode: %v", err))
	}
}

// writeAuditSearchError — ошибка в JSON: страница показывает текст
// оператору, а не голый статус.
func writeAuditSearchError(w http.ResponseWriter, status int, msg string) {
	writeAuditSearchJSON(w, status, map[string]string{jsonErrorField: msg})
}

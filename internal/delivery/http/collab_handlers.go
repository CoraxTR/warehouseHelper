package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"warehouseHelper/internal/collab"
	ccase "warehouseHelper/internal/collab/usecase"
)

// collabMaxBody — потолок тела запроса комнаты: чанк сканов приёмки — это сотни
// строк по ~200 байт, 2 МБ хватает с запасом, а мусор в память не пролезет.
const collabMaxBody = 2 << 20

// collabGuestRequest — обращение к комнате: хост шлёт без guest_id, гость — со
// своим идентификатором; scans — только при отправке чанка.
type collabGuestRequest struct {
	ID      string            `json:"id"`
	GuestID string            `json:"guest_id"`
	Scans   []json.RawMessage `json:"scans"`
	ChunkID string            `json:"chunk_id,omitempty"`
}

// collabGuestDTO — участник для страницы. Сканы не отдаём: они нужны только
// сохранению на сервере, странице — имя, статус и объём отправленного.
type collabGuestDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Ready   bool   `json:"ready"` // отправленное ушло и не меняется: хост не ждёт
	Rows    int    `json:"rows"`
	Chunks  int    `json:"chunks"`
	IdleSec int    `json:"idle_sec"` // сколько секунд молчит (обрыв связи)

	// LastSeq — номер последней принятой строки захода: по нему страница гостя
	// понимает, что уже у хозяина, и после обрыва шлёт только новое.
	LastSeq int64 `json:"last_seq"`
}

// collabSessionDTO — состояние комнаты для страницы хоста и страницы гостя.
type collabSessionDTO struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Ref      string           `json:"ref"`
	Title    string           `json:"title"`
	Closed   bool             `json:"closed"`
	Ready    bool             `json:"ready"`
	Waiting  []string         `json:"waiting"`
	RowsSent int              `json:"rows_sent"`
	Guests   []collabGuestDTO `json:"guests"`
}

// collabStateResponse — ответ состояния: комната + идентификатор гостя, если
// запрос был от гостя (страница держит его в localStorage).
type collabStateResponse struct {
	Session collabSessionDTO `json:"session"`
	GuestID string           `json:"guest_id,omitempty"`
}

// CollabState — GET /ms/collab/state?id=&guest_id=: состояние комнаты для опроса
// страницей. Запрос гостя отмечается как отклик (heartbeat) — по нему хост
// видит, у кого оборвалась связь.
func (h *Handler) CollabState(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	guestID := strings.TrimSpace(r.URL.Query().Get("guest_id"))

	session, err := h.collabUC.State(id)
	if err != nil {
		collabError(w, err)

		return
	}

	resp := collabStateResponse{GuestID: guestID}

	if guestID != "" {
		// Отключённый гость получит ErrGuestGone — это не ошибка опроса: его
		// страница увидит себя в списке отсутствующим и предложит подключиться
		// заново. Молчим, чтобы не сыпать в лог каждые 2 секунды.
		if touched, err := h.collabUC.Touch(id, guestID); err == nil {
			session = touched
		}
	}

	resp.Session = collabSessionDTOOf(session)

	writeCollabJSON(w, resp)
}

// CollabJoin — POST /ms/collab/join: подключить машину к комнате. Пустой
// guest_id — новый гость («Гость N»), знакомый — возврат того же гостя после
// перезагрузки страницы.
func (h *Handler) CollabJoin(w http.ResponseWriter, r *http.Request) {
	var req collabGuestRequest
	if !decodeCollabJSON(w, r, &req) {
		return
	}

	session, err := h.collabUC.Join(strings.TrimSpace(req.ID), strings.TrimSpace(req.GuestID))
	if err != nil {
		collabError(w, err)

		return
	}

	guestID := strings.TrimSpace(req.GuestID)
	if guestID == "" && len(session.Guests) > 0 {
		guestID = session.Guests[len(session.Guests)-1].ID
	}

	writeCollabJSON(w, collabStateResponse{Session: collabSessionDTOOf(session), GuestID: guestID})
}

// CollabSubmit — POST /ms/collab/submit: гость прислал чанк сканов. Строки
// дописываются к отправленным, статус — «готов»; изменить отправленное нельзя.
func (h *Handler) CollabSubmit(w http.ResponseWriter, r *http.Request) {
	var req collabGuestRequest
	if !decodeCollabJSON(w, r, &req) {
		return
	}

	if !validCollabScans(req.Scans) {
		http.Error(w, "В отправленных строках нет ни кода, ни товара", http.StatusBadRequest)

		return
	}

	guestID := strings.TrimSpace(req.GuestID)

	session, err := h.collabUC.Submit(strings.TrimSpace(req.ID), guestID, req.Scans, strings.TrimSpace(req.ChunkID))
	if err != nil {
		collabError(w, err)

		return
	}

	writeCollabJSON(w, collabStateResponse{Session: collabSessionDTOOf(session), GuestID: guestID})
}

// CollabScanning — POST /ms/collab/scanning: гость начал новый чанк, хост снова
// его ждёт (иначе кнопка «Сохранить приёмку» на хосте осталась бы доступной).
func (h *Handler) CollabScanning(w http.ResponseWriter, r *http.Request) {
	var req collabGuestRequest
	if !decodeCollabJSON(w, r, &req) {
		return
	}

	guestID := strings.TrimSpace(req.GuestID)

	session, err := h.collabUC.SetScanning(strings.TrimSpace(req.ID), guestID)
	if err != nil {
		collabError(w, err)

		return
	}

	writeCollabJSON(w, collabStateResponse{Session: collabSessionDTOOf(session), GuestID: guestID})
}

// CollabDrop — POST /ms/collab/drop: хост отключает гостя (обрыв связи): его
// строки в сохранение не идут, хост его больше не ждёт.
func (h *Handler) CollabDrop(w http.ResponseWriter, r *http.Request) {
	var req collabGuestRequest
	if !decodeCollabJSON(w, r, &req) {
		return
	}

	session, err := h.collabUC.Drop(strings.TrimSpace(req.ID), strings.TrimSpace(req.GuestID))
	if err != nil {
		collabError(w, err)

		return
	}

	writeCollabJSON(w, collabStateResponse{Session: collabSessionDTOOf(session)})
}

// CollabClose — POST /ms/collab/close: хост закрывает комнату без сохранения
// (отменил приёмку). Обычный путь — комната закрывается сама после Save.
func (h *Handler) CollabClose(w http.ResponseWriter, r *http.Request) {
	var req collabGuestRequest
	if !decodeCollabJSON(w, r, &req) {
		return
	}

	session, err := h.collabUC.Cancel(strings.TrimSpace(req.ID))
	if err != nil {
		collabError(w, err)

		return
	}

	writeCollabJSON(w, collabStateResponse{Session: collabSessionDTOOf(session)})
}

// claimGuestScans забирает строки гостей на сохранение приёмки: одним вызовом
// проверяет, что комната та же работа, не занята и все гости готовы, и отдаёт
// строки в формате приёмки (хост склеивает их со своими). claimed=false — гостей
// нет: комнату снёс перезапуск приложения или purge, и приёмка сохраняется из
// одних хостовых строк (отказывать оператору из-за этого нельзя).
func (h *Handler) claimGuestScans(sessionID, supplierID string) ([]receiveSaveScan, bool, error) {
	races, err := h.collabUC.Claim(sessionID, supplierID)

	switch {
	case errors.Is(err, collab.ErrNotFound), errors.Is(err, collab.ErrClosed):
		slog.Info("collab: комнаты нет — сохраняю только хостовые строки",
			"session_id", sessionID, "err", err)

		return nil, false, nil
	case err != nil:
		return nil, false, err
	}

	out := make([]receiveSaveScan, 0, len(races))

	for _, raw := range races {
		var scan receiveSaveScan
		if err := json.Unmarshal(raw, &scan); err != nil {
			return nil, false, err
		}

		out = append(out, scan)
	}

	return out, true, nil
}

// releaseRoom снимает «занята» после неудачного сохранения: строки гостей на
// месте, хост может повторить (в т.ч. сохранить без гостей).
func (h *Handler) releaseRoom(sessionID string) {
	if err := h.collabUC.Release(sessionID); err != nil {
		slog.Info("collab: комната не освобождена", "session_id", sessionID, "err", err)
	}
}

// collabSessionDTOOf собирает состояние комнаты для страницы.
func collabSessionDTOOf(session collab.Session) collabSessionDTO {
	now := time.Now()

	guests := make([]collabGuestDTO, 0, len(session.Guests))

	for _, g := range session.Guests {
		idle := 0
		if !g.LastSeen.IsZero() {
			if secs := int(now.Sub(g.LastSeen).Seconds()); secs > 0 {
				idle = secs
			}
		}

		guests = append(guests, collabGuestDTO{
			ID:      g.ID,
			Name:    g.Name,
			Status:  string(g.Status),
			Ready:   g.IsReady(),
			Rows:    g.Rows,
			Chunks:  g.Chunks,
			IdleSec: idle,
			LastSeq: g.LastSeq,
		})
	}

	return collabSessionDTO{
		ID:       session.ID,
		Kind:     string(session.Kind),
		Ref:      session.Ref,
		Title:    session.Title,
		Closed:   session.Closed(),
		Ready:    session.Ready(),
		Waiting:  session.Waiting(),
		RowsSent: session.RowsSent(),
		Guests:   guests,
	}
}

// decodeCollabJSON разбирает тело запроса; отказ уже отдан клиенту.
func decodeCollabJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, collabMaxBody)

	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "невалидный JSON запроса", http.StatusBadRequest)

		return false
	}

	return true
}

// validCollabScans проверяет строки гостя: каждая — скан приёмки, то есть несёт
// код (raw) или выбранный товар (manual_product_id). Пустой объект и мусор
// отсекаем на входе: иначе они доедут до сохранения и сломают приёмку хосту.
func validCollabScans(scans []json.RawMessage) bool {
	for _, raw := range scans {
		if !validCollabScan(raw, 0) {
			return false
		}
	}

	return true
}

// validCollabScan — проверка одной строки; вложения коробки проверяются
// рекурсивно (глубже «коробка → вложения» структуры у приёмки нет).
func validCollabScan(raw json.RawMessage, depth int) bool {
	if depth > 2 {
		return false
	}

	var scan struct {
		Raw             string            `json:"raw"`
		ManualProductID string            `json:"manual_product_id"`
		Children        []json.RawMessage `json:"children"`
	}

	if err := json.Unmarshal(raw, &scan); err != nil {
		return false
	}

	if strings.TrimSpace(scan.Raw) == "" && strings.TrimSpace(scan.ManualProductID) == "" {
		return false
	}

	for _, child := range scan.Children {
		if !validCollabScan(child, depth+1) {
			return false
		}
	}

	return true
}

// writeCollabJSON отдаёт ответ комнаты странице.
func writeCollabJSON(w http.ResponseWriter, resp collabStateResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Info("collab: ответ не отправлен", "err", err)
	}
}

// collabError переводит ошибки комнаты в коды и понятные оператору тексты.
func collabError(w http.ResponseWriter, err error) {
	if notReady, ok := errors.AsType[*collab.NotReadyError](err); ok {
		http.Error(w, notReady.Error(), http.StatusConflict)

		return
	}

	switch {
	case errors.Is(err, collab.ErrGuestGone):
		http.Error(w, "Машина отключена от приёмки хостом", http.StatusNotFound)
	case errors.Is(err, collab.ErrNotFound):
		http.Error(w, "Совместная приёмка не найдена — её уже сохранили или закрыли", http.StatusNotFound)
	case errors.Is(err, collab.ErrClosed):
		http.Error(w, "Совместная приёмка уже закрыта", http.StatusConflict)
	case errors.Is(err, collab.ErrAlreadySaved):
		http.Error(w, "Приёмка по этой ссылке уже сохранена — вторую не создаём. "+
			"Проверьте приёмку в МойСклад.", http.StatusConflict)
	case errors.Is(err, collab.ErrBusy):
		http.Error(w, "Совместная приёмка сохраняется — подождите пару секунд", http.StatusConflict)
	case errors.Is(err, collab.ErrRefMismatch):
		http.Error(w, "Комната открыта для другого поставщика", http.StatusConflict)
	case errors.Is(err, collab.ErrEmpty):
		http.Error(w, "Нет строк для отправки", http.StatusBadRequest)
	case errors.Is(err, collab.ErrKind), errors.Is(err, ccase.ErrNeedRef):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		slog.Info("collab: ошибка комнаты", "err", err)
		http.Error(w, "Ошибка совместной приёмки", http.StatusInternalServerError)
	}
}

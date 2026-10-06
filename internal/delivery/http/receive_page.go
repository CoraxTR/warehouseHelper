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
	ccase "warehouseHelper/internal/collab/usecase"
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

	// Room — комната страницы: приёмка всегда идёт в комнате, поэтому nil только
	// при ошибке открытия. IsGuest — эта машина не хост: сканы уходят хосту.
	Room    *collab.Session
	IsGuest bool

	// Others — живые приёмки того же поставщика на других машинах: подсказка
	// хозяину, иначе один поставщик примут дважды, каждый в своей приёмке.
	Others []collab.Session
}

// ReceivePage — GET /ms/receive[?id=...]: выбор поставщика или страница
// сканирования (кеш приёмки клиент грузит отдельным JSON-запросом —
// встраивать JSON в страницу нельзя: имена товаров пользовательские).
//
// Приёмка всегда идёт в комнате: комната заводится на каждое начало приёмки, а
// хозяином становится машина, которая открыла её первой (решение владельца
// 01.10.2026). Отдельной кнопки «Совместная приёмка» нет, хозяин не меняется: с
// чужой машины к идущей приёмке можно только подключиться гостем — `?c=<комната>`
// или тем же `?id=<поставщик>`.
//
// Хозяин опознаётся по ключу комнаты (cookie `collab_host_<комната>`), а не по
// адресу машины: весь склад ходит через VPN, и адрес у всех один.
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
	// Страница гостя: комната пришла ссылкой из списка открытых приёмок.
	if roomID != "" {
		room, err := h.collabUC.State(roomID)
		if err != nil || room.Closed() || room.Kind != collab.KindReceive {
			data.Error = "совместная приёмка уже сохранена или закрыта"
			data.Open = h.collabUC.List(collab.KindReceive)

			h.renderReceive(w, data)

			return
		}

		id = room.Ref
		data.Room = &room
		data.IsGuest = !roomHost(r, room)
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

	// Комната открывается прямо при отрисовке: гости видят приёмку в списке
	// сразу. Своя приёмка — по ключу хозяина в cookie (перезагрузка страницы
	// продолжает её же); если ключа нет, заводим новую, даже когда у поставщика
	// висит чужая комната: её убьёт TTL, тупика «приёмку не начать» быть не должно.
	if data.Room == nil {
		room, created, err := h.openReceiveRoom(r, w, supplier.ID, supplier.Name)
		if err != nil {
			slog.Info("receive: открыть приёмку", "supplier_id", supplier.ID, "err", err)

			data.Error = "не удалось открыть приёмку: " + err.Error()
		} else {
			data.Room = &room
			// Хозяин — машина, открывшая приёмку (её ключ в cookie). Вторая машина
			// на того же поставщика получает ту же комнату без ключа и сразу
			// становится гостем: иначе её панель хоста могла бы сохранить приёмку.
			data.IsGuest = !created && !roomHost(r, room)
			data.Others = h.otherRooms(collab.KindReceive, supplier.ID, room.ID)
		}
	}

	h.renderReceive(w, data)
}

// openReceiveRoom выдаёт машине комнату приёмки: есть ключ живой приёмки этого
// поставщика — отдаём её же (перезагрузка страницы не заводит вторую), нет —
// заводим новую и выдаём ключ хозяина.
func (h *Handler) openReceiveRoom(r *http.Request, w http.ResponseWriter, supplierID, title string) (collab.Session, bool, error) {
	if room, ok := hostRoom(r, h.collabUC, collab.KindReceive, supplierID); ok {
		return room, false, nil
	}

	room, created, err := h.collabUC.Open(collab.KindReceive, supplierID, title)
	if err != nil {
		return collab.Session{}, false, err
	}

	if created {
		setRoomHostCookie(w, room)
	}

	return room, created, nil
}

// hostRoom ищет живую работу вида kind по ref, ключ которой есть у машины: ключ
// лежит в cookie комнаты (HostCookieName). Вид задаёт вызывающий — функция общая
// для приёмки и инвентаризации, чтобы cookie одной работы не выдавала роль в другой.
func hostRoom(r *http.Request, uc *ccase.UseCase, kind collab.Kind, ref string) (collab.Session, bool) {
	for _, cookie := range r.Cookies() {
		roomID := collab.RoomIDFromHostCookie(cookie.Name)
		if roomID == "" {
			continue
		}

		room, err := uc.State(roomID)
		if err != nil || room.Kind != kind || room.Ref != ref {
			continue
		}

		if room.HostTokenMatches(cookie.Value) {
			return room, true
		}
	}

	return collab.Session{}, false
}

// otherRooms — живые работы вида kind с тем же ref, но с других машин.
func (h *Handler) otherRooms(kind collab.Kind, ref, ownRoomID string) []collab.Session {
	var out []collab.Session

	for _, room := range h.collabUC.List(kind) {
		if room.ID != ownRoomID && room.Ref == ref {
			out = append(out, room)
		}
	}

	return out
}

// roomHost — пришёл ли запрос с ключом хозяина этой комнаты.
func roomHost(r *http.Request, room collab.Session) bool {
	cookie, err := r.Cookie(collab.HostCookieName(room.ID))
	if err != nil {
		return false
	}

	return room.HostTokenMatches(cookie.Value)
}

// hasHostCookie — у машины есть cookie-ключ хозяина комнаты sessionID. Нужен,
// когда комнаты на сервере уже нет (рестарт приложения, TTL, «Отменить»): ключ
// живёт дольше комнаты и отличает машину, начавшую работу, от гостя, у которого
// в session_id та же комната, но ключа нет.
func hasHostCookie(r *http.Request, sessionID string) bool {
	for _, c := range r.Cookies() {
		if collab.HostKeyLooksLike(c.Name, c.Value, sessionID) {
			return true
		}
	}

	return false
}

// setRoomHostCookie отдаёт ключ хозяина машине, открывшей приёмку. Ключ живёт в
// браузере этой машины: перезагрузка роль не снимает, а VPN, скрывающий адреса,
// на него не влияет. Срок — с запасом к сроку жизни комнаты (12 ч против 6 ч).
func setRoomHostCookie(w http.ResponseWriter, room collab.Session) {
	// Приложение живёт в локальной сети по HTTP: Secure-cookie браузер по http не
	// отправит, и хозяин приёмки потерял бы роль. Поэтому Secure не выставляем —
	// вместо него HttpOnly (ключ не виден скриптам страницы) и SameSite.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // локальная сеть, только HTTP
		Name:     collab.HostCookieName(room.ID),
		Value:    room.HostToken,
		Path:     "/",
		MaxAge:   int((12 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
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
		// Сохранить приёмку может только машина, начавшая её: у гостя ключа
		// хозяина нет (его кнопка и не активна). Комната могла уже исчезнуть
		// (рестарт приложения, TTL, «Отменить») — тогда роль доказывает
		// cookie-ключ: без этой проверки вторая машина с тем же session_id
		// сохранила бы приёмку за хозяина.
		room, stateErr := h.collabUC.State(sessionID)

		host := stateErr == nil && roomHost(r, room)
		if stateErr != nil {
			host = hasHostCookie(r, sessionID)
		}

		if !host {
			http.Error(w, "сохранить приёмку может только машина, начавшая её", http.StatusForbidden)

			return
		}

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

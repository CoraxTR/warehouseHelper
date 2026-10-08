package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"warehouseHelper/internal/collab"
	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/inventory"
	iucase "warehouseHelper/internal/inventory/usecase"
)

// Страницы инвентаризации (хаб «Продукция»): выбор вида (products.inventory_type),
// сканирование позиций вида с локальным предпросмотром и создание документа в МС.
// Клиент только собирает сканы; состав группы, количества и деньги считает сервер.
// Маршруты — в router.go.

var (
	inventoryTypesTmpl = template.Must(template.ParseFiles(
		"../internal/delivery/web/templates/goods_inventory.html",
		"../internal/delivery/web/templates/_nav.html",
	))
	inventoryScanTmpl = template.Must(template.ParseFiles(
		"../internal/delivery/web/templates/goods_inventory_scan.html",
		"../internal/delivery/web/templates/_nav.html",
	))
)

// goodsInventoryData — данные страницы выбора вида инвентаризации.
type goodsInventoryData struct {
	Error      string
	Types      []string
	StoreReady bool

	// Open — идущие совместные инвентаризации: их видно списком, из него же
	// подключаются гости (кодов подключения нет — как у приёмки, решение
	// владельца 06.10.2026).
	Open []collab.Session
}

// goodsInventoryScanData — данные страницы сканирования: вид, группа для клиента
// (JSON в data-атрибут), допустимые длины штрих-кодов и состояние склада МС.
type goodsInventoryScanData struct {
	Type       string
	GroupJSON  string
	Lengths    string
	StoreReady bool
	Error      string

	// Room — комната страницы: инвентаризация всегда идёт в комнате, поэтому nil
	// только при ошибке открытия. IsGuest — эта машина не хозяин: сканы уходят
	// хосту, документ в МС создаёт хозяин.
	Room    *collab.Session
	IsGuest bool

	// Others — живые инвентаризации того же вида на других машинах: подсказка
	// хозяину, иначе один вид проведут дважды, каждый в свой документ.
	Others []collab.Session
}

// invGroupItem — позиция группы для клиента: код склада, имя, весовой ли товар,
// единица учёта («кг»/«шт»/…) и делитель граммов для показа факта (клиент
// показывает вес в единицах товара, а не всегда в килограммах).
type invGroupItem struct {
	C string  `json:"c"`
	N string  `json:"n"`
	W int     `json:"w"`
	U string  `json:"u"`
	D float64 `json:"d"`
}

// invMaxBody — потолок тела запросов инвентаризации: тело несёт два массива
// штрих-кодов (общая строка и «Отложка») на заход; 2 МБ хватает с запасом, а
// мусор в память не пролезет (как collabMaxBody у комнаты).
const invMaxBody = 2 << 20

// invPreviewRequest — вход предпросмотра и проведения (одинаковый).
type invPreviewRequest struct {
	Type  string   `json:"type"`
	Scans []string `json:"scans"`
	// Hold — сканы строки «Отложка»: товар под заказами, физически лежащий на
	// складе. Его количество складывается с общей строкой в позицию документа
	// МС, но в сроки годности отложка не идёт вообще (единицы уже списаны
	// подбором). Поле необязательное: нет — nil.
	Hold      []string `json:"hold"`
	SessionID string   `json:"session_id"`
}

// invPreviewRow — строка предпросмотра: только строки, никаких сырых float и
// указателей (форматирование — на сервере).
type invPreviewRow struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Qty       string `json:"qty"`
	HoldQty   string `json:"holdQty"`
	Price     string `json:"price"`
	Scans     int    `json:"scans"`
	HoldScans int    `json:"holdScans"`
	Scanable  bool   `json:"scanable"`
	Zero      bool   `json:"zero"`
	SrokiZero bool   `json:"srokiZero"`
}

// invPreviewResponse — ответ предпросмотра (rows — все позиции группы).
type invPreviewResponse struct {
	OK        bool `json:"ok"`
	Total     int  `json:"total"`
	Scanned   int  `json:"scanned"`
	Scans     int  `json:"scans"`
	HoldScans int  `json:"holdScans"`
	AllZero   bool `json:"allZero"`
	// SrokiSkip — шаг сроков не побежит: в общей строке нет ни одного скана
	// (страховка не обнуляет вид). Страница вместо построчных меток обнуления
	// пишет оператору, что сроки не будут меняться.
	SrokiSkip bool            `json:"srokiSkip"`
	Rows      []invPreviewRow `json:"rows"`
}

// invConductResponse — ответ проведения: созданный документ МС и итог шага
// сроков годности (updated — сроки заменены, skipped — общих сканов нет,
// сроки не трогали).
type invConductResponse struct {
	OK    bool   `json:"ok"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	ID    string `json:"id"`
	Sroki string `json:"sroki"`
}

// invErrorResponse — ошибка JSON-эндпоинта.
type invErrorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// GoodsInventoryPage — GET /goods/inventory: выбор вида инвентаризации.
// Ошибка чтения видов — в Error шаблона, страница остаётся живой (не 500).
func (h *Handler) GoodsInventoryPage(w http.ResponseWriter, r *http.Request) {
	h.renderInventoryPicker(w, h.inventoryPickerData(r.Context(), ""))
}

// inventoryPickerData собирает данные страницы выбора вида: виды инвентаризации
// и идущие совместные инвентаризации (список, из которого подключаются гости).
// Ошибка чтения видов — в Error шаблона, страница остаётся живой (не 500).
func (h *Handler) inventoryPickerData(ctx context.Context, errMsg string) goodsInventoryData {
	data := goodsInventoryData{
		StoreReady: h.inventoryUC.StoreConfigured(),
		Error:      errMsg,
		Open:       h.collabUC.List(collab.KindInventory),
	}

	types, err := h.inventoryUC.Types(ctx)
	if err != nil {
		slog.Error(fmt.Sprintf("inventory types: %v", err))

		if data.Error == "" {
			data.Error = "не удалось прочитать виды инвентаризации: " + err.Error()
		}
	} else {
		data.Types = types
	}

	return data
}

// renderInventoryPicker отдаёт страницу выбора вида; ошибка исполнения уже не
// чинится (часть тела могла уйти) — только в лог.
func (h *Handler) renderInventoryPicker(w http.ResponseWriter, data goodsInventoryData) {
	if err := inventoryTypesTmpl.Execute(w, data); err != nil {
		slog.Error(fmt.Sprintf("inventory types template: %v", err))
	}
}

// GoodsInventoryScanPage — GET /goods/inventory/scan?type=…[&c=…]: сканирование
// позиций выбранного вида. `?type=` — страница хозяина: комната заводится всегда
// при отрисовке (как у приёмки). `?c=<комната>` — страница гостя: вид берётся из
// комнаты, а не из query. Пустой вид — редирект на выбор вида; закрытая/чужая
// комната гостя — понятный текст и список идущих (не 500); ошибка чтения группы —
// Error в шаблоне (сканы всё равно отбиваются сервером).
func (h *Handler) GoodsInventoryScanPage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	inventoryType := strings.TrimSpace(query.Get("type"))
	roomID := strings.TrimSpace(query.Get("c"))

	// Страница гостя: комната пришла ссылкой из списка идущих инвентаризаций.
	if roomID != "" {
		room, err := h.collabUC.State(roomID)
		if err != nil || room.Closed() || room.Kind != collab.KindInventory {
			h.renderInventoryPicker(w, h.inventoryPickerData(r.Context(),
				"совместная инвентаризация уже проведена или закрыта — выберите вид заново"))

			return
		}

		data := h.inventoryScanData(r, room.Ref)
		data.Room = &room
		data.IsGuest = !roomHost(r, room)

		h.renderInventoryScan(w, data)

		return
	}

	if inventoryType == "" {
		http.Redirect(w, r, "/goods/inventory", http.StatusFound)

		return
	}

	data := h.inventoryScanData(r, inventoryType)

	// Комната заводится прямо при отрисовке: гости видят инвентаризацию в списке
	// сразу. Своя — по ключу хозяина в cookie (F5 продолжает её же); нет ключа —
	// заводим новую, даже когда у вида висит чужая комната: её убьёт TTL, тупика
	// «инвентаризацию не начать» быть не должно.
	room, created, err := h.openInventoryRoom(r, w, inventoryType)
	if err != nil {
		slog.Error(fmt.Sprintf("inventory: открыть комнату: %v", err))
		data.Error = "не удалось открыть совместную инвентаризацию: " + err.Error()
	} else {
		data.Room = &room
		// Хозяин — машина, открывшая комнату (её ключ в cookie). Вторая машина на
		// тот же вид получит ту же комнату без ключа и сразу становится гостем:
		// иначе её панель хоста упиралась бы в 403 на проведении.
		data.IsGuest = !created && !roomHost(r, room)
		data.Others = h.otherRooms(collab.KindInventory, inventoryType, room.ID)
	}

	h.renderInventoryScan(w, data)
}

// inventoryScanData — общая часть страницы сканирования: вид, группа для
// клиента, допустимые длины кодов и состояние склада МС.
func (h *Handler) inventoryScanData(r *http.Request, inventoryType string) goodsInventoryScanData {
	data := goodsInventoryScanData{
		Type:       inventoryType,
		GroupJSON:  "[]",
		Lengths:    invLengthsText(),
		StoreReady: h.inventoryUC.StoreConfigured(),
	}

	products, err := h.inventoryUC.Group(r.Context(), inventoryType)
	if err != nil {
		slog.Error(fmt.Sprintf("inventory group: %v", err))
		data.Error = "не удалось прочитать позиции вида: " + err.Error()
	} else {
		data.GroupJSON = invGroupJSON(products)
	}

	return data
}

// renderInventoryScan отдаёт страницу сканирования.
func (h *Handler) renderInventoryScan(w http.ResponseWriter, data goodsInventoryScanData) {
	if err := inventoryScanTmpl.Execute(w, data); err != nil {
		slog.Error(fmt.Sprintf("inventory scan template: %v", err))
	}
}

// GoodsInventoryPreview — POST /goods/inventory/preview: предпросмотр документа
// по сканам. body: {"type":"…","scans":["…"],"hold":["…"]}. 200 — все позиции
// группы с фактическим количеством (итог = общая строка + отложка); 400 — вид не
// задан / скан невалиден / не из группы.
func (h *Handler) GoodsInventoryPreview(w http.ResponseWriter, r *http.Request) {
	req, ok := invDecodeRequest(w, r)
	if !ok {
		return
	}

	preview, err := h.inventoryUC.Preview(r.Context(), req.Type, req.Scans, req.Hold)
	if err != nil {
		invWriteJSONError(w, err)

		return
	}

	invWriteJSON(w, http.StatusOK, invPreviewBody(preview))
}

// GoodsInventoryConduct — POST /goods/inventory/conduct: создать документ
// инвентаризации в МС. body: {"type":"…","scans":["…"],"hold":["…"],"session_id":"…"}.
// Порядок шагов сервера: СНАЧАЛА сроки годности, ПОТОМ документ МС. Сбой сроков —
// документ не создаётся, оператор видит ошибку.
// 200 — документ и итог сроков (sroki: updated|skipped); 400 — вид/сканы (пусто,
// не из группы, пустая группа); 403 — проведение запросила не машина-хозяин;
// 409 — комната занята/не готова; прочее (в т.ч. склад МС не настроен и ошибки
// МС/сроков) — 500 с текстом в error.
//
// Совместная инвентаризация: session_id непустой — строки подключённых гостей
// доклеиваются к строкам хоста (общая строка и отложка отдельно), и весь вид
// уходит в ОДИН вызов Conduct (документ в МС создаёт только хозяин). Пока не все
// гости прислали сканы, проведение отвергается (409): кнопка на странице хоста
// заблокирована, но устаревшая страница не должна провести без чужих строк.
func (h *Handler) GoodsInventoryConduct(w http.ResponseWriter, r *http.Request) {
	req, ok := invDecodeRequest(w, r)
	if !ok {
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	scans := req.Scans
	hold := req.Hold
	claimed := false

	if sessionID != "" {
		// Провести может только машина, начавшая инвентаризацию: у гостя в
		// cookie ключа хозяина нет, и он получит отказ (страница гостя кнопку
		// проведения и не показывает). Комнаты на сервере может уже не быть
		// (рестарт приложения, TTL, «Отменить») — роль и тогда доказывает
		// cookie-ключ: без этой проверки гость со своим session_id создал бы
		// документ по неполным сканам. Ключ живёт 12 ч против TTL комнаты 6 ч,
		// поэтому «хозяин проводит после сноса комнаты» не ломается.
		room, stateErr := h.collabUC.State(sessionID)

		host := stateErr == nil && roomHost(r, room)
		if stateErr != nil {
			host = hasHostCookie(r, sessionID)
		}

		if !host {
			invWriteJSON(w, http.StatusForbidden, invErrorResponse{
				Error: "провести инвентаризацию может только машина, начавшая её",
			})

			return
		}

		guestRows, taken, claimErr := h.claimGuestInventoryScans(sessionID, req.Type)
		if claimErr != nil {
			invCollabError(w, claimErr)

			return
		}

		claimed = taken

		merged, mergeErr := iucase.MergeGuestScans(scans, hold, guestRows)
		if mergeErr != nil {
			if claimed {
				h.releaseRoom(sessionID)
			}

			invWriteJSON(w, http.StatusBadRequest, invErrorResponse{
				Error: "строка гостя не разобрана: " + mergeErr.Error(),
			})

			return
		}

		scans = merged.Scans
		hold = merged.Hold
	}

	// Общая строка и отложка вместе пусты — создавать документ не из чего.
	if len(scans)+len(hold) == 0 {
		invWriteJSON(w, http.StatusBadRequest, invErrorResponse{
			Error: "нет сканов — создавать документ не из чего",
		})

		return
	}

	doc, outcome, err := h.inventoryUC.Conduct(r.Context(), req.Type, scans, hold)
	if err != nil {
		// Инвентаризация не прошла — комнату освобождаем: строки гостей на месте,
		// хост может повторить (в том числе провести без гостей).
		if claimed {
			h.releaseRoom(sessionID)
		}

		// Сроки уже заменены, а документ не создался: об этом оператор обязан
		// узнать — обнуление необратимо, а повторное проведение по тем же сканам
		// его не изменит (шаг идемпотентен). Ошибку оборачиваем, сохраняя цепочку
		// (код ответа считает invErrorStatus по исходной ошибке).
		if outcome == iucase.SrokiUpdated {
			invWriteJSONError(w, fmt.Errorf(
				"%w · сроки годности уже обновлены по сканам общей строки, повторное проведение их не изменит", err))

			return
		}

		invWriteJSONError(w, err)

		return
	}

	// Документ создан — комнату закрываем: гости увидят это опросом и получат
	// «инвентаризация проведена». Отказ закрытия работу не отменяет.
	if sessionID != "" {
		if _, err := h.collabUC.Close(sessionID); err != nil {
			slog.Info("inventory: закрыть совместную инвентаризацию", "session", sessionID, "err", err)
		}
	}

	invWriteJSON(w, http.StatusOK, invConductResponse{
		OK:    true,
		Name:  doc.Name,
		URL:   doc.URL,
		ID:    doc.ID,
		Sroki: string(outcome),
	})
}

// invDecodeRequest читает тело предпросмотра/проведения и проверяет вид: пустой
// вид без смысла (страница обязана была его подставить) — 400.
func invDecodeRequest(w http.ResponseWriter, r *http.Request) (invPreviewRequest, bool) {
	var req invPreviewRequest

	r.Body = http.MaxBytesReader(w, r.Body, invMaxBody)

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		invWriteJSON(w, http.StatusBadRequest, invErrorResponse{Error: "не удалось прочитать запрос"})

		return invPreviewRequest{}, false
	}
	if strings.TrimSpace(req.Type) == "" {
		invWriteJSON(w, http.StatusBadRequest, invErrorResponse{Error: iucase.ErrNoType.Error()})

		return invPreviewRequest{}, false
	}

	return req, true
}

// invGroupJSON — группа для клиентской карты code → {имя, весовой}. Позиции без
// кода склада не сканируются — в JSON не идут.
func invGroupJSON(products []inventory.Product) string {
	items := make([]invGroupItem, 0, len(products))
	for _, p := range products {
		if strings.TrimSpace(p.InternalCode) == "" {
			continue
		}
		w := 0
		if inventory.Weighted(p.UOM) {
			w = 1
		}
		items = append(items, invGroupItem{
			C: p.InternalCode,
			N: p.Name,
			W: w,
			U: invUnitLabel(p.UOM),
			D: invWeightDivisor(p.UOM),
		})
	}

	raw, err := json.Marshal(items)
	if err != nil {
		slog.Error(fmt.Sprintf("inventory group json: %v", err))

		return "[]"
	}

	return string(raw)
}

// invLengthsText — допустимые длины внутренних штрих-кодов («29,33») от
// владельца формата (innercode.ValidLengths), не хардкод.
func invLengthsText() string {
	lengths := innercode.ValidLengths()
	parts := make([]string, 0, len(lengths))
	for _, l := range lengths {
		parts = append(parts, strconv.Itoa(l))
	}

	return strings.Join(parts, ",")
}

// invPreviewBody — отчёт для клиента: все строки группы, отформатированные деньги
// и количества. Qty — ИТОГ строки (общая + отложка) в единицах товара; holdQty —
// текст добавки отложки, только когда отложка есть (иначе пусто). allZero — ни
// одной ненулевой строки (нечего проводить). srokiZero — товар с кодом склада без
// общих сканов: при проведении все его сроки годности обнулятся. srokiSkip — шаг
// сроков не побежит вообще (в общей строке нет ни одного скана), и тогда метки
// обнуления не рисуются: они врали бы оператору с точностью до наоборот.
func invPreviewBody(p inventory.Preview) invPreviewResponse {
	rows := make([]invPreviewRow, 0, len(p.Lines))
	allZero := true
	// srokiRun — побежит ли шаг сроков: без единого скана общей строки он
	// пропускается (страховка Conduct), значит построчные метки обнуления
	// показывать нельзя.
	srokiRun := p.Scans > 0
	for _, line := range p.Lines {
		zero := line.Total() == 0
		if !zero {
			allZero = false
		}
		qty := "0"
		if !zero {
			// Итог строки показываем только у просканированных позиций: у
			// непросканированных он ноль, а «0 кг / 0 шт» читается как ошибка.
			qty = invQtyText(line.Total(), line)
		}
		holdQty := ""
		if line.HoldScans > 0 {
			holdQty = invQtyText(line.HoldFact, line)
		}
		rows = append(rows, invPreviewRow{
			Code:      line.InternalCode,
			Name:      line.Name,
			Qty:       qty,
			HoldQty:   holdQty,
			Price:     invRublesText(line.PriceKop),
			Scans:     line.Scans,
			HoldScans: line.HoldScans,
			Scanable:  line.Scanable,
			Zero:      zero,
			// Товар с кодом склада и без общих сканов: чего не нашли в общей
			// строке — при проведении теряет все лоты (сроки обнулятся).
			SrokiZero: srokiRun && line.Scanable && line.Scans == 0,
		})
	}

	return invPreviewResponse{
		OK:        true,
		Total:     p.Total,
		Scanned:   p.Scanned,
		Scans:     p.Scans,
		HoldScans: p.HoldScans,
		AllZero:   allZero,
		SrokiSkip: !srokiRun,
		Rows:      rows,
	}
}

// invQtyText — количество в единицах товара строкой: весовой — с точностью
// единицы (кг/г/т), штучный — целое. Значение — аргумент, а line — источник
// вида товара и подписи единицы: так одним хелпером печатается и итог строки, и
// добавка отложки; отдельного флага «весовой» нет (revive flag-parameter).
func invQtyText(value float64, line inventory.Line) string {
	if !line.Weighted {
		return strconv.FormatFloat(value, 'f', 0, 64) + " " + invUnitLabel(line.UOM)
	}

	decimals := inventory.WeightDecimals(line.UOM)

	return strconv.FormatFloat(value, 'f', decimals, 64) + " " + invUnitLabel(line.UOM)
}

// invUnitLabel — подпись единицы в предпросмотре: uom товара как есть; пустая
// единица — штучный товар без единицы в МС (весовой её всегда имеет: кг/г/т).
func invUnitLabel(uom string) string {
	if u := strings.TrimSpace(uom); u != "" {
		return u
	}

	return "шт"
}

// invWeightDivisor — во что граммы штрих-кода превращаются в единицах учёта
// товара: кг → 1000, г → 1, т → 1 000 000; 0 — товар не весовой (вес из
// штрих-кода не значит ничего, факт считается штуками).
func invWeightDivisor(uom string) float64 {
	switch strings.ToLower(strings.TrimSpace(uom)) {
	case "кг":
		return 1000
	case "г":
		return 1
	case "т":
		return 1000000
	default:
		return 0
	}
}

// invRublesText — копейки в рубли с двумя знаками («450.00»); 0 — не задана.
func invRublesText(kop int64) string {
	return strconv.FormatFloat(float64(kop)/100, 'f', 2, 64)
}

// invErrorStatus — 400 для доменных ошибок ввода (вид, скан, пустая группа),
// 500 — для всего прочего (каталог, склад МС не настроен, ответ МС).
func invErrorStatus(err error) int {
	switch {
	case errors.Is(err, iucase.ErrNoType),
		errors.Is(err, iucase.ErrEmptyGroup),
		errors.Is(err, inventory.ErrScanInvalid),
		errors.Is(err, inventory.ErrScanNotInGroup):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// invWriteJSONError — отдать ошибку текстом в error: 400 для доменных случаев,
// 500 (с записью в лог, без токенов) — для прочих.
func invWriteJSONError(w http.ResponseWriter, err error) {
	status := invErrorStatus(err)
	if status == http.StatusInternalServerError {
		slog.Error(fmt.Sprintf("inventory: %v", err))
	}

	invWriteJSON(w, status, invErrorResponse{Error: err.Error()})
}

// invWriteJSON — ответ JSON-эндпоинта инвентаризации.
func invWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error(fmt.Sprintf("inventory json: %v", err))
	}
}

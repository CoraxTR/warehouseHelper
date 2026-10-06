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
}

// goodsInventoryScanData — данные страницы сканирования: вид, группа для клиента
// (JSON в data-атрибут), допустимые длины штрих-кодов и состояние склада МС.
type goodsInventoryScanData struct {
	Type       string
	GroupJSON  string
	Lengths    string
	StoreReady bool
	Error      string
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

// invPreviewRequest — вход предпросмотра и проведения (одинаковый).
type invPreviewRequest struct {
	Type  string   `json:"type"`
	Scans []string `json:"scans"`
}

// invPreviewRow — строка предпросмотра: только строки, никаких сырых float и
// указателей (форматирование — на сервере).
type invPreviewRow struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Qty      string `json:"qty"`
	Price    string `json:"price"`
	Scans    int    `json:"scans"`
	Scanable bool   `json:"scanable"`
	Zero     bool   `json:"zero"`
}

// invPreviewResponse — ответ предпросмотра (rows — все позиции группы).
type invPreviewResponse struct {
	OK      bool            `json:"ok"`
	Total   int             `json:"total"`
	Scanned int             `json:"scanned"`
	Scans   int             `json:"scans"`
	AllZero bool            `json:"allZero"`
	Rows    []invPreviewRow `json:"rows"`
}

// invConductResponse — ответ проведения: созданный документ МС.
type invConductResponse struct {
	OK   bool   `json:"ok"`
	Name string `json:"name"`
	URL  string `json:"url"`
	ID   string `json:"id"`
}

// invErrorResponse — ошибка JSON-эндпоинта.
type invErrorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// GoodsInventoryPage — GET /goods/inventory: выбор вида инвентаризации.
// Ошибка чтения видов — в Error шаблона, страница остаётся живой (не 500).
func (h *Handler) GoodsInventoryPage(w http.ResponseWriter, r *http.Request) {
	data := goodsInventoryData{StoreReady: h.inventoryUC.StoreConfigured()}

	types, err := h.inventoryUC.Types(r.Context())
	if err != nil {
		slog.Error(fmt.Sprintf("inventory types: %v", err))
		data.Error = "не удалось прочитать виды инвентаризации: " + err.Error()
	} else {
		data.Types = types
	}

	if err := inventoryTypesTmpl.Execute(w, data); err != nil {
		slog.Error(fmt.Sprintf("inventory types template: %v", err))
	}
}

// GoodsInventoryScanPage — GET /goods/inventory/scan?type=…: сканирование
// позиций выбранного вида. Пустой вид — редирект на выбор вида; ошибка чтения
// группы — Error в шаблоне (страница живёт, сканы всё равно отбиваются сервером).
func (h *Handler) GoodsInventoryScanPage(w http.ResponseWriter, r *http.Request) {
	inventoryType := strings.TrimSpace(r.URL.Query().Get("type"))
	if inventoryType == "" {
		http.Redirect(w, r, "/goods/inventory", http.StatusFound)

		return
	}

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

	if err := inventoryScanTmpl.Execute(w, data); err != nil {
		slog.Error(fmt.Sprintf("inventory scan template: %v", err))
	}
}

// GoodsInventoryPreview — POST /goods/inventory/preview: предпросмотр документа
// по сканам. body: {"type":"…","scans":["…"]}. 200 — все позиции группы со
// фактическим количеством; 400 — вид не задан / скан невалиден / не из группы.
func (h *Handler) GoodsInventoryPreview(w http.ResponseWriter, r *http.Request) {
	req, ok := invDecodeRequest(w, r)
	if !ok {
		return
	}

	preview, err := h.inventoryUC.Preview(r.Context(), req.Type, req.Scans)
	if err != nil {
		invWriteJSONError(w, err)

		return
	}

	invWriteJSON(w, http.StatusOK, invPreviewBody(preview))
}

// GoodsInventoryConduct — POST /goods/inventory/conduct: создать документ
// инвентаризации в МС. body: {"type":"…","scans":["…"]}. 200 — документ;
// 400 — вид/сканы (пусто, не из группы, пустая группа); прочее (в т.ч. склад МС
// не настроен и ошибки МС) — 500 с текстом в error и записью в лог.
func (h *Handler) GoodsInventoryConduct(w http.ResponseWriter, r *http.Request) {
	req, ok := invDecodeRequest(w, r)
	if !ok {
		return
	}
	if len(req.Scans) == 0 {
		invWriteJSON(w, http.StatusBadRequest, invErrorResponse{
			Error: "нет сканов — создавать документ не из чего",
		})

		return
	}

	doc, err := h.inventoryUC.Conduct(r.Context(), req.Type, req.Scans)
	if err != nil {
		invWriteJSONError(w, err)

		return
	}

	invWriteJSON(w, http.StatusOK, invConductResponse{
		OK:   true,
		Name: doc.Name,
		URL:  doc.URL,
		ID:   doc.ID,
	})
}

// invDecodeRequest читает тело предпросмотра/проведения и проверяет вид: пустой
// вид без смысла (страница обязана была его подставить) — 400.
func invDecodeRequest(w http.ResponseWriter, r *http.Request) (invPreviewRequest, bool) {
	var req invPreviewRequest
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
// и количества. allZero — ни одной ненулевой строки (нечего проводить).
func invPreviewBody(p inventory.Preview) invPreviewResponse {
	rows := make([]invPreviewRow, 0, len(p.Lines))
	allZero := true
	for _, line := range p.Lines {
		zero := line.Fact == 0
		if !zero {
			allZero = false
		}
		qty := "0"
		if !zero {
			// Факт строки показываем только у просканированных позиций: у
			// непросканированных он ноль, а «0 кг / 0 шт» читается как ошибка.
			qty = invQtyText(line)
		}
		rows = append(rows, invPreviewRow{
			Code:     line.InternalCode,
			Name:     line.Name,
			Qty:      qty,
			Price:    invRublesText(line.PriceKop),
			Scans:    line.Scans,
			Scanable: line.Scanable,
			Zero:     zero,
		})
	}

	return invPreviewResponse{
		OK:      true,
		Total:   p.Total,
		Scanned: p.Scanned,
		Scans:   p.Scans,
		AllZero: allZero,
		Rows:    rows,
	}
}

// invQtyText — факт просканированной строки строкой: весовой — в единицах
// товара (кг/г/т) с точностью грамма, штучный — целое. Непросканированные
// строки в предпросмотре показываются отдельным текстом («0»), сюда не попадают.
func invQtyText(line inventory.Line) string {
	if !line.Weighted {
		return strconv.FormatFloat(line.Fact, 'f', 0, 64) + " " + invUnitLabel(line.UOM)
	}

	decimals := inventory.WeightDecimals(line.UOM)

	return strconv.FormatFloat(line.Fact, 'f', decimals, 64) + " " + invUnitLabel(line.UOM)
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

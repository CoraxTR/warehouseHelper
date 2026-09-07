// Пакет usecase — отправка подбора в МойСклад (итерация 3). Страница заказа
// кэширует сырые ответы GET (заказ + positions + каталог) — те же запросы,
// что уже делались для рендера. Кнопка «Отправить в МС» шлёт только
// набранные сканы; usecase пересобирает раздел positions по правилам
// владельца и PUT-ит заказ сырым телом GET (полная замена строк: с id —
// обновляются, без id — создаются, отсутствующие удаляются — проверено на
// живом API). После 200 OK списывает сроки через шов stock (PickStock).
package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/stock"
)

// Кванты-заглушки недобранных активных строк (решение владельца, итер. 3):
// резерв 0 — строка не «висит» в резервах заказа при закрытии.
const (
	pieceStubQty   = 0.001 // штучная строка = «ожидает 1 единицу»
	weightStubQty  = 0.0001
	bbLayout       = "02012006" // ДДММГГГГ (срез кода ЧЗ, клиент не парсит)
	submitCacheTTL = 30 * time.Minute
	submitCacheMax = 50
	maxScanWeightG = 99999 // 5 знаков веса в коде
)

// Ошибки валидации submit (400 на клиенте).
var (
	ErrSubmitEmptyRows      = errors.New("нет набранных строк — отсканируйте хотя бы одну позицию")
	ErrSubmitBadRow         = errors.New("битая строка отправки")
	ErrSubmitNoRecords      = errors.New("нет сканов в строке")
	ErrSubmitDuplicateID    = errors.New("позиция передана дважды")
	ErrSubmitRowMissing     = errors.New("позиция не найдена в заказе — обновите страницу")
	ErrSubmitRowUnavailable = errors.New("позиция без кода склада — отправка невозможна")
	ErrSubmitBadBB          = errors.New("неверная дата срока годности (ожидается ДДММГГГГ)")
	ErrSubmitBadWeight      = errors.New("неверный вес в записи скана")
	ErrSubmitOverpick       = errors.New("отсканировано больше единиц, чем в строке заказа")
)

// StockPicker — шов в модуль остатков (stock): списание подобранных единиц
// по срокам годности после успешного обновления заказа в МС. Реализует
// *stock/usecase.StockUseCase через адаптер типов в app/di.go.
type StockPicker interface {
	PickStock(ctx context.Context, lots []stock.PickLotIn) error
}

// SubmitRequest — набранные сканы страницы заказа: покрыты только строки с
// записями (ненабранные активные строки сервер заглушает сам — B1).
type SubmitRequest struct {
	Rows []SubmitRow `json:"rows"`
}

// SubmitRow — одна логическая строка отправки: ids — все позиции МС группы
// (первая — «живая», остальные смёрженные — выбывают из заказа), records —
// сканы строки/группы (каждый скан = одна единица товара).
type SubmitRow struct {
	IDs     []string     `json:"ids"`
	Records []ScanRecord `json:"records"`
}

// ScanRecord — один засчитанный скан: вес в граммах (0 у штучных) и срок
// годности ДДММГГГГ (сырой срез кода, клиент не парсит дату).
type ScanRecord struct {
	WeightG int    `json:"w"`
	BB      string `json:"bb"`
}

// SubmitResult — итог отправки. StockWarn заполняется, когда заказ в МС
// обновлён (200), но списание сроков не прошло: повторный submit невозможен
// (заказ уже переведён), остатки списываются вручную.
type SubmitResult struct {
	StockWarn string `json:"stock_warn,omitempty"`
}

// submitCache — сырые ответы МС заказа для отправки подбора: кладутся при
// рендере детальной страницы (Detail), читаются на Submit. Кэш in-memory
// без состояния: TTL 30 минут, потолок 50 заказов (вытеснение просроченных,
// затем самого старого). Промах (рестарт/TTL) — догрузка свежими GET тем же
// путём fetchAndCache.
type submitCache struct {
	mu     sync.Mutex
	orders map[string]*submitEntry
}

// submitEntry — сырьё одного заказа: тело GET заказа (эхо для PUT), строки
// positions как пришли с expand=assortment (порядок МС!) и каталог по кодам.
type submitEntry struct {
	orderRaw json.RawMessage
	rowsRaw  []json.RawMessage
	catalog  map[string]CatalogProduct
	at       time.Time
}

func newSubmitCache() *submitCache {
	return &submitCache{orders: make(map[string]*submitEntry)}
}

func (c *submitCache) store(id string, e *submitEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.orders) >= submitCacheMax {
		now := time.Now()
		var oldestID string
		var oldestAt time.Time
		for k, v := range c.orders {
			if now.Sub(v.at) > submitCacheTTL {
				delete(c.orders, k)
				continue
			}
			if oldestID == "" || v.at.Before(oldestAt) {
				oldestID, oldestAt = k, v.at
			}
		}
		if len(c.orders) >= submitCacheMax && oldestID != "" {
			delete(c.orders, oldestID)
		}
	}

	c.orders[id] = e
}

// get возвращает свежую запись (просроченная удаляется как промах).
func (c *submitCache) get(id string) *submitEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	e := c.orders[id]
	if e == nil || time.Since(e.at) > submitCacheTTL {
		delete(c.orders, id)
		return nil
	}
	return e
}

// Submit отправляет подбор в МС: пересобирает positions из кэша отправки по
// набранным сканам, PUT-ит заказ, при 200 — списывает сроки (PickStock).
func (uc *UseCase) Submit(ctx context.Context, id string, req SubmitRequest) (SubmitResult, error) {
	done := metrics.Track(trackPkg, "Submit")
	defer done()

	id = strings.TrimSpace(id)
	if id == "" {
		return SubmitResult{}, ErrEmptyOrderID
	}

	records, err := validateSubmit(req)
	if err != nil {
		return SubmitResult{}, err
	}

	entry := uc.cache.get(id)
	if entry == nil {
		// Промах кэша (рестарт/TTL): догружаем теми же GET, что и Detail.
		if _, _, entry, err = uc.fetchAndCache(ctx, id); err != nil {
			return SubmitResult{}, err
		}
	}

	out, lots, err := uc.buildSubmitPositions(req, records, entry)
	if err != nil {
		return SubmitResult{}, err
	}

	body, err := buildPutBody(entry.orderRaw, out)
	if err != nil {
		return SubmitResult{}, err
	}

	if err := uc.ms.UpdateCustomerOrder(ctx, id, body); err != nil {
		return SubmitResult{}, fmt.Errorf("update order %s: %w", id, err)
	}

	res := SubmitResult{}
	if uc.picker == nil {
		// Без шва (не подключён) заказ уже обновлён — остатки не списаны.
		slog.Error(fmt.Sprintf("msorders: stock picker not wired for order %s", id))
		res.StockWarn = "заказ обновлён в МС, но остатки по срокам не списаны — нужен ручной пересчёт сроков"
		return res, nil
	}
	if err := uc.picker.PickStock(ctx, lots); err != nil {
		slog.Error(fmt.Sprintf("msorders: pick stock after order %s updated: %v", id, err))
		res.StockWarn = "заказ обновлён в МС, но остатки по срокам не списаны — нужен ручной пересчёт сроков"
	}

	return res, nil
}

// validateSubmit разбирает и проверяет вход: строки, ids без дублей, записи
// сканов (дата ДДММГГГГ → UTC-полночь, вес 0..99999 г).
func validateSubmit(req SubmitRequest) (map[int][]parsedScan, error) {
	if len(req.Rows) == 0 {
		return nil, ErrSubmitEmptyRows
	}

	records := make(map[int][]parsedScan, len(req.Rows))
	seen := make(map[string]struct{})
	for i := range req.Rows {
		r := &req.Rows[i]
		if len(r.IDs) == 0 {
			return nil, fmt.Errorf("строка %d: %w", i+1, ErrSubmitBadRow)
		}
		if len(r.Records) == 0 {
			return nil, fmt.Errorf("строка %d: %w", i+1, ErrSubmitNoRecords)
		}

		for _, rawID := range r.IDs {
			id := strings.TrimSpace(rawID)
			if id == "" {
				return nil, fmt.Errorf("строка %d: %w", i+1, ErrSubmitBadRow)
			}
			if _, dup := seen[id]; dup {
				return nil, fmt.Errorf("%w: %s", ErrSubmitDuplicateID, id)
			}
			seen[id] = struct{}{}
		}

		parsed := make([]parsedScan, 0, len(r.Records))
		for _, rec := range r.Records {
			bb, err := parseBB(rec.BB)
			if err != nil {
				return nil, fmt.Errorf("строка %d: %w: %q", i+1, ErrSubmitBadBB, rec.BB)
			}
			if rec.WeightG < 0 || rec.WeightG > maxScanWeightG {
				return nil, fmt.Errorf("строка %d: %w: %d", i+1, ErrSubmitBadWeight, rec.WeightG)
			}
			parsed = append(parsed, parsedScan{weightG: rec.WeightG, bb: bb})
		}
		records[i] = parsed
	}

	return records, nil
}

// parsedScan — разобранная запись скана (вес г + срок UTC-полночь).
type parsedScan struct {
	weightG int
	bb      time.Time
}

// parseBB разбирает дату срока ДДММГГГГ (срез кода ЧЗ) в UTC-полночь.
func parseBB(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("пустая дата")
	}
	t, err := time.Parse(bbLayout, s)
	if err != nil {
		return time.Time{}, err
	}
	return utcDay(t), nil
}

// utcDay приводит время к UTC-полуночи (единый ключ лота остатков).
func utcDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// coveredRef — разобранная покрытая строка отправки (живая + смёрженные).
type coveredRef struct {
	live      *submitRowMeta
	merged    []*submitRowMeta
	records   []parsedScan
	productID string
	weighted  bool
}

// submitRowMeta — строка positions из кэша: сырьё в map + разобранные поля.
type submitRowMeta struct {
	idx       int
	m         map[string]any
	id        string
	code      string
	productID string
	weighted  bool
	hasCode   bool
	quantity  float64
	reserve   float64
}

// buildSubmitPositions собирает итоговый список positions (порядок — как в
// кэше/МС) и лоты списания. Правила владельца (итер. 3): живая покрытая
// весовая → qty = reserve = Σ вес_г / 1000 кг; штучная покрытая → qty =
// reserve = число сканов (+ заглушки 0,001 на недобор); ненабранные активные:
// весовые → 0,0001, штучные → разбиение на N заглушек 0,001; смёрженные
// хвосты и пассивные строки — исключаются/остаются как есть.
func (uc *UseCase) buildSubmitPositions(req SubmitRequest, records map[int][]parsedScan, entry *submitEntry) ([]any, []stock.PickLotIn, error) {
	metas := make([]*submitRowMeta, 0, len(entry.rowsRaw))
	metaByID := make(map[string]*submitRowMeta, len(entry.rowsRaw))
	for i, raw := range entry.rowsRaw {
		m, err := rowMap(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("parse cached row %d: %w", i, err)
		}
		meta := metaFromRow(i, m, entry.catalog)
		metas = append(metas, meta)
		if meta.id != "" {
			metaByID[meta.id] = meta
		}
	}

	// Покрытые: живая строка (ids[0]) + хвосты группы.
	liveByID := make(map[string]*coveredRef, len(req.Rows))
	mergedByID := make(map[string]struct{})
	for i := range req.Rows {
		r := &req.Rows[i]
		live, ok := metaByID[strings.TrimSpace(r.IDs[0])]
		if !ok {
			return nil, nil, fmt.Errorf("%w: %s", ErrSubmitRowMissing, r.IDs[0])
		}
		if !live.hasCode {
			return nil, nil, fmt.Errorf("%w: %s", ErrSubmitRowUnavailable, live.id)
		}
		ref := &coveredRef{
			live:      live,
			records:   records[i],
			productID: live.productID,
			weighted:  live.weighted,
		}
		for _, rawID := range r.IDs[1:] {
			id := strings.TrimSpace(rawID)
			tail, ok := metaByID[id]
			if !ok {
				return nil, nil, fmt.Errorf("%w: %s", ErrSubmitRowMissing, id)
			}
			ref.merged = append(ref.merged, tail)
			mergedByID[id] = struct{}{}
		}
		liveByID[live.id] = ref
	}

	out := make([]any, 0, len(metas))
	var lots []stock.PickLotIn
	for _, meta := range metas {
		if ref, covered := liveByID[meta.id]; covered {
			rows, lot, err := applyCovered(ref)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, rows...)
			if lot != nil {
				lots = append(lots, lot...)
			}
			continue
		}
		if _, merged := mergedByID[meta.id]; merged {
			continue // смёрженный хвост выбывает
		}

		if !meta.hasCode || meta.reserve > 0 {
			// пассивная (без кода) / переподбор — как есть
			out = append(out, json.RawMessage(entry.rowsRaw[meta.idx]))
			continue
		}

		// Ненабранная активная строка — заглушка по типу товара (B1).
		if meta.weighted {
			setQtyReserve(meta.m, weightStubQty, 0)
			out = append(out, meta.m)
			continue
		}
		out = append(out, stubPieceRow(meta)...)
	}

	return out, lots, nil
}

// applyCovered применяет сканы к живой строке: весовые — суммарный вес,
// штучные — число единиц + составные заглушки на недобор; собирает лоты
// списания (каждый скан = одна единица своего срока).
func applyCovered(ref *coveredRef) ([]any, []stock.PickLotIn, error) {
	if ref.weighted {
		var sum int64
		for _, r := range ref.records {
			sum += int64(r.weightG)
		}
		if sum <= 0 {
			return nil, nil, fmt.Errorf("%w: весовая запись без веса", ErrSubmitBadWeight)
		}
		qty := float64(sum) / 1000
		setQtyReserve(ref.live.m, qty, qty)
		return []any{ref.live.m}, pickLots(ref), nil
	}

	k := len(ref.records)
	total := ref.live.quantity
	for _, tail := range ref.merged {
		total += tail.quantity
	}
	units := max(1, int(math.Round(total))) // ожидаемых единиц в группе
	if k > units {
		return nil, nil, fmt.Errorf("%w: %s (в строке %d, отсканировано %d)", ErrSubmitOverpick, ref.live.code, units, k)
	}

	setQtyReserve(ref.live.m, float64(k), float64(k))
	out := []any{ref.live.m}
	if k < units {
		for i := 0; i < units-k; i++ {
			out = append(out, createStub(ref.live.m, pieceStubQty))
		}
	}
	return out, pickLots(ref), nil
}

// stubPieceRow разбивает ненабранную активную штучную строку на N заглушек
// 0,001: исходная строка становится заглушкой + (N−1) новых без id.
func stubPieceRow(meta *submitRowMeta) []any {
	units := max(1, int(math.Round(meta.quantity)))
	setQtyReserve(meta.m, pieceStubQty, 0)
	out := []any{meta.m}
	for i := 1; i < units; i++ {
		out = append(out, createStub(meta.m, pieceStubQty))
	}
	return out
}

// pickLots собирает списание по записям покрытой строки: каждая запись —
// одна единица товара своего срока (группировка (товар, срок) снаружи не
// нужна: PickStock дедуплицирует сам).
func pickLots(ref *coveredRef) []stock.PickLotIn {
	lots := make([]stock.PickLotIn, 0, len(ref.records))
	for _, r := range ref.records {
		lots = append(lots, stock.PickLotIn{
			ProductID:  ref.productID,
			BestBefore: r.bb,
			Qty:        1,
		})
	}
	return lots
}

// rowMap разбирает сырую строку positions в map (для точечных правок полей).
func rowMap(raw json.RawMessage) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// metaFromRow достаёт из строки id, код и поля для сборки positions.
func metaFromRow(idx int, m map[string]any, catalog map[string]CatalogProduct) *submitRowMeta {
	meta := &submitRowMeta{idx: idx, m: m}
	meta.id, _ = m["id"].(string)
	if meta.id == "" {
		return meta // строка без id (в кэше так не бывает) — не активна
	}
	meta.quantity, _ = m["quantity"].(float64)
	meta.reserve = floatField(m["reserve"])

	if am, ok := m["assortment"].(map[string]any); ok {
		meta.code = strings.TrimSpace(asString(am["code"]))
	}
	if meta.code != "" {
		if p, ok := catalog[meta.code]; ok {
			meta.hasCode = true
			meta.productID = p.ProductID
			meta.weighted = p.Weighted
		}
	}
	return meta
}

// floatField достаёт число из JSON-значения (null/отсутствие → 0).
func floatField(v any) float64 {
	f, _ := v.(float64)
	return f
}

// asString приводит JSON-значение к строке (не-строка → пусто).
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// setQtyReserve правит quantity/reserve в сырой строке.
func setQtyReserve(m map[string]any, qty, reserve float64) {
	m["quantity"] = qty
	m["reserve"] = reserve
}

// createStub собирает строку для создания (без id/meta/accountId/shipped —
// иначе МС отвечает 412/3013): только поля создания + заглушка количества.
func createStub(base map[string]any, qty float64) map[string]any {
	out := make(map[string]any, 6)
	for _, k := range []string{"assortment", "price", "discount", "vat", "vatEnabled"} {
		if v, ok := base[k]; ok {
			out[k] = v
		}
	}
	setQtyReserve(out, qty, 0)
	return out
}

// buildPutBody собирает тело PUT: полный ответ GET заказа с заменённым
// разделом positions (эхо; служебные поля МС можно не чистить — проверено).
func buildPutBody(orderRaw json.RawMessage, positions []any) (json.RawMessage, error) {
	var body map[string]any
	if err := json.Unmarshal(orderRaw, &body); err != nil {
		return nil, fmt.Errorf("unmarshal order raw: %w", err)
	}
	body["positions"] = positions

	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal order body: %w", err)
	}
	return out, nil
}

// sortLots упорядочивает лоты списания (стабильный порядок для тестов и
// логов): по товару, затем по сроку.
func sortLots(lots []stock.PickLotIn) {
	sort.Slice(lots, func(i, j int) bool {
		if lots[i].ProductID != lots[j].ProductID {
			return lots[i].ProductID < lots[j].ProductID
		}
		return lots[i].BestBefore.Before(lots[j].BestBefore)
	})
}

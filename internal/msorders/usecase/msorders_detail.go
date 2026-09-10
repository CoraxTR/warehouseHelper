// Пакет usecase — детальная страница заказа МС для подбора (итерация 2):
// шапка заказа + позиции. Позиции резолвятся через каталог склада по
// внутреннему коду (assortment.code из expand), упорядочиваются так, чтобы
// одинаковые (internal_code && цена) шли подряд и получили номер группы
// склейки — дальше группирует/соединяет клиент. Своей схемы БД нет.
package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/msclient/client"
)

// ErrEmptyOrderID — пустой id заказа (защита; в роуте id приходит из пути).
var ErrEmptyOrderID = errors.New("пустой id заказа")

// OrderDetailClient — контракт детальной страницы заказа, реализуется
// *client.MSAPIClient.
type OrderDetailClient interface {
	// FetchOrderByID — заказ по id: поля шапки + meta-ссылки agent/positions.
	// Сырое тело GET возвращается вторым значением: тот самый JSON, который
	// уходит эхом в PUT при отправке подбора (msorders Submit).
	FetchOrderByID(ctx context.Context, id string) (*client.MSOrder, json.RawMessage, error)
	// FetchOrderPositionsByHREF — позиции заказа с expand=assortment
	// (имя и внутренний код товара приезжают в строке). Второе значение —
	// сырые JSON-строки rows (тот же порядок): эхо для PUT при отправке.
	FetchOrderPositionsByHREF(ctx context.Context, o *client.MSOrder) ([]client.MSPosition, []json.RawMessage, error)
	// UpdateCustomerOrder — PUT entity/customerorder/{id} сырым телом
	// (полный ответ GET заказа с отредактированным positions). Не-2xx — MSAPIError.
	UpdateCustomerOrder(ctx context.Context, id string, body json.RawMessage) error
}

// OrderClient — полный контракт модуля к клиенту МС: поиск заказа
// (страница «Подобрать») и детальная страница.
type OrderClient interface {
	OrderSearchClient
	OrderDetailClient
}

// CatalogProduct — товар каталога склада, найденный по внутреннему коду.
type CatalogProduct struct {
	ProductID    string // products.id (товар склада, ключ остатков)
	InternalCode string
	Weighted     bool // весовой (учёт в кг) или штучный (в штуках)
}

// CatalogReader — каталог склада по внутренним кодам: подтверждает, что код —
// складской товар, и отдаёт тип учёта. Реализует *postgres.PGClient через
// адаптер типов в app/di.go.
type CatalogReader interface {
	LoadCatalogProductsByCodes(ctx context.Context, codes []string) (map[string]CatalogProduct, error)
	// LoadProductAverageWeights — средние веса штучных товаров (кг) по их
	// products.id: нужны для общего веса заказа (весовые уже в кг). Чтение
	// вторичное: ошибка не должна ронять страницу подбора.
	LoadProductAverageWeights(ctx context.Context, productIDs []string) (map[string]float64, error)
}

// Order — данные страницы заказа: шапка + позиции.
type Order struct {
	ID             string
	Name           string // номер заказа
	AgentName      string
	AgentPhone     string
	Address        string // адрес доставки (addInfo полного адреса; пуст — shipmentAddress)
	AddressComment string // доп. указания к адресу (comment полного адреса)
	DeliveryDate   string // плановая дата отгрузки, ДД.ММ.ГГГГ
	Comment        string // описание заказа
	Rows           []OrderItem
	// TotalSumText — общая сумма заказа (Σ сумм строк), «33480,00».
	TotalSumText string
	// TotalWeightText — общий вес заказа (Σ кг весовых + Σ шт×средний вес),
	// «12,345 кг». Позиции без среднего веса в вес не входят.
	TotalWeightText string
	// WeightMissingPositions — сколько штучных позиций не дали вклад в вес
	// (нет среднего веса в каталоге или товар вне каталога).
	WeightMissingPositions int
}

// OrderItem — позиция заказа, готовая к показу: количество и резерв уже
// отформатированы, группа склейки проставлена. Числовые поля (Qty/Price/
// Reserve) остаются для data-атрибутов клиента.
type OrderItem struct {
	ID          string  // id позиции в МС (якорь)
	Name        string  // товар (assortment.name)
	Code        string  // внутренний артикул (assortment.code)
	HasCode     bool    // код найден в каталоге склада — строка может быть активной
	Weighted    bool    // весовой товар (из каталога)
	Qty         float64 // количество в заказе (весовые — кг, штучные — шт)
	QtyText     string  // «0,367 кг» / «3 шт»
	Price       float64 // цена, копейки за единицу (ключ группы склейки)
	PriceText   string  // «2790,00»
	Reserve     float64 // зарезервировано (те же единицы, что Qty)
	ReserveText string
	Group       int // номер группы склейки (0 — вне группы); группы идут подряд
	GroupSize   int // строк в группе (1 — кнопка «Объединить» не нужна)
	// Active — строка подбирается сканами: товар в каталоге; штучная при
	// reserve < qty (подбор с нуля при 0, добор при частичном резерве),
	// весовая только при reserve == 0 (одна строка = один кусок, источник
	// истины — скан фактического веса).
	// CanRepick — «Переподобрать»: весовая с любым резервом (перескан
	// фактического веса после правки менеджером) или штучная, полностью
	// находящаяся в резерве (reserve >= qty).
	Active    bool
	CanRepick bool
	// Sum — сумма строки, копейки: Price × Qty (весовые — кг), округлено
	// до целых копеек moneyInt. SumText — «1023,93».
	Sum     float64
	SumText string
	// AverageWeightKg — средний вес штуки из каталога (кг); nil — не задан
	// (позиция в общий вес не входит, счётчик WeightMissingPositions).
	AverageWeightKg *float64
	// GroupQty/GroupQtyText — подытог группы склейки (Σ Qty), показывается
	// мелким шрифтом под последней строкой группы (GroupLast).
	GroupQty     float64
	GroupQtyText string
	GroupLast    bool
	// productID — products.id (для чтения среднего веса); в шаблон не выводится.
	productID string
}

// Detail собирает страницу заказа: шапка, позиции с резолвом каталога,
// сортировка и группы склейки. Ошибка клиента/каталога роняет страницу
// целиком (позиции без резолва кодов показать нельзя — строки молча стали
// бы пассивными). Ошибка хопа за контрагентом НЕ роняет: имя/телефон
// остаются пустыми (вторичные данные, клиент уже залогировал).
// Побочно кладёт сырые ответы МС в кэш отправки (те же GET, что и так
// делались для рендера) — на submit новые запросы не нужны.
func (uc *UseCase) Detail(ctx context.Context, id string) (*Order, error) {
	done := metrics.Track(trackPkg, "Detail")
	defer done()

	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrEmptyOrderID
	}

	order, entry, err := uc.fetchAndCache(ctx, id)
	if err != nil {
		return nil, err
	}

	rows := buildItems(entry.positions, entry.catalog)

	// Средний вес штучных — отдельное вторичное чтение; сумма и общий вес
	// считаются поверх строк (копейки — moneyInt, см. applyTotals).
	weights := uc.loadAverageWeights(ctx, rows)
	totalSum, totalWeight, missing := applyTotals(rows, weights)

	agentName, agentPhone, _ := uc.ms.FetchOrderAgentByHREF(ctx, order)

	return &Order{
		ID:             order.ID,
		Name:           order.Name,
		AgentName:      orDash(strings.TrimSpace(agentName)),
		AgentPhone:     orDash(strings.TrimSpace(agentPhone)),
		Address:        orderAddress(order),
		AddressComment: strings.TrimSpace(order.ShipmentAddressFull.Comment),
		DeliveryDate:   shortDate(order.DeliveryPlannedMoment),
		Comment:        orDash(strings.TrimSpace(order.Description)),
		Rows:           rows,

		TotalSumText:           moneyText(totalSum),
		TotalWeightText:        qtyWeightText(totalWeight),
		WeightMissingPositions: missing,
	}, nil
}

// fetchAndCache загружает заказ и позиции одним путём для Detail и Submit
// (догрузка при промахе кэша отправки): типизированные данные для страницы
// + сырьё (заказ, строки positions, каталог) в кэш отправки.
func (uc *UseCase) fetchAndCache(ctx context.Context, id string) (*client.MSOrder, *submitEntry, error) {
	order, orderRaw, err := uc.ms.FetchOrderByID(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch order %s: %w", id, err)
	}

	positions, rowsRaw, err := uc.ms.FetchOrderPositionsByHREF(ctx, order)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch positions %s: %w", id, err)
	}

	catalog, err := uc.loadCatalog(ctx, positions)
	if err != nil {
		return nil, nil, err
	}

	entry := &submitEntry{
		orderRaw:  orderRaw,
		rowsRaw:   rowsRaw,
		catalog:   catalog,
		positions: positions,
		at:        time.Now(),
	}
	uc.cache.store(id, entry)

	return order, entry, nil
}

// buildItems резолвит позиции через каталог, сортирует и проставляет группы.
func buildItems(positions []client.MSPosition, catalog map[string]CatalogProduct) []OrderItem {
	items := make([]OrderItem, 0, len(positions))
	for _, p := range positions {
		code := strings.TrimSpace(p.Assortment.Code)
		product, inCatalog := catalog[code]
		weighted := inCatalog && product.Weighted

		qtyText := qtyPiecesText(p.Quantity)
		reserveText := qtyPiecesText(p.Reserve)
		if weighted {
			qtyText = qtyWeightText(p.Quantity)
			reserveText = qtyWeightText(p.Reserve)
		}

		items = append(items, OrderItem{
			ID:          p.ID,
			Name:        orDash(strings.TrimSpace(p.Assortment.Name)),
			Code:        code,
			HasCode:     inCatalog,
			Weighted:    weighted,
			Qty:         p.Quantity,
			QtyText:     qtyText,
			Price:       p.Price,
			PriceText:   moneyText(p.Price),
			Reserve:     p.Reserve,
			ReserveText: reserveText,
			Active:      inCatalog && (p.Reserve == 0 || (!weighted && p.Reserve < p.Quantity)),
			CanRepick:   inCatalog && p.Reserve > 0 && (weighted || p.Reserve >= p.Quantity),
			productID:   product.ProductID,
		})
	}

	sortItems(items)
	assignGroups(items)
	assignGroupTotals(items)

	return items
}

// loadCatalog запрашивает каталог по уникальным кодам позиций. Пусто, когда
// ни у одной позиции нет кода — каталог не дёргаем (nil-карта безопасна).
func (uc *UseCase) loadCatalog(ctx context.Context, positions []client.MSPosition) (map[string]CatalogProduct, error) {
	codes := make([]string, 0, len(positions))
	seen := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		code := strings.TrimSpace(p.Assortment.Code)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}

	if len(codes) == 0 {
		return map[string]CatalogProduct{}, nil
	}

	catalog, err := uc.catalog.LoadCatalogProductsByCodes(ctx, codes)
	if err != nil {
		return nil, fmt.Errorf("load catalog for order: %w", err)
	}

	return catalog, nil
}

// loadAverageWeights читает средние веса штучных товаров каталога (кг) по их
// products.id. Весовые позиции вес уже несут в Qty — их id не запрашиваются.
// Чтение вторичное: ошибка/пропуски НЕ роняют страницу — пустая карта, вес
// позиции не считается (applyTotals учтёт её в WeightMissingPositions).
func (uc *UseCase) loadAverageWeights(ctx context.Context, items []OrderItem) map[string]float64 {
	ids := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		id := items[i].productID
		if items[i].Weighted || id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	if len(ids) == 0 {
		return map[string]float64{}
	}

	weights, err := uc.catalog.LoadProductAverageWeights(ctx, ids)
	if err != nil {
		return map[string]float64{}
	}
	if weights == nil {
		return map[string]float64{}
	}

	return weights
}

// applyTotals считает суммы строк (копейки, округление moneyInt), общий вес
// заказа и число штучных позиций без среднего веса. Весовые дают Qty кг,
// штучные — Qty×AverageWeightKg; позиция без среднего веса в вес не входит.
func applyTotals(items []OrderItem, weights map[string]float64) (totalSum, totalWeight float64, missing int) {
	for i := range items {
		it := &items[i]

		sum := float64(moneyInt(it.Price * it.Qty))
		it.Sum = sum
		it.SumText = moneyText(sum)
		totalSum += sum

		if it.Weighted {
			totalWeight += it.Qty
			continue
		}

		avg, ok := weights[it.productID]
		if it.productID == "" || !ok || avg <= 0 {
			missing++
			continue
		}
		weight := avg
		it.AverageWeightKg = &weight
		totalWeight += it.Qty * avg
	}

	return totalSum, totalWeight, missing
}

// assignGroupTotals проставляет подытоги групп склейки: суммарное количество
// (текст по типу учёта группы) и признак последней строки группы — под ней
// рендерится строка «итого». Строки вне групп — без подытога.
func assignGroupTotals(items []OrderItem) {
	qty := make(map[int]float64, len(items))
	for i := range items {
		if items[i].Group != 0 {
			qty[items[i].Group] += items[i].Qty
		}
	}

	for i := range items {
		g := items[i].Group
		if g == 0 {
			continue
		}
		items[i].GroupQty = qty[g]
		items[i].GroupQtyText = groupQtyText(items[i])
		items[i].GroupLast = i == len(items)-1 || items[i+1].Group != g
	}
}

// groupQtyText форматирует суммарное количество группы для строки-подытога
// («4,367 кг» — весовые, «5 шт» — штучные). Тип учёта берётся из самой строки:
// флаг управления в параметрах — control coupling (revive flag-parameter).
func groupQtyText(it OrderItem) string {
	if it.Weighted {
		return qtyWeightText(it.GroupQty)
	}

	return qtyPiecesText(it.GroupQty)
}

// sortItems упорядочивает позиции: строки с внутренним кодом — группами по
// (internal_code, цена), строки без кода — в конце в исходном порядке
// (стабильная сортировка).
func sortItems(items []OrderItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.HasCode != b.HasCode {
			return a.HasCode
		}
		if !a.HasCode {
			return false
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return moneyInt(a.Price) < moneyInt(b.Price)
	})
}

// assignGroups нумерует группы склейки: идущие подряд строки с одинаковыми
// internal_code и ценой получают один номер; размер группы — для решения
// о кнопке «Объединить» (>=2). Строки без кода — вне групп.
func assignGroups(items []OrderItem) {
	sizes := make(map[int]int, len(items))
	group := 0
	for i := range items {
		if !items[i].HasCode {
			continue
		}
		if i > 0 && items[i-1].HasCode &&
			items[i].Code == items[i-1].Code &&
			moneyInt(items[i].Price) == moneyInt(items[i-1].Price) {
			items[i].Group = items[i-1].Group
		} else {
			group++
			items[i].Group = group
		}
		sizes[items[i].Group]++
	}

	for i := range items {
		items[i].GroupSize = sizes[items[i].Group]
	}
}

// orderAddress собирает адрес доставки: полный адрес (addInfo) приоритетнее
// строки shipmentAddress; пусто — вернётся «—» на клиенте.
func orderAddress(o *client.MSOrder) string {
	if addr := strings.TrimSpace(o.ShipmentAddressFull.AddInfo); addr != "" {
		return addr
	}
	return strings.TrimSpace(o.ShipmentAddress)
}

// moneyInt приводит копейки к целым (ключ сравнения цен без плавающих хвостов).
func moneyInt(copeck float64) int64 {
	return int64(copeck + 0.5)
}

// qtyWeightText форматирует килограммы с точностью до 3 знаков
// («0,367 кг», хвостовые нули обрезаются).
func qtyWeightText(q float64) string {
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(q, 'f', 3, 64), "0"), ".")
	return strings.Replace(s, ".", ",", 1) + " кг"
}

// qtyPiecesText форматирует штучное количество («3 шт»).
func qtyPiecesText(q float64) string {
	return strconv.FormatFloat(q, 'f', 0, 64) + " шт"
}

// moneyText переводит копейки МС в рубли с двумя знаками («2790,00»).
func moneyText(copeck float64) string {
	s := strconv.FormatFloat(copeck/100, 'f', 2, 64)
	return strings.Replace(s, ".", ",", 1)
}

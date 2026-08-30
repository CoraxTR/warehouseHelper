// Пакет usecase — сценарии модуля приёмки: кеш поставщика (правила +
// маппинг кодов), резолв штрих-кодов (внутренние 29/33 и внешние по
// правилам), сохранение приёмки (остатки через AcceptStock, веса).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/decoderules"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/stock"
)

// ReceiveRepository — контракт чтения данных приёмки.
type ReceiveRepository interface {
	// LoadSupplierBarcodes — связки «внешний код → товар» поставщика.
	LoadSupplierBarcodes(ctx context.Context, supplierID string) ([]receiving.BarcodeRef, error)
	// GetSupplier — поставщик (правила вычитки).
	GetSupplier(ctx context.Context, id string) (*domain.Supplier, error)
	// LoadCatalogProductsByCodes — товары каталога по внутренним кодам.
	LoadCatalogProductsByCodes(ctx context.Context, codes []string) (map[string]receiving.ProductRef, error)
	// LoadCatalogAllRefs — все товары каталога по внутренним кодам
	// (для кеша страницы приёмки: внутренние коды 29/33 распознаются сразу).
	LoadCatalogAllRefs(ctx context.Context) ([]receiving.ProductRef, error)
	// InsertReceivedWeights — запись весов принятых единиц (граммы).
	InsertReceivedWeights(ctx context.Context, rows []receiving.WeightRow) error
	// TrimReceivedWeights — оставить последние keep весов товара (FIFO по id).
	TrimReceivedWeights(ctx context.Context, productID string, keep int) error
}

// StockAccepter — адаптер модуля сроков: принятые партии добавляются к
// остаткам (qty +=), реализует *sucase.StockUseCase.
type StockAccepter interface {
	AcceptStock(ctx context.Context, lots []stock.LotIn) error
}

// ReceivingUseCase — сценарии приёмки.
type ReceivingUseCase struct {
	repo  ReceiveRepository
	stock StockAccepter
}

// NewReceivingUseCase создаёт сценарий с хранилищем и адаптером сроков.
func NewReceivingUseCase(repo ReceiveRepository, stock StockAccepter) *ReceivingUseCase {
	return &ReceivingUseCase{repo: repo, stock: stock}
}

// keepWeights — сколько последних весов единицы хранить на товар (FIFO);
// настройка приложения, зеркалит комментарий схемы received_weights.
const keepWeights = 100

// GetCache собирает кеш приёмки поставщика: правила вычитки (куски и
// коробки), маппинг внешних кодов, позиции поставщика для ручного выбора.
func (uc *ReceivingUseCase) GetCache(ctx context.Context, supplierID string) (*receiving.Cache, error) {
	supplierID = strings.TrimSpace(supplierID)
	if supplierID == "" {
		return nil, errors.New("не выбран поставщик")
	}

	s, err := uc.repo.GetSupplier(ctx, supplierID)
	if err != nil {
		return nil, fmt.Errorf("получить поставщика: %w", err)
	}

	itemRules, err := parseRules(s.DecodeRules, decoderules.ParseItem)
	if err != nil {
		return nil, fmt.Errorf("правила вычитки: %w", err)
	}
	boxRules, err := parseRules(s.BoxDecodeRules, decoderules.ParseBox)
	if err != nil {
		return nil, fmt.Errorf("правила коробок: %w", err)
	}

	barcodes, err := uc.repo.LoadSupplierBarcodes(ctx, supplierID)
	if err != nil {
		return nil, fmt.Errorf("связки кодов поставщика: %w", err)
	}

	cache := &receiving.Cache{
		ItemRules:  itemRules,
		BoxRules:   boxRules,
		ByExternal: make(map[string]receiving.BarcodeRef, len(barcodes)),
		ByCode:     make(map[string]receiving.ProductRef),
	}
	seen := make(map[string]struct{}, len(barcodes))
	for _, b := range barcodes {
		cache.ByExternal[b.ExternalCode] = b
		if _, ok := seen[b.ProductID]; ok {
			continue
		}
		seen[b.ProductID] = struct{}{}
		cache.Products = append(cache.Products, receiving.ProductRef{
			ProductID:    b.ProductID,
			InternalCode: b.InternalCode,
			Name:         b.ProductName,
			Weighted:     b.Weighted,
		})
	}
	sort.Slice(cache.Products, func(i, j int) bool {
		return strings.ToLower(cache.Products[i].Name) < strings.ToLower(cache.Products[j].Name)
	})

	return cache, nil
}

// AddCatalogCodes заполняет кеш приёмки картой «internal_code → товар» по
// всему каталогу — внутренние штрих-коды (29/33) распознаются «на лету»
// даже для товаров, не заведённых у поставщика. Вызывается только при
// отдаче кеша странице; Save резолвит внутренние коды лениво.
func (uc *ReceivingUseCase) AddCatalogCodes(ctx context.Context, cache *receiving.Cache) error {
	refs, err := uc.repo.LoadCatalogAllRefs(ctx)
	if err != nil {
		return fmt.Errorf("каталог для кеша: %w", err)
	}
	if cache.ByCode == nil {
		cache.ByCode = make(map[string]receiving.ProductRef, len(refs))
	}
	for _, r := range refs {
		cache.ByCode[r.InternalCode] = r
	}
	return nil
}

// parseRules разбирает правила поставщика в кеш приёмки.
func parseRules(rules []string, parse func(string) (decoderules.Rule, error)) ([]receiving.DecodeRule, error) {
	out := make([]receiving.DecodeRule, 0, len(rules))
	for _, r := range rules {
		parsed, err := parse(r)
		if err != nil {
			return nil, err
		}
		dr := receiving.DecodeRule{Length: parsed.Length, Fields: make([]receiving.RuleField, len(parsed.Fields))}
		for i, f := range parsed.Fields {
			dr.Fields[i] = receiving.RuleField{Pos: f.Pos, Len: f.Len}
		}
		out = append(out, dr)
	}
	return out, nil
}

// Resolve распознаёт скан: внутренний формат (29/33) или правило поставщика.
// Ручные поля (товар, вес, даты) применяются, если поле не вычитывается.
func (uc *ReceivingUseCase) Resolve(ctx context.Context, cache *receiving.Cache, e receiving.ScanEntry) (*receiving.DecodedScan, error) {
	raw := strings.TrimSpace(e.Raw)
	if raw == "" {
		return nil, errors.New("пустой штрих-код")
	}

	// Внутренний формат склада: кусок 29 / коробка 33.
	if len(raw) == 29 || len(raw) == 33 {
		return uc.resolveInternal(ctx, cache, raw, e)
	}

	// Внешние коды поставщика: сначала коробки (по длине), затем куски.
	for _, rule := range cache.BoxRules {
		if rule.Length != len(raw) {
			continue
		}
		return uc.resolveByRule(cache, rule, raw, e, receiving.KindBox)
	}
	for _, rule := range cache.ItemRules {
		if rule.Length != len(raw) {
			continue
		}
		return uc.resolveByRule(cache, rule, raw, e, receiving.KindItem)
	}

	return nil, receiving.ErrScanUnknown
}

// resolveInternal разбирает внутренний штрих-код (29 — кусок, 33 — коробка).
func (uc *ReceivingUseCase) resolveInternal(ctx context.Context, cache *receiving.Cache, raw string, e receiving.ScanEntry) (*receiving.DecodedScan, error) {
	code, err := innercode.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("внутренний штрих-код: %w", err)
	}

	ref, ok := cache.ByCode[code.InternalCode]
	if !ok {
		// Лениво подгружаем каталог по коду (товаров много, тянуть все нельзя).
		found, err := uc.repo.LoadCatalogProductsByCodes(ctx, []string{code.InternalCode})
		if err != nil {
			return nil, fmt.Errorf("товар по коду %s: %w", code.InternalCode, err)
		}
		if f, ok := found[code.InternalCode]; ok {
			ref = f
			cache.ByCode[code.InternalCode] = f
		} else {
			return nil, fmt.Errorf("товар с внутренним кодом %s не найден в каталоге", code.InternalCode)
		}
	}

	scan := &receiving.DecodedScan{
		Kind:         scanKindOf(code.Kind),
		Raw:          raw,
		IsInternal:   true,
		ProductID:    ref.ProductID,
		InternalCode: ref.InternalCode,
		ProductName:  ref.Name,
	}
	if code.WeightG > 0 {
		w := int64(code.WeightG)
		scan.WeightG = &w
	}
	if !code.ProdDate.IsZero() {
		p := code.ProdDate
		scan.ProducedOn = &p
	}
	if !code.ExpDate.IsZero() {
		b := code.ExpDate
		scan.BestBefore = &b
	}
	if scan.Kind == receiving.KindBox {
		q := int64(code.Qty)
		w := int64(code.WeightG)
		scan.Qty = int64(code.Qty)
		scan.DeclaredQty = &q
		scan.DeclaredWeightG = &w
	} else {
		scan.Qty = 1
	}

	return scan, nil
}

// scanKindOf маппит вид внутреннего кода на вид скана приёмки.
func scanKindOf(k innercode.Kind) receiving.ScanKind {
	if k == innercode.KindBox {
		return receiving.KindBox
	}
	return receiving.KindItem
}

// resolveByRule вычитывает скан по правилу поставщика.
func (uc *ReceivingUseCase) resolveByRule(cache *receiving.Cache, rule receiving.DecodeRule, raw string, e receiving.ScanEntry, kind receiving.ScanKind) (*receiving.DecodedScan, error) {
	scan := &receiving.DecodedScan{Kind: kind, Raw: raw}

	// Код товара: правило или ручной выбор позиции.
	code, ok := sliceRule(rule, raw, 0)
	var ref receiving.BarcodeRef
	refOK := false
	if ok {
		ref, refOK = cache.ByExternal[code]
	}
	if refOK {
		scan.ProductID = ref.ProductID
		scan.InternalCode = ref.InternalCode
		scan.ProductName = ref.ProductName
	} else if e.ManualProductID != "" {
		scan.ProductID = e.ManualProductID
	} else {
		if ok {
			return nil, fmt.Errorf("внешний код %q не заведён у поставщика — добавьте его на карточке поставщика", code)
		}
		return nil, fmt.Errorf("в правиле не вычитывается код товара — выберите позицию вручную")
	}

	// Вес (граммы): правило или ручной ввод.
	if w, ok := sliceRule(rule, raw, 1); ok {
		g, err := strconv.ParseInt(w, 10, 64)
		if err != nil || g <= 0 {
			return nil, fmt.Errorf("вес %q из штрих-кода не число", w)
		}
		scan.WeightG = &g
	}
	if e.ManualWeightG != nil {
		scan.WeightG = e.ManualWeightG
	}

	// Даты: выработка и срок (ДДММГГГГ) — правило или ручной ввод.
	dateField := 3
	if kind == receiving.KindBox {
		dateField = 4
	}
	if d, ok := sliceRule(rule, raw, 2); ok {
		t, err := time.Parse("02012006", d)
		if err != nil {
			return nil, fmt.Errorf("дата выработки %q не распознана (ожидается ДДММГГГГ)", d)
		}
		scan.ProducedOn = &t
	}
	if e.ManualProducedOn != nil {
		scan.ProducedOn = e.ManualProducedOn
	}
	if d, ok := sliceRule(rule, raw, dateField); ok {
		t, err := time.Parse("02012006", d)
		if err != nil {
			return nil, fmt.Errorf("срок годности %q не распознан (ожидается ДДММГГГГ)", d)
		}
		scan.BestBefore = &t
	}
	if e.ManualBestBefore != nil {
		scan.BestBefore = e.ManualBestBefore
	}

	if kind == receiving.KindBox {
		// Кол-во вложений — поле 2 правила коробки.
		if q, ok := sliceRule(rule, raw, 2); ok {
			n, err := strconv.ParseInt(q, 10, 64)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("кол-во вложений %q из кода коробки не число", q)
			}
			scan.Qty = n
			scan.DeclaredQty = &n
		}
		scan.DeclaredWeightG = scan.WeightG
	} else {
		scan.Qty = 1
	}

	return scan, nil
}

// sliceRule вырезает поле правила из штрих-кода; ok=false — поле не задано.
func sliceRule(rule receiving.DecodeRule, raw string, i int) (string, bool) {
	if i < 0 || i >= len(rule.Fields) || rule.Fields[i].Pos <= 0 {
		return "", false
	}
	f := rule.Fields[i]
	start := f.Pos - 1
	if start+f.Len > len(raw) {
		return "", false
	}
	return raw[start : start+f.Len], true
}

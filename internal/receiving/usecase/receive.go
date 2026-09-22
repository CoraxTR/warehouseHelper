// Пакет usecase — сценарии модуля приёмки: кеш поставщика (правила +
// маппинг кодов), резолв штрих-кодов (внутренние 29/33 и внешние по
// правилам), сохранение приёмки (остатки через AcceptStock, веса).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/avgweight"
	"warehouseHelper/internal/decoderules"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/stock"
)

// Ошибки распознавания коробки на приёмке.
var (
	// errBoxNeedsButton — скан кода коробки мимо карточки коробки: коробка
	// открывается кнопкой «+ Коробка», потому что правила товаров и коробок
	// одной длины иначе пересекались бы.
	errBoxNeedsButton = errors.New("код коробки — нажмите «+ Коробка»")
	// errNotBoxCode — запись с вложениями, но код не вычитывается правилом
	// коробок: значит это не код коробки.
	errNotBoxCode = errors.New("это не код коробки — правило коробок его не вычитывает")
	// errBoxNoCode — запись с вложениями без кода: ручной коробки без кода больше
	// нет, коробка всегда открывается сканом своего штрих-кода (решение 22.09).
	errBoxNoCode = errors.New("коробка без кода: отсканируйте штрих-код коробки")
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
}

// StockAccepter — адаптер модуля сроков: принятые партии добавляются к
// остаткам (qty +=), реализует *sucase.StockUseCase.
type StockAccepter interface {
	AcceptStock(ctx context.Context, lots []stock.LotIn) error
}

// WeightRecorder — модуль среднего веса: пишет единичные веса приёмки
// поштучно, обрезает историю до лимита, пересчитывает среднее и обновляет
// каталог и вики; предупреждения его синков — в отчёт. Реализует
// *aucase.UseCase.
type WeightRecorder interface {
	RecordWeights(ctx context.Context, rows []avgweight.WeightRow) ([]string, error)
}

// ReceivingUseCase — сценарии приёмки.
type ReceivingUseCase struct {
	repo    ReceiveRepository
	stock   StockAccepter
	weights WeightRecorder
}

// NewReceivingUseCase создаёт сценарий с хранилищем, адаптером сроков
// и модулем среднего веса.
func NewReceivingUseCase(repo ReceiveRepository, stock StockAccepter, weights WeightRecorder) *ReceivingUseCase {
	return &ReceivingUseCase{repo: repo, stock: stock, weights: weights}
}

// GetCache собирает кеш приёмки поставщика: правила вычитки (куски и
// коробки), маппинг внешних кодов, позиции поставщика для ручного выбора.
func (uc *ReceivingUseCase) GetCache(ctx context.Context, supplierID string) (*receiving.Cache, error) {
	done := metrics.Track(trackPkg, "GetCache")
	defer done()
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
		BBByBatch: needBatchBestBefore(itemRules, receiving.ItemBestBeforeField) ||
			needBatchBestBefore(boxRules, receiving.BoxBestBeforeField),
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
	slices.SortFunc(cache.Products, func(a, b receiving.ProductRef) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	return cache, nil
}

// AddCatalogCodes заполняет кеш приёмки картой «internal_code → товар» по
// всему каталогу — внутренние штрих-коды (29/33) распознаются «на лету»
// даже для товаров, не заведённых у поставщика. Вызывается только при
// отдаче кеша странице; Save резолвит внутренние коды лениво.
func (uc *ReceivingUseCase) AddCatalogCodes(ctx context.Context, cache *receiving.Cache) error {
	done := metrics.Track(trackPkg, "AddCatalogCodes")
	defer done()
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

// needBatchBestBefore — есть ли правило, не вычитывающее срок (поле
// best-before пустое): такие сканы требуют срока партии при приёмке.
func needBatchBestBefore(rules []receiving.DecodeRule, bbField int) bool {
	for _, r := range rules {
		if len(r.Fields) <= bbField || r.Fields[bbField].Pos == 0 {
			return true
		}
	}
	return false
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

// Resolve распознаёт скан. Вид скана задаёт карточка, а не длина строки:
// коробка — запись с вложениями (её открывает кнопка «+ Коробка», и код коробки
// проверяется правилами КОРОБОК), обычный скан — правила товаров и внутренний
// ярлык куска (29). Автозахода в коробку нет: правило товара и правило коробки
// одной длины иначе пересекались бы, а скан кода коробки мимо карточки получает
// подсказку нажать «+ Коробка».
func (uc *ReceivingUseCase) Resolve(ctx context.Context, cache *receiving.Cache, e receiving.ScanEntry) (*receiving.DecodedScan, error) {
	done := metrics.Track(trackPkg, "Resolve")
	defer done()
	raw := strings.TrimSpace(e.Raw)
	// Коробка всегда имеет код (её открывает скан штрих-кода коробки):
	// запись с вложениями без кода — отказ, а не «ручная коробка с карточки».
	if len(e.Children) > 0 && raw == "" {
		return nil, errBoxNoCode
	}
	if raw == "" {
		// Скан без кода: строка блока ручного ввода (код не распознан полностью)
		// либо строка, добавленная оператором руками.
		return resolveManual(cache, e)
	}

	if len(e.Children) > 0 {
		return uc.resolveBoxEntry(ctx, cache, raw, e)
	}

	// Обычный скан: сначала правила товаров по длине, затем внутренний ярлык
	// куска. Правила коробок здесь не участвуют вовсе.
	for _, rule := range cache.ItemRules {
		if rule.Length != len(raw) {
			continue
		}
		return uc.resolveByRule(cache, rule, raw, e, receiving.KindItem)
	}
	if len(raw) == 29 {
		return uc.resolveInternal(ctx, cache, raw, e)
	}
	if len(raw) == 33 || hasBoxRule(cache, len(raw)) {
		return nil, errBoxNeedsButton
	}

	return nil, receiving.ErrScanUnknown
}

// hasBoxRule — заявлено ли у поставщика правило коробок такой длины (нужно
// только для подсказки оператору, что скан похож на код коробки).
func hasBoxRule(cache *receiving.Cache, length int) bool {
	for _, rule := range cache.BoxRules {
		if rule.Length == length {
			return true
		}
	}
	return false
}

// resolveBoxEntry распознаёт саму коробку: её код проверяется правилами
// коробок, а если такого правила нет — внутренним 33-значным ярлыком (своя
// наклейка). Кусок кодом коробки быть не может: тогда это не коробка.
func (uc *ReceivingUseCase) resolveBoxEntry(ctx context.Context, cache *receiving.Cache, raw string, e receiving.ScanEntry) (*receiving.DecodedScan, error) {
	for _, rule := range cache.BoxRules {
		if rule.Length != len(raw) {
			continue
		}
		return uc.resolveByRule(cache, rule, raw, e, receiving.KindBox)
	}
	if len(raw) == 33 {
		s, err := uc.resolveInternal(ctx, cache, raw, e)
		if err != nil {
			return nil, err
		}
		if s.Kind != receiving.KindBox {
			return nil, errNotBoxCode
		}
		return s, nil
	}
	return nil, fmt.Errorf("код коробки длиной %d не подходит ни под одно правило коробок поставщика — проверьте правило на карточке поставщика", len(raw))
}

// resolveInternal разбирает внутренний штрих-код (29 — кусок, 33 — коробка).
func (uc *ReceivingUseCase) resolveInternal(ctx context.Context, cache *receiving.Cache, raw string, _ receiving.ScanEntry) (*receiving.DecodedScan, error) {
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
		Weighted:     ref.Weighted,
	}
	// Вес — только весовым товарам: у штучного в 29-значном коде этикетки
	// стоит sentinel 1 г (ExportLabels), в данные приёмки он попадать не должен.
	if code.WeightG > 0 && ref.Weighted {
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
		scan.Qty = int64(code.Qty)
		scan.DeclaredQty = &q
		// Даты ярлыка коробки — заявленные: по ним Save сверяет даты вложений.
		scan.DeclaredProducedOn = scan.ProducedOn
		scan.DeclaredBestBefore = scan.BestBefore
		// Заявленный вес коробки — только весовым: у штучных сверять нечего
		// (веса у вложений нет), см. resolveBox.
		if ref.Weighted {
			w := int64(code.WeightG)
			scan.DeclaredWeightG = &w
		}
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

	pr, err := resolveProductByRule(cache, rule, raw, e)
	if err != nil {
		return nil, err
	}
	scan.ProductID, scan.InternalCode, scan.ProductName = pr.productID, pr.internalCode, pr.name
	scan.Weighted = pr.weighted
	scan.Weighted = pr.weighted
	if err := fillRuleScanData(scan, rule, raw, e, kind); err != nil {
		return nil, err
	}
	return scan, nil
}

// fillRuleScanData заполняет вычитанные кодом данные скана: вес (только
// весовому), даты (правило, затем ручной ввод строки) и, для коробки,
// заявленные значения — по ним Save сверяет вложения.
func fillRuleScanData(scan *receiving.DecodedScan, rule receiving.DecodeRule, raw string, e receiving.ScanEntry, kind receiving.ScanKind) error {
	// Вес — только весовым товарам (uom кг/г/т). У штучного поле веса правила
	// не вычитывается вовсе: иначе «вес» из цифр кода всплыл бы в карточке и на
	// этикетке, а сама приёмка зависела бы от порядка правил одной длины
	// (первое правило по длине решает, есть вес или нет).
	if scan.Weighted {
		if w, ok := sliceRule(rule, raw, 1); ok {
			g, err := strconv.ParseInt(w, 10, 64)
			if err != nil || g <= 0 {
				return fmt.Errorf("вес %q из штрих-кода не число", w)
			}
			scan.WeightG = &g
		}
		if e.ManualWeightG != nil {
			scan.WeightG = e.ManualWeightG
		}
	}

	// Даты: выработка и срок (ДДММГГГГ) — правило или ручной ввод строки.
	dates, err := resolveRuleDates(rule, raw, kind)
	if err != nil {
		return err
	}

	if kind != receiving.KindBox {
		scan.Qty = 1
		applyDates(scan, dates, e)
		return nil
	}
	// Коробка: заявленные кодом значения — ДО применения ручных дат: по ним Save
	// сверяет даты вложений (ручные даты партии в сверке не участвуют).
	if dates.hasProduced {
		p := dates.producedOn
		scan.DeclaredProducedOn = &p
	}
	if dates.hasBestBefore {
		b := dates.bestBefore
		scan.DeclaredBestBefore = &b
	}
	if err := fillBoxQty(scan, rule, raw); err != nil {
		return err
	}
	if scan.WeightG != nil {
		w := *scan.WeightG
		scan.DeclaredWeightG = &w
	}
	applyDates(scan, dates, e)
	return nil
}

// fillBoxQty — заявленное кодом число вложений коробки (нет поля — нечего
// заявлять, количество посчитается по вложениям).
func fillBoxQty(scan *receiving.DecodedScan, rule receiving.DecodeRule, raw string) error {
	q, ok := sliceRule(rule, raw, decoderules.BoxQty)
	if !ok {
		return nil
	}
	qty, err := strconv.ParseInt(q, 10, 64)
	if err != nil || qty <= 0 {
		return fmt.Errorf("кол-во вложений %q из штрих-кода не число", q)
	}
	scan.Qty = qty
	scan.DeclaredQty = &qty
	return nil
}

// applyDates — действующие даты скана: правило, затем ручной ввод строки.
func applyDates(scan *receiving.DecodedScan, dates ruleDates, e receiving.ScanEntry) {
	if dates.hasProduced {
		p := dates.producedOn
		scan.ProducedOn = &p
	}
	if dates.hasBestBefore {
		b := dates.bestBefore
		scan.BestBefore = &b
	}
	if e.ManualProducedOn != nil {
		scan.ProducedOn = e.ManualProducedOn
	}
	if e.ManualBestBefore != nil {
		scan.BestBefore = e.ManualBestBefore
	}
}

// productResolve — результат определения товара по правилу (структура,
// чтобы не плодить 4-значные сигнатуры).
type productResolve struct {
	productID    string
	internalCode string
	name         string
	weighted     bool
}

// findProduct ищет позицию поставщика (кеш) по id товара.
func findProduct(cache *receiving.Cache, productID string) (receiving.ProductRef, bool) {
	for i := range cache.Products {
		if cache.Products[i].ProductID == productID {
			return cache.Products[i], true
		}
	}
	return receiving.ProductRef{}, false
}

// resolveManual собирает скан из ручных полей, когда кода нет вовсе: строка
// блока ручного ввода (код не распознан полностью) либо строка, добавленная
// оператором руками. Коробки сюда не попадают: у коробки всегда есть код
// (запись с вложениями без кода отбивается в Resolve). Товар берётся из позиций
// поставщика, вес и даты — из полей строки (полноту проверяет Save: срок
// обязателен всем, вес — весовым).
func resolveManual(cache *receiving.Cache, e receiving.ScanEntry) (*receiving.DecodedScan, error) {
	if e.ManualProductID == "" {
		return nil, errors.New("пустой штрих-код без выбранного товара")
	}
	ref, ok := findProduct(cache, e.ManualProductID)
	if !ok {
		return nil, fmt.Errorf("товар %q не найден в позициях поставщика", e.ManualProductID)
	}
	scan := &receiving.DecodedScan{
		ProductID:    ref.ProductID,
		InternalCode: ref.InternalCode,
		ProductName:  ref.Name,
		Weighted:     ref.Weighted,
		ProducedOn:   e.ManualProducedOn,
		BestBefore:   e.ManualBestBefore,
	}
	scan.Kind = receiving.KindItem
	scan.Qty = 1
	// Вес — только весовым товарам: у штучного ручной вес гасится.
	if ref.Weighted {
		scan.WeightG = e.ManualWeightG
	}
	return scan, nil
}

// resolveProductByRule определяет товар скана: внешний код из правила через
// маппинг поставщика, либо ручной выбор позиции (с дополнением из списка
// позиций — код товара в этом случае правилом не вычитывается).
func resolveProductByRule(cache *receiving.Cache, rule receiving.DecodeRule, raw string, e receiving.ScanEntry) (productResolve, error) {
	code, ok := sliceRule(rule, raw, 0)
	if ok {
		if ref, refOK := cache.ByExternal[code]; refOK {
			return productResolve{ref.ProductID, ref.InternalCode, ref.ProductName, ref.Weighted}, nil
		}
		return productResolve{}, fmt.Errorf("внешний код %q не заведён у поставщика — добавьте его на карточке поставщика", code)
	}
	if e.ManualProductID != "" {
		if p, found := findProduct(cache, e.ManualProductID); found {
			return productResolve{p.ProductID, p.InternalCode, p.Name, p.Weighted}, nil
		}
		return productResolve{productID: e.ManualProductID}, nil
	}
	return productResolve{}, errors.New("в правиле не вычитывается код товара — выберите позицию вручную")
}

// ruleDates — даты, вычитанные кодом по правилу: поля в правиле нет — дата
// пустая (даты есть не всегда: у куска в правиле может не быть срока).
type ruleDates struct {
	producedOn    time.Time
	hasProduced   bool
	bestBefore    time.Time
	hasBestBefore bool
}

// resolveRuleDates вычитывает обе даты правила. Поля берутся по виду скана: у
// коробки выработка — четвёртое поле (между весом и датами стоит кол-во
// вложений), чтение её из поля кол-ва ломало приёмку коробок.
func resolveRuleDates(rule receiving.DecodeRule, raw string, kind receiving.ScanKind) (ruleDates, error) {
	producedField := decoderules.FieldProducedOn
	bestBeforeField := decoderules.FieldBestBefore
	if kind == receiving.KindBox {
		producedField = decoderules.BoxProducedOn
		bestBeforeField = decoderules.BoxBestBefore
	}
	producedOn, hasProduced, err := resolveRuleDate(rule, raw, producedField, "дата выработки")
	if err != nil {
		return ruleDates{}, err
	}
	bestBefore, hasBestBefore, err := resolveRuleDate(rule, raw, bestBeforeField, "срок годности")
	if err != nil {
		return ruleDates{}, err
	}
	return ruleDates{
		producedOn:    producedOn,
		hasProduced:   hasProduced,
		bestBefore:    bestBefore,
		hasBestBefore: hasBestBefore,
	}, nil
}

// resolveRuleDate вычитывает дату ДДММГГГГ из поля правила; ok=false — поля
// не задано (дата известна не всегда: у куска в правиле может не быть срока).
func resolveRuleDate(rule receiving.DecodeRule, raw string, field int, label string) (time.Time, bool, error) {
	d, ok := sliceRule(rule, raw, field)
	if !ok {
		return time.Time{}, false, nil
	}
	t, err := time.Parse("02012006", d)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("%s %q не распознана (ожидается ДДММГГГГ)", label, d)
	}
	return t, true, nil
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

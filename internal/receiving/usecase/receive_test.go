package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/avgweight"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/stock"
)

// --- стабы ---

type stubReceiveRepo struct {
	supplier *domain.Supplier
	barcodes []receiving.BarcodeRef
	catalog  map[string]receiving.ProductRef
}

func (s *stubReceiveRepo) GetSupplier(context.Context, string) (*domain.Supplier, error) {
	if s.supplier == nil {
		return nil, domain.ErrSupplierNotFound
	}
	return s.supplier, nil
}

func (s *stubReceiveRepo) LoadSupplierBarcodes(context.Context, string) ([]receiving.BarcodeRef, error) {
	return s.barcodes, nil
}

func (s *stubReceiveRepo) LoadCatalogProductsByCodes(_ context.Context, codes []string) (map[string]receiving.ProductRef, error) {
	out := make(map[string]receiving.ProductRef, len(codes))
	for _, c := range codes {
		if p, ok := s.catalog[c]; ok {
			out[c] = p
		}
	}
	return out, nil
}

func (s *stubReceiveRepo) LoadCatalogAllRefs(_ context.Context) ([]receiving.ProductRef, error) {
	out := make([]receiving.ProductRef, 0, len(s.catalog))
	for _, p := range s.catalog {
		out = append(out, p)
	}
	return out, nil
}

type stubWeightRecorder struct {
	recorded []avgweight.WeightRow
	warnings []string
	err      error
}

func (s *stubWeightRecorder) RecordWeights(_ context.Context, rows []avgweight.WeightRow) ([]string, error) {
	s.recorded = append(s.recorded, rows...)
	return s.warnings, s.err
}

type stubStockAccepter struct {
	lots []stock.LotIn
}

func (s *stubStockAccepter) AcceptStock(_ context.Context, lots []stock.LotIn) error {
	s.lots = append(s.lots, lots...)
	return nil
}

// --- фикстуры ---

// Внутренние коды: кусок 29 (код 8 + вес 5 + ДДММГГГГ 8 + ДДММГГГГ 8),
// коробка 33 (код 8 + вес 6 + кол-во 3 + ДДММГГГГ 8 + ДДММГГГГ 8).
// Собираются конкатенацией констант — потерянная цифра меняет длину.
const (
	intCode     = "00210003"
	itemBarcode = intCode + "00250" + "29082026" + "29092026"          // 29
	boxBarcode  = intCode + "025000" + "010" + "29082026" + "29092026" // 33
)

// Внешний код по правилу "28-1-6-7-6-13-8-21-8": код 6 + вес 6 + даты 8+8.
const (
	extCode        = "123456"
	extBarcode     = extCode + "000250" + "29082026" + "29092026" // 28
	itemRule       = "28-1-6-7-6-13-8-21-8"
	itemRuleNoCode = "28- -6-7-6-13-8-21-8" // код товара не вычитывается

	// Правило без веса: код товара 6 + выработка 8 + срок 8 (вес не вычитывается).
	itemRuleNoWeight   = "22-1-6- -0-7-8-15-8"
	extBarcodeNoWeight = extCode + "29082026" + "29092026" // 22

	// Коробка поставщика: код 6 (поз.1), общий вес 6 (поз.7), кол-во вложений 3
	// (поз.13), выработка 8 (поз.16), срок 8 (поз.24); последние 2 знака кода
	// правилом не вычитываются.
	boxRule       = "33-1-6-7-6-13-3-16-8-24-8"
	supBoxBarcode = extCode + "002500" + "010" + "29082026" + "29092026" + "77" // 33

	// Правило товара длиной 29 — как у внутреннего ярлыка куска: код поставщика
	// такой длины должен разбираться правилом, а не внутренним форматом.
	itemRule29   = "29-1-6-7-6-13-8-21-8"
	supBarcode29 = extCode + "000250" + "29082026" + "29092026" + "7" // 29

	// Штучный товар (вес при приёмке не спрашивают).
	pieceCode    = "00210010"
	pieceExtCode = "777777"
	pieceBarcode = pieceExtCode + "29082026" + "29092026" // 22

	// 13-значный внешний код: префикс + код 6 + вес 5 + цифра. Правило с полем
	// веса: у весового товара вес из кода читается, у штучного — гасится, и
	// порядок правил одной длины на приёмку не влияет.
	ruleWeighted13     = "13-2-6-8-5- -0- -0"
	ruleNoWeight13     = "13-2-6- -0- -0- -0"
	weightedBarcode13  = "4" + extCode + "00250" + "4"      // 13
	pieceBarcode13     = "4" + pieceExtCode + "23283" + "4" // 13
	pieceBarcode13BadW = "4" + pieceExtCode + "2328A" + "4" // 13, вес не число

	// 21-значный код того же вида + срок годности (для приёмки целиком):
	// код 6 + вес 5 + ДДММГГГГ 8.
	ruleWeighted21 = "21-2-6-8-5- -0-13-8"
	pieceBarcode21 = "4" + pieceExtCode + "23283" + "29092026" + "4" // 21
)

// addPieceProduct заводит у поставщика штучный товар: uom «шт», вес не нужен.
func addPieceProduct(repo *stubReceiveRepo) {
	repo.barcodes = append(repo.barcodes, receiving.BarcodeRef{
		ExternalCode: pieceExtCode, ProductID: "p2", ProductName: "Хлеб Бородинский",
		InternalCode: pieceCode, Weighted: false,
	})
	repo.catalog[pieceCode] = receiving.ProductRef{
		ProductID: "p2", InternalCode: pieceCode, Name: "Хлеб Бородинский", Weighted: false,
	}
}

func testCacheRepo() *stubReceiveRepo {
	return &stubReceiveRepo{
		supplier: &domain.Supplier{
			ID:             "sup-1",
			DecodeRules:    []string{itemRule},
			BoxDecodeRules: []string{boxRule},
		},
		barcodes: []receiving.BarcodeRef{
			{ExternalCode: extCode, ProductID: "p1", ProductName: "Говядина охл.", InternalCode: intCode, Weighted: true},
		},
		catalog: map[string]receiving.ProductRef{
			intCode: {ProductID: "p1", InternalCode: intCode, Name: "Говядина охл.", Weighted: true},
		},
	}
}

func newTestReceive() (*ReceivingUseCase, *stubStockAccepter) {
	repo := testCacheRepo()
	stock := &stubStockAccepter{}
	return NewReceivingUseCase(repo, stock, &stubWeightRecorder{}), stock
}

// d — дата 2026 года: в фикстурах приёмки других лет нет, год не параметр
// (unparam: параметр-константа — лишний).
func d(m time.Month, day int) time.Time {
	return time.Date(2026, m, day, 0, 0, 0, 0, time.UTC)
}

// --- тесты ---

func TestGetCache(t *testing.T) {
	uc, _ := newTestReceive()

	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}
	if len(cache.ItemRules) != 1 || len(cache.BoxRules) != 1 {
		t.Fatalf("правила: item=%d box=%d, want 1/1", len(cache.ItemRules), len(cache.BoxRules))
	}
	if len(cache.ByExternal) != 1 || len(cache.Products) != 1 {
		t.Fatalf("маппинг/позиции: external=%d products=%d", len(cache.ByExternal), len(cache.Products))
	}
	if !cache.Products[0].Weighted {
		t.Error("позиция должна быть весовой")
	}
	// Все правила вычитывают срок — батч-срок не нужен.
	if cache.BBByBatch {
		t.Error("BBByBatch: все правила с датами, ожидалось false")
	}
}

func TestGetCache_BBByBatch(t *testing.T) {
	// Хоть одно правило без дат (товарное или коробочное) → срок партии.
	cases := []struct {
		name   string
		item   []string
		box    []string
		wantBB bool
	}{
		{name: "товарное правило без дат", item: []string{"12-1-6-7-5- -0- -0"}, box: nil, wantBB: true},
		{name: "коробочное правило без дат", item: nil, box: []string{"13-2-6-8-5- -0- -0- -0"}, wantBB: true},
		{name: "без правил", item: nil, box: nil, wantBB: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testCacheRepo()
			repo.supplier.DecodeRules = tc.item
			repo.supplier.BoxDecodeRules = tc.box
			uc := NewReceivingUseCase(repo, &stubStockAccepter{}, &stubWeightRecorder{})

			cache, err := uc.GetCache(context.Background(), "sup-1")
			if err != nil {
				t.Fatalf("GetCache: %v", err)
			}
			if cache.BBByBatch != tc.wantBB {
				t.Fatalf("BBByBatch = %v, want %v", cache.BBByBatch, tc.wantBB)
			}
		})
	}
}

func TestResolveInternalItem(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: itemBarcode})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Kind != receiving.KindItem || s.ProductID != "p1" || s.InternalCode != intCode {
		t.Fatalf("кусок: %+v", s)
	}
	if s.WeightG == nil || *s.WeightG != 250 {
		t.Fatalf("вес: %v", s.WeightG)
	}
	if s.BestBefore == nil || !s.BestBefore.Equal(d(9, 29)) {
		t.Fatalf("срок: %v", s.BestBefore)
	}
}

// Коробка поставщика разбирается правилом коробки только внутри карточки
// коробки (запись с вложениями): верхний уровень коробку не открывает.
func TestResolveSupplierBoxByRule(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{
		Raw:      supBoxBarcode,
		Children: []receiving.ScanEntry{{Raw: itemBarcode}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Kind != receiving.KindBox || s.IsInternal {
		t.Fatalf("коробка: %+v", s)
	}
	if s.ProductID != "p1" || s.ProductName != "Говядина охл." {
		t.Fatalf("товар коробки: %+v", s)
	}
	if s.DeclaredQty == nil || *s.DeclaredQty != 10 {
		t.Fatalf("кол-во вложений: %v", s.DeclaredQty)
	}
	if s.DeclaredWeightG == nil || *s.DeclaredWeightG != 2500 {
		t.Fatalf("вес коробки: %v", s.DeclaredWeightG)
	}
	// Выработка — четвёртое поле правила коробки, а не поле кол-ва вложений.
	if s.ProducedOn == nil || !s.ProducedOn.Equal(d(8, 29)) {
		t.Fatalf("выработка из правила: %v", s.ProducedOn)
	}
	if s.BestBefore == nil || !s.BestBefore.Equal(d(9, 29)) {
		t.Fatalf("срок из правила: %v", s.BestBefore)
	}
	// Даты, вычитанные кодом коробки, — заявленные: по ним Save сверяет вложения.
	if s.DeclaredProducedOn == nil || !s.DeclaredProducedOn.Equal(d(8, 29)) ||
		s.DeclaredBestBefore == nil || !s.DeclaredBestBefore.Equal(d(9, 29)) {
		t.Fatalf("заявленные даты коробки: %+v", s)
	}
}

// Верхний уровень коробку не открывает: код коробки (по правилу коробок или наш
// 33-значный ярлык) отвечает отказом-подсказкой «нажмите „+ Коробка“».
func TestResolveBoxCodeOnTopLevelRefused(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	if _, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: supBoxBarcode}); !errors.Is(err, errBoxNeedsButton) {
		t.Fatalf("правило коробок: ожидался отказ «нажмите + Коробка», получил %v", err)
	}

	// Тот же отказ у нашего 33-значного ярлыка, когда правила коробок нет.
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.BoxDecodeRules = nil
	uc2 := NewReceivingUseCase(repo, &stubStockAccepter{}, &stubWeightRecorder{})
	cache2, err := uc2.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}
	if _, err := uc2.Resolve(context.Background(), cache2, receiving.ScanEntry{Raw: boxBarcode}); !errors.Is(err, errBoxNeedsButton) {
		t.Fatalf("внутренний ярлык: ожидался отказ «нажмите + Коробка», получил %v", err)
	}
}

// Код поставщика длиной 29 (как внутренний ярлык куска) разбирается правилом
// товара: длина строки сама по себе вид скана не решает.
func TestResolveSupplierItemWithInternalLength(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.BoxDecodeRules = nil
	repo.supplier.DecodeRules = []string{itemRule29}
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: supBarcode29})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Kind != receiving.KindItem || s.IsInternal || s.ProductID != "p1" {
		t.Fatalf("кусок по правилу: %+v", s)
	}
	if s.WeightG == nil || *s.WeightG != 250 {
		t.Fatalf("вес: %v", s.WeightG)
	}
	if s.BestBefore == nil || !s.BestBefore.Equal(d(9, 29)) {
		t.Fatalf("срок: %v", s.BestBefore)
	}
}

// Внутренний 33-значный ярлык коробки читается внутри карточки коробки, когда
// правило коробок такой длины не заявлено.
func TestResolveInternalBoxInCard(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.BoxDecodeRules = nil
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{
		Raw:      boxBarcode,
		Children: []receiving.ScanEntry{{Raw: itemBarcode}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Kind != receiving.KindBox || !s.IsInternal {
		t.Fatalf("коробка: %+v", s)
	}
	if s.DeclaredQty == nil || *s.DeclaredQty != 10 {
		t.Fatalf("кол-во вложений: %v", s.DeclaredQty)
	}
	if s.DeclaredWeightG == nil || *s.DeclaredWeightG != 25000 {
		t.Fatalf("вес коробки: %v", s.DeclaredWeightG)
	}
	// Даты ярлыка — заявленные коробки (по ним Save сверяет даты вложений).
	if s.DeclaredProducedOn == nil || !s.DeclaredProducedOn.Equal(d(8, 29)) ||
		s.DeclaredBestBefore == nil || !s.DeclaredBestBefore.Equal(d(9, 29)) {
		t.Fatalf("заявленные даты ярлыка: %+v", s)
	}
}

func TestResolveExternalItem(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: extBarcode})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Kind != receiving.KindItem || s.ProductID != "p1" || s.ProductName != "Говядина охл." {
		t.Fatalf("внешний кусок: %+v", s)
	}
	if s.WeightG == nil || *s.WeightG != 250 {
		t.Fatalf("вес: %v", s.WeightG)
	}
}

func TestResolveExternalCodeNotMapped(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	// Код не заведён у поставщика: правильная длина, но нет в маппинге.
	raw := "999999" + "000250" + "29082026" + "29092026"
	_, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: raw})
	if err == nil {
		t.Fatal("ожидалась ошибка о незаведённом коде")
	}
}

func TestResolveManualProduct(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.DecodeRules = []string{itemRuleNoCode}
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: extBarcode, ManualProductID: "p1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.ProductID != "p1" {
		t.Fatalf("товар из ручного выбора: %+v", s)
	}
}

func TestResolveUnknown(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	_, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: "12345"})
	if !errors.Is(err, receiving.ErrScanUnknown) {
		t.Fatalf("ожидался ErrScanUnknown, получил %v", err)
	}
}

func TestSave(t *testing.T) {
	uc, stock := newTestReceive()

	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{Raw: itemBarcode},
			{Raw: extBarcode},
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(res.Units) != 2 {
		t.Fatalf("единиц: %d, want 2", len(res.Units))
	}
	if len(stock.lots) != 1 || stock.lots[0].Qty != 2 {
		t.Fatalf("лоты: %+v", stock.lots)
	}
	if !stock.lots[0].BestBefore.Equal(d(9, 29)) {
		t.Fatalf("срок лота: %v", stock.lots[0].BestBefore)
	}
	weights, ok := uc.weights.(*stubWeightRecorder)
	if !ok {
		t.Fatal("ожидался stubWeightRecorder")
	}
	if len(weights.recorded) != 2 {
		t.Fatalf("весов передано: %d, want 2", len(weights.recorded))
	}
	if res.Warnings != nil {
		t.Fatalf("предупреждений быть не должно: %v", res.Warnings)
	}
	if len(res.Rows) != 1 || res.Rows[0].ProductName != "Говядина охл." {
		t.Fatalf("отчёт: %+v", res.Rows)
	}
	if res.Rows[0].QtyKg != 0.5 { // 250 г + 250 г
		t.Fatalf("кг в отчёте: %v", res.Rows[0].QtyKg)
	}
	if !res.Rows[0].Weighted {
		t.Fatal("весовой товар в отчёте должен помечаться weighted (кг, не шт)")
	}
}

func TestSaveWeightSyncWarnings(t *testing.T) {
	uc, _ := newTestReceive()

	// Модуль среднего веса вернул предупреждения синков — приёмка проходит,
	// предупреждения уходят в отчёт с именем товара вместо id.
	weights, ok := uc.weights.(*stubWeightRecorder)
	if !ok {
		t.Fatal("ожидался stubWeightRecorder")
	}
	weights.warnings = []string{"товар p1: каталог не обновлён (ошибка)", "товар p1: вики не обновлена (ошибка)"}

	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: itemBarcode}},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("предупреждений: %d, want 2", len(res.Warnings))
	}
	if !strings.Contains(res.Warnings[0], "Говядина охл.") || strings.Contains(res.Warnings[0], "p1:") {
		t.Fatalf("id товара должен быть заменён именем: %q", res.Warnings[0])
	}
}

func TestSaveWeightRecorderFailure(t *testing.T) {
	uc, _ := newTestReceive()

	// Ядро модуля среднего веса (запись весов) упало — Save падает целиком.
	weights, ok := uc.weights.(*stubWeightRecorder)
	if !ok {
		t.Fatal("ожидался stubWeightRecorder")
	}
	weights.err = errors.New("база недоступна")

	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: itemBarcode}},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка при падении записи весов")
	}
}

// Коробка поставщика, разобранная правилом: заявлено 10 вложений по 250 г, в
// сканах — 2 куска. Приёмка проходит «как есть», расхождение помечается.
func TestSaveSupplierBoxMismatch(t *testing.T) {
	uc, _ := newTestReceive()

	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{
				Raw: supBoxBarcode,
				Children: []receiving.ScanEntry{
					{Raw: itemBarcode},
					{Raw: itemBarcode},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(res.Boxes) != 1 {
		t.Fatalf("коробок: %d, want 1", len(res.Boxes))
	}
	b := res.Boxes[0]
	if !b.Mismatch {
		t.Fatal("ожидалось расхождение (заявлено 10, внутри 2)")
	}
	if b.Qty != 2 || b.WeightG != 500 {
		t.Fatalf("факт коробки: %+v", b)
	}
	if b.DeclaredQty == nil || *b.DeclaredQty != 10 || b.DeclaredWeightG == nil || *b.DeclaredWeightG != 2500 {
		t.Fatalf("заявленное коробки: %+v", b)
	}
	// Даты коробки — из правила коробки (выработка — четвёртое поле).
	if b.ProducedOn == nil || !b.ProducedOn.Equal(d(8, 29)) || b.BestBefore == nil || !b.BestBefore.Equal(d(9, 29)) {
		t.Fatalf("даты коробки: %+v", b)
	}
	if len(res.Units) != 2 {
		t.Fatalf("единиц из коробки: %d, want 2", len(res.Units))
	}
	if !res.Units[0].InBox || !res.Units[0].BoxMismatch {
		t.Fatalf("кусок из коробки: %+v", res.Units[0])
	}
}

// Совпавшая с заявленной коробка: 2 куска, 500 г — расхождения нет.
func TestSaveSupplierBoxMatchingDeclared(t *testing.T) {
	uc, _ := newTestReceive()

	// Заявлено 2 вложения и 500 г — ровно то, что внутри.
	raw := extCode + "000500" + "002" + "29082026" + "29092026" + "77"
	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{
				Raw: raw,
				Children: []receiving.ScanEntry{
					{Raw: itemBarcode},
					{Raw: itemBarcode},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(res.Boxes) != 1 {
		t.Fatalf("коробок: %d, want 1", len(res.Boxes))
	}
	if b := res.Boxes[0]; b.Mismatch || b.Qty != 2 || b.WeightG != 500 {
		t.Fatalf("коробка: %+v", b)
	}
}

func TestSaveBoxDifferentProducts(t *testing.T) {
	uc, _ := newTestReceive()

	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{
				Raw: supBoxBarcode,
				Children: []receiving.ScanEntry{
					{Raw: itemBarcode},
					{Raw: "00310003" + "00250" + "29082026" + "29092026"}, // другой internal_code
				},
			},
		},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка о разных товарах в коробке")
	}
}

// Ручной коробки без кода больше нет: запись с вложениями и пустым Raw — отказ
// (коробка всегда открывается сканом своего штрих-кода — решение владельца).
func TestSaveBoxWithoutCodeRefused(t *testing.T) {
	uc, _ := newTestReceive()
	pd, bb := d(8, 28), d(9, 5)

	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{
				ManualProductID:  "p1",
				ManualProducedOn: &pd,
				ManualBestBefore: &bb,
				Children:         []receiving.ScanEntry{{Raw: itemBarcode}},
			},
		},
	})
	if !errors.Is(err, errBoxNoCode) {
		t.Fatalf("ожидался отказ «коробка без кода», получил: %v", err)
	}
}

// Товар коробки (из кода) обязан совпасть с товаром вложений, иначе наклейка
// ушла бы на другую позицию.
func TestSaveBoxProductDiffersFromChildren(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addPieceProduct(repo)
	// Код коробки заявлен штучным товаром (777777), внутри — весовой кусок p1.
	raw := pieceExtCode + "000250" + "002" + "29082026" + "29092026" + "77"

	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{Raw: raw, Children: []receiving.ScanEntry{{Raw: itemBarcode}}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "не совпадает с товаром вложений") {
		t.Fatalf("ожидалась ошибка о разных товарах, получил: %v", err)
	}
}

// Даты вложения обязаны совпадать с датами, вычитанными КОДОМ коробки: это
// отказ (в отличие от расхождения кол-ва/веса, которое лишь помечается).
func TestSaveBoxChildDateMismatch(t *testing.T) {
	cases := []struct {
		name string
		raw  string // внутренний 29-значный ярлык куска
		want string
	}{
		{
			name: "срок вложения",
			raw:  intCode + "00250" + "29082026" + "05102026",
			want: "срок годности 05.10.2026 не совпадает со сроком из кода коробки 29.09.2026",
		},
		{
			name: "выработка вложения",
			raw:  intCode + "00250" + "01082026" + "29092026",
			want: "выработка 01.08.2026 не совпадает с выработкой из кода коробки 29.08.2026",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc, _ := newTestReceive()

			_, err := uc.Save(context.Background(), receiving.SaveRequest{
				SupplierID: "sup-1",
				Scans: []receiving.ScanEntry{
					{Raw: supBoxBarcode, Children: []receiving.ScanEntry{{Raw: tc.raw}}},
				},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ожидалась ошибка %q, получил: %v", tc.want, err)
			}
		})
	}
}

// Код коробки на верхнем уровне — не коробка: страница держит карточку коробки
// открытой и ждёт вложения, а запрос мимо страницы отбивается подсказкой.
// Сторож «в коробке нет вложений» остаётся в resolveBox для таких запросов.
func TestSaveBoxWithoutChildren(t *testing.T) {
	uc, _ := newTestReceive()

	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: supBoxBarcode}},
	})
	if !errors.Is(err, errBoxNeedsButton) {
		t.Fatalf("ожидался отказ «нажмите + Коробка», получил: %v", err)
	}

	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}
	_, _, err = uc.resolveBox(context.Background(), cache,
		&receiving.DecodedScan{Kind: receiving.KindBox, Raw: supBoxBarcode},
		receiving.ScanEntry{Raw: supBoxBarcode})
	if err == nil || !strings.Contains(err.Error(), "в коробке нет отсканированных товаров") {
		t.Fatalf("сторож пустой коробки: %v", err)
	}
}

// Наклейки коробок: коробка без срока годности в файл не попадает (33-значный
// код без дат не собрать), весовость, вес и число вложений переносятся как есть.
func TestToLabelBoxes(t *testing.T) {
	pd, bb := d(8, 28), d(9, 5)

	out := toLabelBoxes([]receiving.Box{
		{InternalCode: intCode, ProductName: "Говядина охл.", Weighted: true, WeightG: 500, Qty: 2, ProducedOn: &pd, BestBefore: &bb},
		{InternalCode: intCode, ProductName: "Говядина охл.", Weighted: true, WeightG: 250, Qty: 1, ProducedOn: &pd},
	})
	if len(out) != 1 {
		t.Fatalf("наклеек: %d, want 1 (коробка без срока пропускается)", len(out))
	}
	got := out[0]
	if got.InternalCode != intCode || got.WeightG != 500 || got.Qty != 2 || !got.Weighted {
		t.Fatalf("наклейка: %+v", got)
	}
	if !got.BestBefore.Equal(bb) || !got.ProducedOn.Equal(pd) {
		t.Fatalf("даты наклейки: %+v", got)
	}
}

func TestSaveMissingBestBefore(t *testing.T) {
	uc, _ := newTestReceive()

	// Правило без срока годности и без ручного ввода → ошибка.
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.DecodeRules = []string{"28-1-6-7-6-13-8"}
	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: extBarcode}},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка о неуказанном сроке")
	}
}

func TestResolveManualWeightFromEntry(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.DecodeRules = []string{itemRuleNoWeight}
	cache, _ := uc.GetCache(context.Background(), "sup-1")
	w := int64(2450)

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: extBarcodeNoWeight, ManualWeightG: &w})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.WeightG == nil || *s.WeightG != 2450 {
		t.Fatalf("вес: %v, want 2450", s.WeightG)
	}
	if !s.Weighted || s.ProductID != "p1" {
		t.Fatalf("скан: %+v", s)
	}
	if s.BestBefore == nil || !s.BestBefore.Equal(d(9, 29)) {
		t.Fatalf("срок из правила: %v", s.BestBefore)
	}
}

func TestResolveManualWeightOverridesCode(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")
	w := int64(999) // в коде 250 г — ручное перекрывает

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: extBarcode, ManualWeightG: &w})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.WeightG == nil || *s.WeightG != 999 {
		t.Fatalf("вес: %v, want 999 (ручной ввод перекрывает код)", s.WeightG)
	}
}

func TestResolveManualEntryWithoutRaw(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")
	w := int64(2500)
	pd, bb := d(8, 28), d(9, 5)

	// Строка блока ручного ввода: код не распознан полностью, значения — из ячеек.
	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{
		ManualProductID:  "p1",
		ManualWeightG:    &w,
		ManualProducedOn: &pd,
		ManualBestBefore: &bb,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Raw != "" || s.Kind != receiving.KindItem || s.ProductID != "p1" || s.InternalCode != intCode {
		t.Fatalf("скан: %+v", s)
	}
	if s.WeightG == nil || *s.WeightG != 2500 || !s.Weighted {
		t.Fatalf("вес/весовость: %+v", s)
	}
	if s.BestBefore == nil || !s.BestBefore.Equal(bb) || s.ProducedOn == nil || !s.ProducedOn.Equal(pd) {
		t.Fatalf("даты: %+v", s)
	}
}

func TestResolveManualEntryWithoutProduct(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	// Скан без кода и без выбранного товара — принять нечего.
	if _, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{}); err == nil {
		t.Fatal("ожидалась ошибка о пустом штрих-коде без товара")
	}
}

func TestSaveManualEntriesWithoutRaw(t *testing.T) {
	uc, stock := newTestReceive()
	w1, w2 := int64(2450), int64(2510)
	pd, bb := d(8, 28), d(9, 5)

	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{ManualProductID: "p1", ManualWeightG: &w1, ManualProducedOn: &pd, ManualBestBefore: &bb},
			{ManualProductID: "p1", ManualWeightG: &w2, ManualProducedOn: &pd, ManualBestBefore: &bb},
		},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(res.Units) != 2 {
		t.Fatalf("единиц: %d, want 2", len(res.Units))
	}
	if res.Units[0].WeightG != 2450 || res.Units[1].WeightG != 2510 {
		t.Fatalf("веса: %d, %d", res.Units[0].WeightG, res.Units[1].WeightG)
	}
	if len(stock.lots) != 1 || stock.lots[0].Qty != 2 {
		t.Fatalf("лоты: %+v", stock.lots)
	}
	if !stock.lots[0].BestBefore.Equal(bb) {
		t.Fatalf("срок лота: %v, want %v", stock.lots[0].BestBefore, bb)
	}
	weights, ok := uc.weights.(*stubWeightRecorder)
	if !ok {
		t.Fatal("ожидался stubWeightRecorder")
	}
	if len(weights.recorded) != 2 {
		t.Fatalf("весов передано: %d, want 2", len(weights.recorded))
	}
	if len(res.Rows) != 1 || !res.Rows[0].Weighted || res.Rows[0].QtyKg != 4.96 {
		t.Fatalf("отчёт: %+v", res.Rows)
	}
}

func TestSavePieceProductWithoutWeight(t *testing.T) {
	uc, stock := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addPieceProduct(repo)
	repo.supplier.DecodeRules = []string{itemRuleNoWeight}

	// Штучный товар: вес не вычитывается и не вводится — приёмка проходит,
	// веса в статистику не уходят, отчёт в штуках.
	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: pieceBarcode}},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(res.Units) != 1 || res.Units[0].Weighted || res.Units[0].WeightG != 0 {
		t.Fatalf("единица: %+v", res.Units)
	}
	weights, ok := uc.weights.(*stubWeightRecorder)
	if !ok {
		t.Fatal("ожидался stubWeightRecorder")
	}
	if len(weights.recorded) != 0 {
		t.Fatalf("веса штучных писать не должны: %+v", weights.recorded)
	}
	if len(res.Rows) != 1 || res.Rows[0].Weighted || res.Rows[0].Qty != 1 || res.Rows[0].QtyKg != 0 {
		t.Fatalf("отчёт: %+v", res.Rows)
	}
	if len(stock.lots) != 1 || stock.lots[0].Qty != 1 {
		t.Fatalf("лоты: %+v", stock.lots)
	}
}

func TestSaveWeightedRequiresWeight(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.DecodeRules = []string{itemRuleNoWeight}

	// Весовой товар, вес не вычитан кодом и не введён вручную → отказ.
	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: extBarcodeNoWeight}},
	})
	if err == nil || !strings.Contains(err.Error(), "не указан вес") {
		t.Fatalf("ожидалась ошибка о весе, получили %v", err)
	}
}

// Вес вычитывается только весовым товарам: у штучного поле веса правила
// гасится, поэтому порядок правил одной длины на результат не влияет.
// У весового порядок по-прежнему решает, откуда вес: правило без поля веса —
// вес вводится вручную (это выбор правил поставщика, не приёмки).
func TestResolveWeightOnlyForWeightedProduct(t *testing.T) {
	cases := []struct {
		name          string
		rules         []string
		wantWeightedG *int64 // вес весового скана: из поля кода или nil (вручную)
	}{
		{
			name:          "весовое правило первым",
			rules:         []string{ruleWeighted13, ruleNoWeight13},
			wantWeightedG: new(int64(250)),
		},
		{
			name:  "правило без веса первым",
			rules: []string{ruleNoWeight13, ruleWeighted13},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc, _ := newTestReceive()
			repo, ok := uc.repo.(*stubReceiveRepo)
			if !ok {
				t.Fatal("ожидался stubReceiveRepo")
			}
			addPieceProduct(repo)
			repo.supplier.DecodeRules = tc.rules
			cache, err := uc.GetCache(context.Background(), "sup-1")
			if err != nil {
				t.Fatalf("GetCache: %v", err)
			}

			// Штучный: код вычитан, «вес» 23283 из цифр кода не взят.
			p, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: pieceBarcode13})
			if err != nil {
				t.Fatalf("Resolve штучного: %v", err)
			}
			if p.ProductID != "p2" || p.Weighted || p.WeightG != nil || p.Qty != 1 {
				t.Fatalf("штучный скан: %+v", p)
			}

			// Весовой: длина та же, вес — по своему правилу.
			w, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: weightedBarcode13})
			if err != nil {
				t.Fatalf("Resolve весового: %v", err)
			}
			if !w.Weighted || !eqInt64Ptr(w.WeightG, tc.wantWeightedG) {
				t.Fatalf("весовой скан: вес %v, want %v", w.WeightG, tc.wantWeightedG)
			}
		})
	}
}

func eqInt64Ptr(got, want *int64) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

// Мусор в поле веса штучного скана приёмку не роняет: поле не вычитывается.
func TestResolvePieceProductBadWeightField(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addPieceProduct(repo)
	repo.supplier.DecodeRules = []string{ruleWeighted13}
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: pieceBarcode13BadW})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.ProductID != "p2" || s.WeightG != nil {
		t.Fatalf("скан: %+v", s)
	}
}

// Ручной вес штучному не приписывается: веса у штучного нет (uom «шт»).
func TestResolvePieceProductIgnoresManualWeight(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addPieceProduct(repo)
	repo.supplier.DecodeRules = []string{ruleWeighted13}
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}
	w := int64(2450)

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: pieceBarcode13, ManualWeightG: &w})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Weighted || s.WeightG != nil {
		t.Fatalf("скан: %+v", s)
	}
}

// Ярлык штучного куска (29 знаков) несёт sentinel 1 г: в скан приёмки вес не идёт.
func TestResolveInternalPieceLabelHasNoWeight(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addPieceProduct(repo)
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}
	raw := pieceCode + "00001" + "29082026" + "29092026" // 29

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: raw})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.InternalCode != pieceCode || s.Weighted || s.WeightG != nil || s.Qty != 1 {
		t.Fatalf("скан: %+v", s)
	}
}

// Приёмка штучного товара правилом с полем веса: вес не уходит ни в остатки,
// ни в статистику весов, отчёт — в штуках.
func TestSavePieceProductWithWeightRule(t *testing.T) {
	uc, stock := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addPieceProduct(repo)
	repo.supplier.DecodeRules = []string{ruleWeighted21}

	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: pieceBarcode21}},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(res.Units) != 1 || res.Units[0].Weighted || res.Units[0].WeightG != 0 {
		t.Fatalf("единица: %+v", res.Units)
	}
	weights, ok := uc.weights.(*stubWeightRecorder)
	if !ok {
		t.Fatal("ожидался stubWeightRecorder")
	}
	if len(weights.recorded) != 0 {
		t.Fatalf("веса штучных писать не должны: %+v", weights.recorded)
	}
	if len(res.Rows) != 1 || res.Rows[0].Weighted || res.Rows[0].Qty != 1 || res.Rows[0].QtyKg != 0 {
		t.Fatalf("отчёт: %+v", res.Rows)
	}
	if len(stock.lots) != 1 || stock.lots[0].Qty != 1 {
		t.Fatalf("лоты: %+v", stock.lots)
	}
}

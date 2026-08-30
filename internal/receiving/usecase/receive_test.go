package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/stock"
)

// --- стабы ---

type stubReceiveRepo struct {
	supplier *domain.Supplier
	barcodes []receiving.BarcodeRef
	catalog  map[string]receiving.ProductRef
	inserted []receiving.WeightRow
	trimmed  map[string]int
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

func (s *stubReceiveRepo) InsertReceivedWeights(_ context.Context, rows []receiving.WeightRow) error {
	s.inserted = append(s.inserted, rows...)
	return nil
}

func (s *stubReceiveRepo) TrimReceivedWeights(_ context.Context, productID string, keep int) error {
	if s.trimmed == nil {
		s.trimmed = make(map[string]int)
	}
	s.trimmed[productID] = keep
	return nil
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
)

func testCacheRepo() *stubReceiveRepo {
	return &stubReceiveRepo{
		supplier: &domain.Supplier{
			ID:             "sup-1",
			DecodeRules:    []string{itemRule},
			BoxDecodeRules: []string{"33-1-6-7-6-13-3-16-8-24-8"},
		},
		barcodes: []receiving.BarcodeRef{
			{ExternalCode: extCode, ProductID: "p1", ProductName: "Говядина охл.", InternalCode: intCode, Weighted: true},
		},
		catalog: map[string]receiving.ProductRef{
			intCode: {ProductID: "p1", InternalCode: intCode, Name: "Говядина охл.", Weighted: true},
		},
	}
}

func newTestReceive() (*ReceivingUseCase, *stubReceiveRepo, *stubStockAccepter) {
	repo := testCacheRepo()
	stock := &stubStockAccepter{}
	return NewReceivingUseCase(repo, stock), repo, stock
}

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// --- тесты ---

func TestGetCache(t *testing.T) {
	uc, _, _ := newTestReceive()

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
}

func TestResolveInternalItem(t *testing.T) {
	uc, _, _ := newTestReceive()
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
	if s.BestBefore == nil || !s.BestBefore.Equal(d(2026, 9, 29)) {
		t.Fatalf("срок: %v", s.BestBefore)
	}
}

func TestResolveInternalBox(t *testing.T) {
	uc, _, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: boxBarcode})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.Kind != receiving.KindBox {
		t.Fatalf("коробка: %+v", s)
	}
	if s.DeclaredQty == nil || *s.DeclaredQty != 10 {
		t.Fatalf("кол-во вложений: %v", s.DeclaredQty)
	}
	if s.DeclaredWeightG == nil || *s.DeclaredWeightG != 25000 {
		t.Fatalf("вес коробки: %v", s.DeclaredWeightG)
	}
}

func TestResolveExternalItem(t *testing.T) {
	uc, _, _ := newTestReceive()
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
	uc, _, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	// Код не заведён у поставщика: правильная длина, но нет в маппинге.
	raw := "999999" + "000250" + "29082026" + "29092026"
	_, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: raw})
	if err == nil {
		t.Fatal("ожидалась ошибка о незаведённом коде")
	}
}

func TestResolveManualProduct(t *testing.T) {
	uc, _, _ := newTestReceive()
	repo := uc.repo.(*stubReceiveRepo)
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
	uc, _, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	_, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: "12345"})
	if !errors.Is(err, receiving.ErrScanUnknown) {
		t.Fatalf("ожидался ErrScanUnknown, получил %v", err)
	}
}

func TestSave(t *testing.T) {
	uc, repo, stock := newTestReceive()

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
	if !stock.lots[0].BestBefore.Equal(d(2026, 9, 29)) {
		t.Fatalf("срок лота: %v", stock.lots[0].BestBefore)
	}
	if len(repo.inserted) != 2 {
		t.Fatalf("весов: %d, want 2", len(repo.inserted))
	}
	if len(res.Rows) != 1 || res.Rows[0].ProductName != "Говядина охл." {
		t.Fatalf("отчёт: %+v", res.Rows)
	}
	if res.Rows[0].QtyKg != 0.5 { // 250 г + 250 г
		t.Fatalf("кг в отчёте: %v", res.Rows[0].QtyKg)
	}
}

func TestSaveBoxMismatch(t *testing.T) {
	uc, _, _ := newTestReceive()

	// Коробка заявляет 10 вложений по 250 г (2.5 кг), внутри — только 2 куска.
	res, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{
				Raw: boxBarcode,
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
	if len(res.Units) != 2 {
		t.Fatalf("единиц из коробки: %d, want 2", len(res.Units))
	}
}

func TestSaveBoxDifferentProducts(t *testing.T) {
	uc, _, _ := newTestReceive()

	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{
				Raw: boxBarcode,
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

func TestSaveMissingBestBefore(t *testing.T) {
	uc, _, _ := newTestReceive()

	// Правило без срока годности и без ручного ввода → ошибка.
	repo := uc.repo.(*stubReceiveRepo)
	repo.supplier.DecodeRules = []string{"28-1-6-7-6-13-8"}
	_, err := uc.Save(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: extBarcode}},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка о неуказанном сроке")
	}
}

package usecase

import (
	"context"
	"errors"
	"testing"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/receiving"
)

// --- стабы ---

type stubBarcodeRepo struct {
	list    []receiving.BarcodeRef
	get     *receiving.BarcodeRef
	count   int
	saved   []string // supplierID:externalCode:productID
	deleted []string
	err     error
}

func (s *stubBarcodeRepo) LoadSupplierBarcodes(context.Context, string) ([]receiving.BarcodeRef, error) {
	return s.list, s.err
}

func (s *stubBarcodeRepo) GetSupplierBarcode(_ context.Context, _, _ string) (*receiving.BarcodeRef, error) {
	return s.get, s.err
}

func (s *stubBarcodeRepo) SaveSupplierBarcode(_ context.Context, supplierID, externalCode, productID string) error {
	s.saved = append(s.saved, supplierID+":"+externalCode+":"+productID)

	return s.err
}

func (s *stubBarcodeRepo) DeleteSupplierBarcode(_ context.Context, supplierID, externalCode string) error {
	s.deleted = append(s.deleted, supplierID+":"+externalCode)

	return s.err
}

func (s *stubBarcodeRepo) CountSupplierProductCodes(context.Context, string, string) (int, error) {
	return s.count, s.err
}

type stubSupplierReader struct {
	supplier *domain.Supplier
	err      error
}

func (s *stubSupplierReader) GetSupplier(context.Context, string) (*domain.Supplier, error) {
	return s.supplier, s.err
}

type stubCatalogReader struct {
	product *domain.Product
	err     error
}

func (s *stubCatalogReader) GetProduct(context.Context, string) (*domain.Product, error) {
	return s.product, s.err
}

type stubWikiRef struct {
	ensured     []string // productID:name:avgWeight
	addedTags   []string // title:tag
	removedTags []string // title:tag
	err         error
}

func (s *stubWikiRef) EnsureProductPage(_ context.Context, productID, name, averageWeight string) error {
	s.ensured = append(s.ensured, productID+":"+name+":"+averageWeight)

	return s.err
}

func (s *stubWikiRef) AddTagToPage(_ context.Context, title, tag string) error {
	s.addedTags = append(s.addedTags, title+":"+tag)

	return s.err
}

func (s *stubWikiRef) RemoveTagFromPage(_ context.Context, title, tag string) error {
	s.removedTags = append(s.removedTags, title+":"+tag)

	return s.err
}

// --- фикстуры ---

const (
	testSupplierID = "supplier-uuid-1"
	testProductID  = "product-uuid-1"
)

func newEditor(repo *stubBarcodeRepo, suppliers *stubSupplierReader, catalog *stubCatalogReader, wiki *stubWikiRef) *BarcodeEditor {
	if repo == nil {
		repo = &stubBarcodeRepo{}
	}
	if suppliers == nil {
		suppliers = &stubSupplierReader{supplier: &domain.Supplier{ID: testSupplierID, Name: "Мираторг"}}
	}
	if catalog == nil {
		avg := 2.5
		catalog = &stubCatalogReader{product: &domain.Product{ID: testProductID, Name: "Грудной отруб", AverageWeight: &avg}}
	}
	if wiki == nil {
		wiki = &stubWikiRef{}
	}

	return NewBarcodeEditor(repo, suppliers, catalog, wiki)
}

// --- тесты Add ---

func TestBarcodeEditor_AddSuccess(t *testing.T) {
	repo, wiki := &stubBarcodeRepo{}, &stubWikiRef{}
	uc := newEditor(repo, nil, nil, wiki)

	if err := uc.Add(context.Background(), testSupplierID, "4607000000001", testProductID); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(repo.saved) != 1 || repo.saved[0] != testSupplierID+":4607000000001:"+testProductID {
		t.Fatalf("связка сохранена неверно: %v", repo.saved)
	}
	if len(wiki.ensured) != 1 || wiki.ensured[0] != testProductID+":Грудной отруб:2.5" {
		t.Fatalf("EnsureProductPage вызван неверно: %v", wiki.ensured)
	}
	wantTags := []string{"Грудной отруб:Мираторг", "Мираторг:Грудной отруб"}
	if !equalStrings(wiki.addedTags, wantTags) {
		t.Fatalf("теги добавлены неверно: %v, want %v", wiki.addedTags, wantTags)
	}
}

func TestBarcodeEditor_AddNoAvgWeight(t *testing.T) {
	wiki := &stubWikiRef{}
	uc := newEditor(nil, nil, &stubCatalogReader{product: &domain.Product{ID: testProductID, Name: "Без веса"}}, wiki)

	if err := uc.Add(context.Background(), testSupplierID, "123", testProductID); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(wiki.ensured) != 1 || wiki.ensured[0] != testProductID+":Без веса:" {
		t.Fatalf("пустой средний вес должен передаваться пустой строкой: %v", wiki.ensured)
	}
}

func TestBarcodeEditor_AddValidation(t *testing.T) {
	repo := &stubBarcodeRepo{}
	uc := newEditor(repo, nil, nil, nil)

	if err := uc.Add(context.Background(), "", "123", testProductID); err == nil {
		t.Fatal("пустой поставщик должен давать ошибку")
	}
	if err := uc.Add(context.Background(), testSupplierID, "   ", testProductID); err == nil {
		t.Fatal("пустой код должен давать ошибку")
	}
	if err := uc.Add(context.Background(), testSupplierID, "123", ""); err == nil {
		t.Fatal("пустой товар должен давать ошибку")
	}
	if len(repo.saved) != 0 {
		t.Fatal("при ошибке валидации связка не должна сохраняться")
	}
}

func TestBarcodeEditor_AddSupplierNotFound(t *testing.T) {
	repo := &stubBarcodeRepo{}
	uc := newEditor(repo, &stubSupplierReader{err: domain.ErrSupplierNotFound}, nil, nil)

	err := uc.Add(context.Background(), testSupplierID, "123", testProductID)
	if !errors.Is(err, domain.ErrSupplierNotFound) {
		t.Fatalf("ожидался ErrSupplierNotFound, получил %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatal("связка не должна сохраняться при отсутствии поставщика")
	}
}

func TestBarcodeEditor_AddProductNotFound(t *testing.T) {
	repo := &stubBarcodeRepo{}
	uc := newEditor(repo, nil, &stubCatalogReader{err: domain.ErrProductNotFound}, nil)

	err := uc.Add(context.Background(), testSupplierID, "123", testProductID)
	if !errors.Is(err, domain.ErrProductNotFound) {
		t.Fatalf("ожидался ErrProductNotFound, получил %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatal("связка не должна сохраняться при отсутствии товара")
	}
}

// --- тесты Remove ---

func TestBarcodeEditor_RemoveLastCode(t *testing.T) {
	repo := &stubBarcodeRepo{
		get:   &receiving.BarcodeRef{ExternalCode: "4607000000001", ProductID: testProductID, ProductName: "Грудной отруб"},
		count: 0,
	}
	wiki := &stubWikiRef{}
	uc := newEditor(repo, nil, nil, wiki)

	if err := uc.Remove(context.Background(), testSupplierID, "4607000000001"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != testSupplierID+":4607000000001" {
		t.Fatalf("связка удалена неверно: %v", repo.deleted)
	}
	wantTags := []string{"Грудной отруб:Мираторг", "Мираторг:Грудной отруб"}
	if !equalStrings(wiki.removedTags, wantTags) {
		t.Fatalf("теги сняты неверно: %v, want %v", wiki.removedTags, wantTags)
	}
}

func TestBarcodeEditor_RemoveKeepsTagsIfMoreCodes(t *testing.T) {
	repo := &stubBarcodeRepo{
		get:   &receiving.BarcodeRef{ExternalCode: "4607000000001", ProductID: testProductID, ProductName: "Грудной отруб"},
		count: 2,
	}
	wiki := &stubWikiRef{}
	uc := newEditor(repo, nil, nil, wiki)

	if err := uc.Remove(context.Background(), testSupplierID, "4607000000001"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(wiki.removedTags) != 0 {
		t.Fatalf("при оставшихся кодах теги снимать нельзя: %v", wiki.removedTags)
	}
}

func TestBarcodeEditor_RemoveNotFound(t *testing.T) {
	repo := &stubBarcodeRepo{get: nil}
	uc := newEditor(repo, nil, nil, nil)

	if err := uc.Remove(context.Background(), testSupplierID, "123"); err == nil {
		t.Fatal("удаление несуществующей связки должно давать ошибку")
	}
	if len(repo.deleted) != 0 {
		t.Fatal("несуществующая связка не должна удаляться")
	}
}

// --- тест List ---

func TestBarcodeEditor_List(t *testing.T) {
	want := []receiving.BarcodeRef{{ExternalCode: "123", ProductID: testProductID, ProductName: "Грудной отруб"}}
	repo := &stubBarcodeRepo{list: want}
	uc := newEditor(repo, nil, nil, nil)

	got, err := uc.List(context.Background(), testSupplierID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ExternalCode != "123" {
		t.Fatalf("List вернул неверные данные: %+v", got)
	}
}

func TestBarcodeEditor_ListEmptySupplier(t *testing.T) {
	uc := newEditor(nil, nil, nil, nil)

	if _, err := uc.List(context.Background(), "  "); err == nil {
		t.Fatal("пустой поставщик должен давать ошибку")
	}
}

// equalStrings — сравнение срезов строк без учёта порядка (для тегов).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		if seen[s] == 0 {
			return false
		}
		seen[s]--
	}

	return true
}

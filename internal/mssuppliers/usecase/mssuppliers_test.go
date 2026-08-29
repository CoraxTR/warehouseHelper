package usecase

import (
	"context"
	"errors"
	"testing"

	"warehouseHelper/internal/domain"
)

// fakeRepo — in-memory хранилище поставщиков для тестов.
type fakeRepo struct {
	suppliers map[string]domain.Supplier
	deleted   []string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{suppliers: make(map[string]domain.Supplier)}
}

func (f *fakeRepo) ListSuppliers(_ context.Context) ([]domain.Supplier, error) {
	out := make([]domain.Supplier, 0, len(f.suppliers))
	for _, s := range f.suppliers {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeRepo) GetSupplier(_ context.Context, id string) (*domain.Supplier, error) {
	s, ok := f.suppliers[id]
	if !ok {
		//nolint:nilnil // стаб: не найдено
		return nil, nil
	}
	return &s, nil
}

func (f *fakeRepo) SaveSupplier(_ context.Context, s *domain.Supplier) error {
	f.suppliers[s.ID] = *s
	return nil
}

func (f *fakeRepo) DeleteSupplier(_ context.Context, id string) error {
	if _, ok := f.suppliers[id]; !ok {
		return domain.ErrSupplierNotFound
	}
	delete(f.suppliers, id)
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeMS — фейк клиента МойСклад.
type fakeMS struct {
	names map[string]string
	err   error
}

func (f *fakeMS) FetchCounterpartyName(_ context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	name, ok := f.names[id]
	if !ok {
		return "", errors.New("counterparty not found")
	}
	return name, nil
}

// fakeWiki — фейк синка вики: запоминает вызовы, умеет падать.
type fakeWiki struct {
	syncErr   error
	calls     int
	lastID    string
	lastName  string
	lastOrder []int
	lastDeliv []int
}

func (f *fakeWiki) SyncSupplierPage(_ context.Context, supplierID, name string, orderDays, deliveryDays []int) error {
	f.calls++
	f.lastID = supplierID
	f.lastName = name
	f.lastOrder = orderDays
	f.lastDeliv = deliveryDays

	return f.syncErr
}

const testUUID = "c2f28fc8-a154-11f1-0a80-161200147fdc"

// testSupplierName — имя контрагента в фейках МС (goconst).
const testSupplierName = "ООО Тест"

func newUC(repo *fakeRepo, ms *fakeMS, wiki *fakeWiki) *MSSuppliersUseCase {
	if ms == nil {
		ms = &fakeMS{names: map[string]string{testUUID: testSupplierName}}
	}
	if wiki == nil {
		wiki = &fakeWiki{}
	}
	return NewMSSuppliersUseCase(repo, ms, wiki)
}

func validSupplier() *domain.Supplier {
	return &domain.Supplier{
		ID:               testUUID,
		DecodeRules:      []string{"28-1-6-7-6-13-8-21-8"},
		OrderDays:        []int16{3, 1, 3}, // дубль убирается
		DeliveryDays:     []int16{5},
		SpecialOrderDays: []int16{},
	}
}

func TestExtractCounterpartyID(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "полная ссылка на контрагента", raw: "https://online.moysklad.ru/app/#counterparty/edit?id=" + testUUID, want: testUUID},
		{name: "ссылка на заказ поставщика (другой фрагмент)", raw: "https://online.moysklad.ru/app/#purchaseorder/edit?id=" + testUUID, want: testUUID},
		{name: "голый uuid", raw: testUUID, want: testUUID},
		{name: "uuid с пробелами", raw: "  " + testUUID + "  ", want: testUUID},
		{name: "пустая строка", raw: "", wantErr: true},
		{name: "мусор", raw: "https://example.com/not-a-link", wantErr: true},
		{name: "uuid не в id=", raw: "https://online.moysklad.ru/app/#counterparty?other=" + testUUID, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCounterpartyID(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractCounterpartyID(%q) = %q, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractCounterpartyID(%q) error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ExtractCounterpartyID(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestCreate_HappyPath(t *testing.T) {
	repo := newFakeRepo()
	uc := newUC(repo, nil, nil)

	s := validSupplier()
	if err := uc.Create(context.Background(), s); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	got, err := uc.Get(context.Background(), testUUID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Name != testSupplierName {
		t.Fatalf("Name = %q, want %q (из МС)", got.Name, testSupplierName)
	}
	if len(got.OrderDays) != 2 || got.OrderDays[0] != 1 || got.OrderDays[1] != 3 {
		t.Fatalf("OrderDays = %v, want [1 3] (дубли убраны, сортировка)", got.OrderDays)
	}
}

func TestCreate_DuplicateID(t *testing.T) {
	repo := newFakeRepo()
	uc := newUC(repo, nil, nil)

	first := validSupplier()
	if err := uc.Create(context.Background(), first); err != nil {
		t.Fatalf("first Create error: %v", err)
	}

	second := validSupplier()
	err := uc.Create(context.Background(), second)
	if !errors.Is(err, domain.ErrSupplierExists) {
		t.Fatalf("second Create = %v, want ErrSupplierExists", err)
	}
}

func TestCreate_MSError_LeavesFormData(t *testing.T) {
	repo := newFakeRepo()
	ms := &fakeMS{err: errors.New("WAF 415")}
	uc := newUC(repo, ms, nil)

	s := validSupplier()
	err := uc.Create(context.Background(), s)
	if !errors.Is(err, ErrCounterpartyNameFetch) {
		t.Fatalf("Create = %v, want ErrCounterpartyNameFetch", err)
	}
	if _, ok := repo.suppliers[testUUID]; ok {
		t.Fatal("supplier saved despite MS error")
	}
	if s.Name != "" {
		t.Fatalf("Name = %q, want empty (имя не подставилось)", s.Name)
	}
}

func TestCreate_ValidationErrors(t *testing.T) {
	uc := newUC(newFakeRepo(), nil, nil)

	tests := []struct {
		name string
		mut  func(s *domain.Supplier)
	}{
		{name: "пустой id", mut: func(s *domain.Supplier) { s.ID = "" }},
		{name: "id не uuid", mut: func(s *domain.Supplier) { s.ID = "not-a-uuid" }},
		{name: "день вне диапазона", mut: func(s *domain.Supplier) { s.OrderDays = []int16{0} }},
		{name: "день 8", mut: func(s *domain.Supplier) { s.DeliveryDays = []int16{8} }},
		{name: "кривое правило штрихкода", mut: func(s *domain.Supplier) { s.DecodeRules = []string{"28-1-6-abc"} }},
		{name: "отрицательная задержка", mut: func(s *domain.Supplier) { d := int16(-1); s.DelayDays = &d }},
		{name: "отрицательная сумма", mut: func(s *domain.Supplier) { m := int64(-100); s.MinOrderAmount = &m }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSupplier()
			tt.mut(s)
			if err := uc.Create(context.Background(), s); err == nil {
				t.Fatal("Create = nil, want validation error")
			}
			if len(repoSuppliers(uc)) != 0 {
				t.Fatal("supplier saved despite validation error")
			}
		})
	}
}

// repoSuppliers возвращает текущее число сохранённых поставщиков (через List).
func repoSuppliers(uc *MSSuppliersUseCase) []domain.Supplier {
	list, _ := uc.List(context.Background())
	return list
}

func TestUpdate_RefetchesName(t *testing.T) {
	repo := newFakeRepo()
	ms := &fakeMS{names: map[string]string{testUUID: testSupplierName}}
	uc := newUC(repo, ms, nil)

	if err := uc.Create(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	// В МС имя поменялось — сохранение (даже без изменения полей) должно его подтянуть.
	ms.names[testUUID] = "ООО Тест (новое)"
	s := validSupplier()
	s.DecodeRules = nil
	if err := uc.Update(context.Background(), s); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	got, _ := uc.Get(context.Background(), testUUID)
	if got.Name != "ООО Тест (новое)" {
		t.Fatalf("Name = %q, want %q (перезапрошено из МС)", got.Name, "ООО Тест (новое)")
	}
}

func TestUpdate_MSError(t *testing.T) {
	repo := newFakeRepo()
	uc := newUC(repo, nil, nil)
	if err := uc.Create(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	uc2 := newUC(repo, &fakeMS{err: errors.New("boom")}, nil)
	s := validSupplier()
	err := uc2.Update(context.Background(), s)
	if !errors.Is(err, ErrCounterpartyNameFetch) {
		t.Fatalf("Update = %v, want ErrCounterpartyNameFetch", err)
	}
	got, _ := uc.Get(context.Background(), testUUID)
	if got.Name != testSupplierName {
		t.Fatalf("Name = %q, want старое имя (обновление не прошло)", got.Name)
	}
}

func TestDelete(t *testing.T) {
	repo := newFakeRepo()
	uc := newUC(repo, nil, nil)

	if err := uc.Create(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if err := uc.Delete(context.Background(), testUUID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if _, err := uc.Get(context.Background(), testUUID); err != nil {
		t.Fatalf("Get after delete error: %v", err)
	}
	if len(repoSuppliers(uc)) != 0 {
		t.Fatal("supplier still listed after delete")
	}

	err := uc.Delete(context.Background(), testUUID)
	if !errors.Is(err, domain.ErrSupplierNotFound) {
		t.Fatalf("second Delete = %v, want ErrSupplierNotFound", err)
	}
}

func TestNormalizeDays(t *testing.T) {
	got, err := normalizeDays("тест", []int16{5, 1, 5, 3})
	if err != nil {
		t.Fatalf("normalizeDays error: %v", err)
	}
	want := []int16{1, 3, 5}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("normalizeDays = %v, want %v", got, want)
	}

	if _, err := normalizeDays("тест", []int16{7, 0}); err == nil {
		t.Fatal("normalizeDays([7 0]) = nil, want error")
	}
}

func TestCreate_SyncsWiki(t *testing.T) {
	repo := newFakeRepo()
	wiki := &fakeWiki{}
	uc := newUC(repo, nil, wiki)

	if err := uc.Create(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if wiki.calls != 1 {
		t.Fatalf("SyncSupplierPage вызван %d раз, want 1", wiki.calls)
	}
	if wiki.lastID != testUUID || wiki.lastName != testSupplierName {
		t.Fatalf("синк: id=%q name=%q, want id=%q name=%q (имя из МС)", wiki.lastID, wiki.lastName, testUUID, testSupplierName)
	}
	if len(wiki.lastOrder) != 2 || wiki.lastOrder[0] != 1 || wiki.lastOrder[1] != 3 {
		t.Fatalf("orderDays синка = %v, want [1 3] (нормализованные)", wiki.lastOrder)
	}
	if len(wiki.lastDeliv) != 1 || wiki.lastDeliv[0] != 5 {
		t.Fatalf("deliveryDays синка = %v, want [5]", wiki.lastDeliv)
	}
}

func TestUpdate_SyncsWiki(t *testing.T) {
	repo := newFakeRepo()
	wiki := &fakeWiki{}
	uc := newUC(repo, nil, wiki)

	if err := uc.Create(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if err := uc.Update(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if wiki.calls != 2 {
		t.Fatalf("SyncSupplierPage вызван %d раз, want 2 (create + update)", wiki.calls)
	}
}

func TestCreate_WikiSyncError_SavedAnyway(t *testing.T) {
	repo := newFakeRepo()
	wiki := &fakeWiki{syncErr: errors.New("заголовок занят")}
	uc := newUC(repo, nil, wiki)

	err := uc.Create(context.Background(), validSupplier())
	if !errors.Is(err, ErrWikiSync) {
		t.Fatalf("Create = %v, want ErrWikiSync", err)
	}
	// Поставщик сохранён несмотря на ошибку синка (контуры без кросс-транзакций).
	if _, ok := repo.suppliers[testUUID]; !ok {
		t.Fatal("supplier должен быть сохранён, даже если синк вики упал")
	}
}

func TestDelete_DoesNotSyncWiki(t *testing.T) {
	repo := newFakeRepo()
	wiki := &fakeWiki{}
	uc := newUC(repo, nil, wiki)

	if err := uc.Create(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if err := uc.Delete(context.Background(), testUUID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if wiki.calls != 1 {
		t.Fatalf("SyncSupplierPage вызван %d раз, want 1 (только create; удаление страницу не трогает)", wiki.calls)
	}
}

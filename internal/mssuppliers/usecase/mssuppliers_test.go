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

func (f *fakeRepo) ListSuppliers(ctx context.Context) ([]domain.Supplier, error) {
	out := make([]domain.Supplier, 0, len(f.suppliers))
	for _, s := range f.suppliers {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeRepo) GetSupplier(ctx context.Context, id string) (*domain.Supplier, error) {
	s, ok := f.suppliers[id]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (f *fakeRepo) SaveSupplier(ctx context.Context, s *domain.Supplier) error {
	f.suppliers[s.ID] = *s
	return nil
}

func (f *fakeRepo) DeleteSupplier(ctx context.Context, id string) error {
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

func (f *fakeMS) FetchCounterpartyName(ctx context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	name, ok := f.names[id]
	if !ok {
		return "", errors.New("counterparty not found")
	}
	return name, nil
}

const testUUID = "c2f28fc8-a154-11f1-0a80-161200147fdc"

func newUC(repo *fakeRepo, ms *fakeMS) *MSSuppliersUseCase {
	if ms == nil {
		ms = &fakeMS{names: map[string]string{testUUID: "ООО Тест"}}
	}
	return NewMSSuppliersUseCase(repo, ms)
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
	uc := newUC(repo, nil)

	s := validSupplier()
	if err := uc.Create(context.Background(), s); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	got, err := uc.Get(context.Background(), testUUID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Name != "ООО Тест" {
		t.Fatalf("Name = %q, want %q (из МС)", got.Name, "ООО Тест")
	}
	if len(got.OrderDays) != 2 || got.OrderDays[0] != 1 || got.OrderDays[1] != 3 {
		t.Fatalf("OrderDays = %v, want [1 3] (дубли убраны, сортировка)", got.OrderDays)
	}
}

func TestCreate_DuplicateID(t *testing.T) {
	repo := newFakeRepo()
	uc := newUC(repo, nil)

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
	uc := newUC(repo, ms)

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
	uc := newUC(newFakeRepo(), nil)

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
	ms := &fakeMS{names: map[string]string{testUUID: "ООО Тест"}}
	uc := newUC(repo, ms)

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
	uc := newUC(repo, nil)
	if err := uc.Create(context.Background(), validSupplier()); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	uc2 := newUC(repo, &fakeMS{err: errors.New("boom")})
	s := validSupplier()
	err := uc2.Update(context.Background(), s)
	if !errors.Is(err, ErrCounterpartyNameFetch) {
		t.Fatalf("Update = %v, want ErrCounterpartyNameFetch", err)
	}
	got, _ := uc.Get(context.Background(), testUUID)
	if got.Name != "ООО Тест" {
		t.Fatalf("Name = %q, want старое имя (обновление не прошло)", got.Name)
	}
}

func TestDelete(t *testing.T) {
	repo := newFakeRepo()
	uc := newUC(repo, nil)

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

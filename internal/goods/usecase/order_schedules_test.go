package usecase

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"warehouseHelper/internal/domain"
)

// stubOrderSchedules — хранилище вариантов заказа для тестов каталога.
type stubOrderSchedules struct {
	list          []domain.OrderSchedule
	saved         []domain.OrderSchedule
	savedProduct  string
	listErr       error
	saveErr       error
	saveCalls     int
	saveArgsEmpty bool
}

func (s *stubOrderSchedules) ListProductOrderSchedules(_ context.Context, _ string) ([]domain.OrderSchedule, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list, nil
}

func (s *stubOrderSchedules) ReplaceProductOrderSchedules(_ context.Context, productID string, list []domain.OrderSchedule) error {
	s.saveCalls++
	s.savedProduct = productID
	s.saved = list
	s.saveArgsEmpty = list == nil
	return s.saveErr
}

// stubSupplierDirectory — справочник поставщиков (модуль mssuppliers) для формы.
type stubSupplierDirectory struct {
	list []domain.Supplier
	err  error
}

func (s *stubSupplierDirectory) List(_ context.Context) ([]domain.Supplier, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.list, nil
}

func newSchedulesUC(t *testing.T, repo *stubProductsRepo, sched *stubOrderSchedules, sup *stubSupplierDirectory) *GoodsUseCase {
	t.Helper()
	uc := NewGoodsUseCase(nil, nil, repo, nil)
	uc.SetOrderScheduleStore(sched, sup)
	return uc
}

func schedulesFixture() (*stubProductsRepo, *stubOrderSchedules, *stubSupplierDirectory) {
	repo := &stubProductsRepo{get: &domain.Product{ID: "p1", Name: "Стейк"}}
	sched := &stubOrderSchedules{}
	sup := &stubSupplierDirectory{list: []domain.Supplier{
		{ID: "sup-1", Name: "Мираторг"},
		{ID: "sup-2", Name: "Агро"},
	}}
	return repo, sched, sup
}

func TestSaveOrderSchedules_Normalizes(t *testing.T) {
	repo, sched, sup := schedulesFixture()
	uc := newSchedulesUC(t, repo, sched, sup)

	delay := int16(2)
	list := []domain.OrderSchedule{{
		SupplierID:   "sup-1",
		Name:         "  Общий  ",
		OrderDays:    []int16{3, 1, 3},
		DeliveryDays: []int16{5, 4, 5},
		DelayDays:    &delay,
	}}

	if err := uc.SaveOrderSchedules(context.Background(), "p1", list); err != nil {
		t.Fatalf("SaveOrderSchedules: %v", err)
	}
	if sched.savedProduct != "p1" {
		t.Fatalf("productID = %q, want p1", sched.savedProduct)
	}
	if len(sched.saved) != 1 {
		t.Fatalf("сохранено %d вариантов, want 1", len(sched.saved))
	}
	got := sched.saved[0]
	if got.Name != "Общий" {
		t.Fatalf("Name = %q, want %q (обрезан)", got.Name, "Общий")
	}
	if !reflect.DeepEqual(got.OrderDays, []int16{1, 3}) {
		t.Fatalf("OrderDays = %v, want [1 3] (дубли убраны, по возрастанию)", got.OrderDays)
	}
	if !reflect.DeepEqual(got.DeliveryDays, []int16{4, 5}) {
		t.Fatalf("DeliveryDays = %v, want [4 5]", got.DeliveryDays)
	}
	if got.DelayDays == nil || *got.DelayDays != 2 {
		t.Fatalf("DelayDays = %v, want 2", got.DelayDays)
	}
}

func TestSaveOrderSchedules_EmptyListClears(t *testing.T) {
	repo, sched, sup := schedulesFixture()
	uc := newSchedulesUC(t, repo, sched, sup)

	if err := uc.SaveOrderSchedules(context.Background(), "p1", nil); err != nil {
		t.Fatalf("SaveOrderSchedules: %v", err)
	}
	if sched.saveCalls != 1 || len(sched.saved) != 0 {
		t.Fatalf("ожидалась одна запись пустого набора, получил %+v", sched.saved)
	}
}

func TestSaveOrderSchedules_Validation(t *testing.T) {
	negDelay := int16(-1)
	tests := []struct {
		name    string
		item    domain.OrderSchedule
		wantErr string
	}{
		{
			name:    "пустое название",
			item:    domain.OrderSchedule{SupplierID: "sup-1", Name: "  ", OrderDays: []int16{1}},
			wantErr: "не заполнено название",
		},
		{
			name:    "нет дней заказа",
			item:    domain.OrderSchedule{SupplierID: "sup-1", Name: "Общий"},
			wantErr: "день заказа",
		},
		{
			name:    "день вне диапазона",
			item:    domain.OrderSchedule{SupplierID: "sup-1", Name: "Общий", OrderDays: []int16{1, 8}},
			wantErr: "день 8 вне диапазона",
		},
		{
			name:    "день доставки вне диапазона",
			item:    domain.OrderSchedule{SupplierID: "sup-1", Name: "Общий", OrderDays: []int16{1}, DeliveryDays: []int16{0}},
			wantErr: "день 0 вне диапазона",
		},
		{
			name:    "поставщик не из справочника",
			item:    domain.OrderSchedule{SupplierID: "sup-x", Name: "Общий", OrderDays: []int16{1}},
			wantErr: "не из справочника",
		},
		{
			name:    "пустой поставщик",
			item:    domain.OrderSchedule{Name: "Общий", OrderDays: []int16{1}},
			wantErr: "не выбран поставщик",
		},
		{
			name:    "отрицательная задержка",
			item:    domain.OrderSchedule{SupplierID: "sup-1", Name: "Общий", OrderDays: []int16{1}, DelayDays: &negDelay},
			wantErr: "не может быть отрицательным",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, sched, sup := schedulesFixture()
			uc := newSchedulesUC(t, repo, sched, sup)

			err := uc.SaveOrderSchedules(context.Background(), "p1", []domain.OrderSchedule{tt.item})
			if err == nil {
				t.Fatal("SaveOrderSchedules = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ошибка %q, want подстроку %q", err, tt.wantErr)
			}
			if sched.saveCalls != 0 {
				t.Fatal("варианты сохранены несмотря на ошибку валидации")
			}
		})
	}
}

func TestSaveOrderSchedules_DuplicateVariant(t *testing.T) {
	repo, sched, sup := schedulesFixture()
	uc := newSchedulesUC(t, repo, sched, sup)

	list := []domain.OrderSchedule{
		{SupplierID: "sup-1", Name: "Общий", OrderDays: []int16{1}},
		{SupplierID: "sup-1", Name: " общий ", OrderDays: []int16{4}},
	}
	err := uc.SaveOrderSchedules(context.Background(), "p1", list)
	if err == nil {
		t.Fatal("SaveOrderSchedules = nil, want дубль-ошибку")
	}
	if !strings.Contains(err.Error(), "уже есть") {
		t.Fatalf("ошибка %q, want про дубль варианта", err)
	}
	if sched.saveCalls != 0 {
		t.Fatal("варианты сохранены несмотря на дубль")
	}
}

func TestSaveOrderSchedules_ProductNotFound(t *testing.T) {
	repo := &stubProductsRepo{} // GetProduct → (nil, nil)
	sched := &stubOrderSchedules{}
	sup := &stubSupplierDirectory{list: []domain.Supplier{{ID: "sup-1", Name: "Мираторг"}}}
	uc := newSchedulesUC(t, repo, sched, sup)

	err := uc.SaveOrderSchedules(context.Background(), "p-x", []domain.OrderSchedule{
		{SupplierID: "sup-1", Name: "Общий", OrderDays: []int16{1}},
	})
	if !errors.Is(err, domain.ErrProductNotFound) {
		t.Fatalf("SaveOrderSchedules = %v, want ErrProductNotFound", err)
	}
}

func TestSaveOrderSchedules_NotWired(t *testing.T) {
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(nil, nil, repo, nil)

	err := uc.SaveOrderSchedules(context.Background(), "p1", nil)
	if err == nil {
		t.Fatal("SaveOrderSchedules без хранилища = nil, want ошибку")
	}
	if _, _, err := uc.ListOrderSchedules(context.Background(), "p1"); err == nil {
		t.Fatal("ListOrderSchedules без хранилища = nil, want ошибку")
	}
}

func TestListOrderSchedules(t *testing.T) {
	repo, sched, sup := schedulesFixture()
	sched.list = []domain.OrderSchedule{
		{ID: 1, ProductID: "p1", SupplierID: "sup-1", SupplierName: "Мираторг", Name: "Общий", OrderDays: []int16{1, 4}},
	}
	uc := newSchedulesUC(t, repo, sched, sup)

	list, suppliers, err := uc.ListOrderSchedules(context.Background(), "p1")
	if err != nil {
		t.Fatalf("ListOrderSchedules: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Общий" {
		t.Fatalf("варианты = %+v, want один «Общий»", list)
	}
	if len(suppliers) != 2 {
		t.Fatalf("поставщиков = %d, want 2 (для выпадающего списка)", len(suppliers))
	}
}

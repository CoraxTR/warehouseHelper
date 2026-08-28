package usecase

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"warehouseHelper/internal/domain"
)

// stubWikiRepo — заглушка WikiRepository для тестов: возвращает
// предзаданные значения и запоминает последние аргументы вызовов.
type stubWikiRepo struct {
	listIndex    []domain.WikiIndexEntry
	page         *domain.WikiPage
	backlinks    []string
	saveErr      error
	getErr       error
	backlinksErr error
	photoData    []byte
	photoContent string
	photoErr     error
	tagCloud     []domain.WikiTagCount
	linkTargets  map[string]string
	titles       []string
	// синк страниц поставщиков:
	linkedPage            *domain.WikiPage
	unlinkedPage          *domain.WikiPage
	createPageErr         error
	updatePageErr         error
	createdPage           *domain.WikiPage
	lastUpdatedPageID     int64
	lastUpdatedSupplierID string
	lastUpdatedTitle      string
	lastUpdatedOrderDays  []int
	lastUpdatedDelivDays  []int

	saveCalled        bool
	resolveCalled     bool
	lastSavedPage     *domain.WikiPage
	lastCurrentTitle  string
	lastSavedPhoto    *domain.PhotoUpload
	lastGetPageTitle  string
	lastResolveTitles []string
}

// Компиляционная проверка: стаб реализует WikiRepository.
var _ WikiRepository = (*stubWikiRepo)(nil)

func (s *stubWikiRepo) ListIndex(context.Context, string, []string, domain.PageType) ([]domain.WikiIndexEntry, error) {
	return s.listIndex, nil
}

func (s *stubWikiRepo) GetPage(_ context.Context, title string) (*domain.WikiPage, error) {
	s.lastGetPageTitle = title

	return s.page, s.getErr
}

func (s *stubWikiRepo) GetBacklinks(context.Context, string) ([]string, error) {
	return s.backlinks, s.backlinksErr
}

func (s *stubWikiRepo) SavePage(_ context.Context, page *domain.WikiPage, currentTitle string, photo *domain.PhotoUpload) error {
	s.saveCalled = true
	s.lastSavedPage = page
	s.lastCurrentTitle = currentTitle
	s.lastSavedPhoto = photo

	return s.saveErr
}

func (s *stubWikiRepo) DeletePage(context.Context, string) error {
	return nil
}

func (s *stubWikiRepo) RemovePhoto(context.Context, string) error {
	return nil
}

func (s *stubWikiRepo) GetPhoto(context.Context, string) ([]byte, string, error) {
	return s.photoData, s.photoContent, s.photoErr
}

func (s *stubWikiRepo) TagCloud(context.Context) ([]domain.WikiTagCount, error) {
	return s.tagCloud, nil
}

func (s *stubWikiRepo) ResolveLinkTargets(_ context.Context, rawTitles []string) (map[string]string, error) {
	s.resolveCalled = true
	s.lastResolveTitles = rawTitles

	return s.linkTargets, nil
}

func (s *stubWikiRepo) ListPageTitlesByType(context.Context, domain.PageType) ([]string, error) {
	return s.titles, nil
}

func (s *stubWikiRepo) GetPageBySupplierID(_ context.Context, _ string) (*domain.WikiPage, error) {
	return s.linkedPage, nil
}

func (s *stubWikiRepo) GetUnlinkedSupplierPageByTitle(_ context.Context, _ string) (*domain.WikiPage, error) {
	return s.unlinkedPage, nil
}

func (s *stubWikiRepo) CreateSupplierPage(_ context.Context, page *domain.WikiPage) error {
	s.createdPage = page

	return s.createPageErr
}

func (s *stubWikiRepo) UpdateSupplierPage(_ context.Context, pageID int64, supplierID, title string, orderDays, deliveryDays []int) error {
	s.lastUpdatedPageID = pageID
	s.lastUpdatedSupplierID = supplierID
	s.lastUpdatedTitle = title
	s.lastUpdatedOrderDays = orderDays
	s.lastUpdatedDelivDays = deliveryDays

	return s.updatePageErr
}

func TestWikiUseCaseSavePageValidation(t *testing.T) {
	// 51 уникальных названий продуктов — превышение лимита в 50 элементов.
	products51 := make([]string, 51)
	for i := range products51 {
		products51[i] = fmt.Sprintf("п%d", i)
	}

	tests := []struct {
		name      string
		current   string
		page      *domain.WikiPage
		photo     *domain.PhotoUpload
		wantError string // ожидаемая подстрока текста ошибки
	}{
		{
			name:      "пустой заголовок",
			page:      &domain.WikiPage{Type: domain.PageTypeSupplier, Title: "   "},
			wantError: "заголовок не может быть пустым",
		},
		{
			name:      "неизвестный тип страницы",
			page:      &domain.WikiPage{Type: domain.PageType("bogus"), Title: "Страница"},
			wantError: "неизвестный тип страницы",
		},
		{
			name: "контакт без телефона, email и сайта",
			page: &domain.WikiPage{
				Type:     domain.PageTypeSupplier,
				Title:    "Поставщик",
				Contacts: []domain.Contact{{Name: "Иван"}},
			},
			wantError: "заполните телефон, email или сайт в контакте 1",
		},
		{
			name: "неверный день недели",
			page: &domain.WikiPage{
				Type:      domain.PageTypeSupplier,
				Title:     "Поставщик",
				OrderDays: []int{1, 8},
			},
			wantError: "неверный день недели: 8",
		},
		{
			name: "слишком длинный средний вес",
			page: &domain.WikiPage{
				Type:          domain.PageTypeSupplier,
				Title:         "Поставщик",
				AverageWeight: strings.Repeat("0", 101),
			},
			wantError: "средний вес",
		},
		{
			name: "слишком много продуктов",
			page: &domain.WikiPage{
				Type:     domain.PageTypeSupplier,
				Title:    "Поставщик",
				Products: products51,
			},
			wantError: "слишком много продуктов",
		},
		{
			name: "продукт слишком длинный",
			page: &domain.WikiPage{
				Type:     domain.PageTypeSupplier,
				Title:    "Поставщик",
				Products: []string{strings.Repeat("П", 101)},
			},
			wantError: "элемент продуктов слишком длинный",
		},
		{
			name: "фото больше 5 МБ",
			page: &domain.WikiPage{
				Type:  domain.PageTypeSupplier,
				Title: "Поставщик",
			},
			photo:     &domain.PhotoUpload{Data: make([]byte, 5<<20+1), ContentType: "image/png"},
			wantError: "фото больше 5 МБ",
		},
		{
			name: "фото не изображение",
			page: &domain.WikiPage{
				Type:  domain.PageTypeSupplier,
				Title: "Поставщик",
			},
			photo:     &domain.PhotoUpload{Data: []byte("data"), ContentType: "application/pdf"},
			wantError: "фото должно быть изображением",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubWikiRepo{}
			uc := NewWikiUseCase(repo)

			err := uc.SavePage(context.Background(), tt.current, tt.page, tt.photo)
			if err == nil {
				t.Fatalf("SavePage: ожидалась ошибка, получен nil")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("SavePage error = %q, want содержит %q", err.Error(), tt.wantError)
			}
			if repo.saveCalled {
				t.Errorf("repo.SavePage вызван при ошибке валидации")
			}
		})
	}
}

func TestWikiUseCaseSavePageNormalization(t *testing.T) {
	repo := &stubWikiRepo{}
	uc := NewWikiUseCase(repo)

	page := &domain.WikiPage{
		Type:          domain.PageTypeProduct,
		Title:         "  Товар  ",
		Contacts:      []domain.Contact{{Name: " Иван ", Phone: " +7 900 "}},
		OrderDays:     []int{7, 1, 1, 3, 7},
		DeliveryDays:  []int{2, 2, 5},
		AverageWeight: " 12 кг ",
		Suppliers:     []string{"X", "x", " y "},
		Products:      []string{"П", "п", " q "},
		Tags:          []string{" Б ", "a", "b", " A ", "", " "},
	}

	if err := uc.SavePage(context.Background(), "", page, nil); err != nil {
		t.Fatalf("SavePage error: %v", err)
	}
	if !repo.saveCalled {
		t.Fatalf("repo.SavePage не вызван")
	}

	got := repo.lastSavedPage
	if got.Title != "Товар" {
		t.Errorf("Title = %q, want %q", got.Title, "Товар")
	}
	if !reflect.DeepEqual(got.Contacts, []domain.Contact{{Name: "Иван", Phone: "+7 900"}}) {
		t.Errorf("Contacts = %+v, want обрезанные поля", got.Contacts)
	}
	if !reflect.DeepEqual(got.OrderDays, []int{1, 3, 7}) {
		t.Errorf("OrderDays = %v, want [1 3 7]", got.OrderDays)
	}
	if !reflect.DeepEqual(got.DeliveryDays, []int{2, 5}) {
		t.Errorf("DeliveryDays = %v, want [2 5]", got.DeliveryDays)
	}
	if got.AverageWeight != "12 кг" {
		t.Errorf("AverageWeight = %q, want %q", got.AverageWeight, "12 кг")
	}
	if !reflect.DeepEqual(got.Suppliers, []string{"X", "y"}) {
		t.Errorf("Suppliers = %v, want [X y]", got.Suppliers)
	}
	if !reflect.DeepEqual(got.Products, []string{"П", "q"}) {
		t.Errorf("Products = %v, want [П q]", got.Products)
	}
	if !reflect.DeepEqual(got.Tags, []string{"Б", "a", "b"}) {
		t.Errorf("Tags = %v, want [Б a b]", got.Tags)
	}
}

func TestWikiUseCaseSavePageTypeChangeForbidden(t *testing.T) {
	repo := &stubWikiRepo{
		page: &domain.WikiPage{Type: domain.PageTypeSupplier, Title: "Старый"},
	}
	uc := NewWikiUseCase(repo)

	err := uc.SavePage(context.Background(), "Старый", &domain.WikiPage{
		Type:  domain.PageTypeProduct,
		Title: "Новый",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "тип страницы менять нельзя") {
		t.Fatalf("SavePage error = %v, want «тип страницы менять нельзя»", err)
	}
	if repo.saveCalled {
		t.Errorf("repo.SavePage не должен вызываться при смене типа")
	}
	if repo.lastGetPageTitle != "Старый" {
		t.Errorf("GetPage вызван с %q, want %q", repo.lastGetPageTitle, "Старый")
	}
}

func TestWikiUseCaseSavePageEditMissingPageIsCreate(t *testing.T) {
	// Редактирование несуществующей страницы считается созданием:
	// currentTitle обнуляется и страница сохраняется.
	repo := &stubWikiRepo{} // GetPage → (nil, nil)
	uc := NewWikiUseCase(repo)

	err := uc.SavePage(context.Background(), "Нет такой", &domain.WikiPage{
		Type:  domain.PageTypeSupplier,
		Title: "Новый поставщик",
	}, nil)
	if err != nil {
		t.Fatalf("SavePage error: %v", err)
	}
	if !repo.saveCalled {
		t.Fatalf("repo.SavePage не вызван")
	}
	if repo.lastCurrentTitle != "" {
		t.Errorf("currentTitle = %q, want \"\" (создание)", repo.lastCurrentTitle)
	}
}

func TestWikiUseCaseSavePageTitleTaken(t *testing.T) {
	repo := &stubWikiRepo{saveErr: domain.ErrTitleTaken}
	uc := NewWikiUseCase(repo)

	err := uc.SavePage(context.Background(), "", &domain.WikiPage{
		Type:  domain.PageTypeSupplier,
		Title: "Занято",
	}, nil)
	if !errors.Is(err, domain.ErrTitleTaken) {
		t.Fatalf("SavePage error = %v, want domain.ErrTitleTaken (errors.Is)", err)
	}
}

func TestWikiUseCaseSavePageEmptyPhotoIsNil(t *testing.T) {
	repo := &stubWikiRepo{}
	uc := NewWikiUseCase(repo)

	err := uc.SavePage(context.Background(), "", &domain.WikiPage{
		Type:  domain.PageTypeSupplier,
		Title: "Поставщик",
	}, &domain.PhotoUpload{ContentType: "image/png"})
	if err != nil {
		t.Fatalf("SavePage error: %v", err)
	}
	if repo.lastSavedPhoto != nil {
		t.Errorf("photo = %+v, want nil при пустых данных", repo.lastSavedPhoto)
	}
}

func TestWikiUseCaseGetPageWithBacklinksNotFound(t *testing.T) {
	repo := &stubWikiRepo{} // GetPage → (nil, nil)
	uc := NewWikiUseCase(repo)

	page, backlinks, err := uc.GetPageWithBacklinks(context.Background(), "Нет такой")
	if err != nil {
		t.Fatalf("GetPageWithBacklinks error: %v", err)
	}
	if page != nil || backlinks != nil {
		t.Fatalf("GetPageWithBacklinks = (%v, %v), want (nil, nil)", page, backlinks)
	}
}

func TestWikiUseCaseGetPageWithBacklinksFound(t *testing.T) {
	want := &domain.WikiPage{Type: domain.PageTypeSupplier, Title: "Поставщик"}
	repo := &stubWikiRepo{
		page:      want,
		backlinks: []string{"Товар"},
	}
	uc := NewWikiUseCase(repo)

	page, backlinks, err := uc.GetPageWithBacklinks(context.Background(), "Поставщик")
	if err != nil {
		t.Fatalf("GetPageWithBacklinks error: %v", err)
	}
	if page != want {
		t.Errorf("GetPageWithBacklinks page = %+v, want %+v", page, want)
	}
	if !reflect.DeepEqual(backlinks, []string{"Товар"}) {
		t.Errorf("GetPageWithBacklinks backlinks = %v, want [Товар]", backlinks)
	}
}

func TestWikiUseCaseResolveLinkTargetsEmpty(t *testing.T) {
	repo := &stubWikiRepo{}
	uc := NewWikiUseCase(repo)

	targets, err := uc.ResolveLinkTargets(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveLinkTargets error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("ResolveLinkTargets = %v, want пустая map", targets)
	}
	if repo.resolveCalled {
		t.Errorf("repo.ResolveLinkTargets вызван при пустом списке")
	}
}

func TestSyncSupplierPage_UpdateExisting(t *testing.T) {
	repo := &stubWikiRepo{linkedPage: &domain.WikiPage{ID: 42, Title: "Старое имя"}}
	uc := NewWikiUseCase(repo)

	err := uc.SyncSupplierPage(context.Background(), "uuid-1", "Мираторг", []int{1, 3}, []int{2, 5})
	if err != nil {
		t.Fatalf("SyncSupplierPage: %v", err)
	}
	if repo.lastUpdatedPageID != 42 || repo.lastUpdatedSupplierID != "uuid-1" || repo.lastUpdatedTitle != "Мираторг" {
		t.Fatalf("UpdateSupplierPage вызван с неверными аргументами: %+v", repo)
	}
	if !reflect.DeepEqual(repo.lastUpdatedOrderDays, []int{1, 3}) || !reflect.DeepEqual(repo.lastUpdatedDelivDays, []int{2, 5}) {
		t.Fatalf("дни переданы неверно: order=%v delivery=%v", repo.lastUpdatedOrderDays, repo.lastUpdatedDelivDays)
	}
	if repo.createdPage != nil {
		t.Fatalf("страница не должна создаваться при существующей привязке")
	}
}

func TestSyncSupplierPage_ClaimUnlinked(t *testing.T) {
	repo := &stubWikiRepo{unlinkedPage: &domain.WikiPage{ID: 7, Title: "Мираторг", SupplierID: ""}}
	uc := NewWikiUseCase(repo)

	err := uc.SyncSupplierPage(context.Background(), "uuid-1", "Мираторг", []int{1, 3}, nil)
	if err != nil {
		t.Fatalf("SyncSupplierPage: %v", err)
	}
	if repo.lastUpdatedPageID != 7 || repo.lastUpdatedSupplierID != "uuid-1" {
		t.Fatalf("claim: ожидался Update страницы 7 с привязкой uuid-1, получил %+v", repo)
	}
	if repo.createdPage != nil {
		t.Fatalf("при наличии непривязанной страницы создание не нужно")
	}
}

func TestSyncSupplierPage_CreateNew(t *testing.T) {
	repo := &stubWikiRepo{}
	uc := NewWikiUseCase(repo)

	err := uc.SyncSupplierPage(context.Background(), "uuid-1", "Новый поставщик", []int{1}, []int{3, 4})
	if err != nil {
		t.Fatalf("SyncSupplierPage: %v", err)
	}
	if repo.createdPage == nil {
		t.Fatalf("страница не создана")
	}
	if repo.createdPage.Type != domain.PageTypeSupplier || repo.createdPage.Title != "Новый поставщик" ||
		repo.createdPage.SupplierID != "uuid-1" {
		t.Fatalf("создана неверная страница: %+v", repo.createdPage)
	}
	if !reflect.DeepEqual(repo.createdPage.OrderDays, []int{1}) || !reflect.DeepEqual(repo.createdPage.DeliveryDays, []int{3, 4}) {
		t.Fatalf("дни созданной страницы неверны: %+v", repo.createdPage)
	}
}

func TestSyncSupplierPage_RecreateAfterManualDelete(t *testing.T) {
	// Привязки нет и непривязанной страницы нет (пользователь удалил) — создаём заново.
	repo := &stubWikiRepo{}
	uc := NewWikiUseCase(repo)

	if err := uc.SyncSupplierPage(context.Background(), "uuid-1", "Мираторг", nil, nil); err != nil {
		t.Fatalf("SyncSupplierPage: %v", err)
	}
	if repo.createdPage == nil {
		t.Fatalf("удалённая вручную страница должна пересоздаваться")
	}
}

func TestSyncSupplierPage_TitleTakenOnCreate(t *testing.T) {
	repo := &stubWikiRepo{createPageErr: domain.ErrTitleTaken}
	uc := NewWikiUseCase(repo)

	err := uc.SyncSupplierPage(context.Background(), "uuid-1", "Мираторг", nil, nil)
	if !errors.Is(err, domain.ErrTitleTaken) {
		t.Fatalf("ожидался ErrTitleTaken, получил %v", err)
	}
}

func TestSyncSupplierPage_Validation(t *testing.T) {
	uc := NewWikiUseCase(&stubWikiRepo{})

	if err := uc.SyncSupplierPage(context.Background(), "", "Мираторг", nil, nil); err == nil {
		t.Fatalf("пустой id поставщика должен давать ошибку")
	}
	if err := uc.SyncSupplierPage(context.Background(), "uuid-1", "  ", nil, nil); err == nil {
		t.Fatalf("пустое имя должно давать ошибку")
	}
}

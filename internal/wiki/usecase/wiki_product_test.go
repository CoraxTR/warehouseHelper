package usecase

import (
	"context"
	"errors"
	"testing"

	"warehouseHelper/internal/domain"
)

func TestEnsureProductPage_UpdateExisting(t *testing.T) {
	repo := &stubWikiRepo{linkedProductPage: &domain.WikiPage{ID: 42, Title: "Старое имя"}}
	uc := NewWikiUseCase(repo)

	err := uc.EnsureProductPage(context.Background(), "prod-1", "Грудной отруб", "2.5")
	if err != nil {
		t.Fatalf("EnsureProductPage: %v", err)
	}
	if repo.lastUpdatedPageID != 42 || repo.lastUpdatedProductID != "prod-1" ||
		repo.lastUpdatedTitle != "Грудной отруб" || repo.lastUpdatedAvgWeight != "2.5" {
		t.Fatalf("UpdateProductPage вызван с неверными аргументами: %+v", repo)
	}
	if repo.createdPage != nil {
		t.Fatal("страница не должна создаваться при существующей привязке")
	}
}

func TestEnsureProductPage_ClaimUnlinked(t *testing.T) {
	repo := &stubWikiRepo{unlinkedProductPage: &domain.WikiPage{ID: 7, Title: "Ветчина"}}
	uc := NewWikiUseCase(repo)

	err := uc.EnsureProductPage(context.Background(), "prod-2", "Ветчина", "")
	if err != nil {
		t.Fatalf("EnsureProductPage: %v", err)
	}
	if repo.lastUpdatedPageID != 7 || repo.lastUpdatedProductID != "prod-2" {
		t.Fatalf("claim: ожидался Update страницы 7 с привязкой prod-2, получил %+v", repo)
	}
	if repo.createdPage != nil {
		t.Fatal("при наличии непривязанной страницы создание не нужно")
	}
}

func TestEnsureProductPage_CreateNew(t *testing.T) {
	repo := &stubWikiRepo{}
	uc := NewWikiUseCase(repo)

	err := uc.EnsureProductPage(context.Background(), "prod-3", "Новый товар", "1.25")
	if err != nil {
		t.Fatalf("EnsureProductPage: %v", err)
	}
	if repo.createdPage == nil {
		t.Fatal("страница не создана")
	}
	if repo.createdPage.Type != domain.PageTypeProduct || repo.createdPage.Title != "Новый товар" ||
		repo.createdPage.ProductID != "prod-3" || repo.createdPage.AverageWeight != "1.25" {
		t.Fatalf("создана неверная страница: %+v", repo.createdPage)
	}
}

func TestEnsureProductPage_Validation(t *testing.T) {
	uc := NewWikiUseCase(&stubWikiRepo{})
	if err := uc.EnsureProductPage(context.Background(), "", "Товар", ""); err == nil {
		t.Fatal("пустой id товара должен давать ошибку")
	}
	if err := uc.EnsureProductPage(context.Background(), "prod-1", "  ", ""); err == nil {
		t.Fatal("пустое имя должно давать ошибку")
	}
}

func TestAddTagToPage(t *testing.T) {
	repo := &stubWikiRepo{page: &domain.WikiPage{ID: 9, Title: "Мираторг"}}
	uc := NewWikiUseCase(repo)

	err := uc.AddTagToPage(context.Background(), "Мираторг", "Ветчина")
	if err != nil {
		t.Fatalf("AddTagToPage: %v", err)
	}
	if len(repo.addedTags) != 1 || repo.addedTags[0].pageID != 9 || repo.addedTags[0].tag != "Ветчина" {
		t.Fatalf("AddPageTag вызван неверно: %+v", repo.addedTags)
	}
}

func TestAddTagToPage_PageNotFound(t *testing.T) {
	repo := &stubWikiRepo{} // GetPage → (nil, nil)
	uc := NewWikiUseCase(repo)

	err := uc.AddTagToPage(context.Background(), "Нет такой", "тег")
	if !errors.Is(err, domain.ErrPageNotFound) {
		t.Fatalf("ожидался ErrPageNotFound, получил %v", err)
	}
	if len(repo.addedTags) != 0 {
		t.Fatal("тег не должен добавляться при отсутствии страницы")
	}
}

func TestRemoveTagFromPage(t *testing.T) {
	repo := &stubWikiRepo{page: &domain.WikiPage{ID: 9, Title: "Ветчина"}}
	uc := NewWikiUseCase(repo)

	err := uc.RemoveTagFromPage(context.Background(), "Ветчина", "Мираторг")
	if err != nil {
		t.Fatalf("RemoveTagFromPage: %v", err)
	}
	if len(repo.removedTags) != 1 || repo.removedTags[0].pageID != 9 || repo.removedTags[0].tag != "Мираторг" {
		t.Fatalf("RemovePageTag вызван неверно: %+v", repo.removedTags)
	}
}

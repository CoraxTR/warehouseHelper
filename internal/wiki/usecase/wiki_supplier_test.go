package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"warehouseHelper/internal/domain"
)

func TestEnsureSupplierPage_CreateNew(t *testing.T) {
	repo := &stubWikiRepo{}
	uc := NewWikiUseCase(repo)

	if err := uc.EnsureSupplierPage(context.Background(), "sup-3", "Новый поставщик"); err != nil {
		t.Fatalf("EnsureSupplierPage: %v", err)
	}
	if repo.createdPage == nil {
		t.Fatal("страница не создана")
	}
	if repo.createdPage.Type != domain.PageTypeSupplier || repo.createdPage.Title != "Новый поставщик" ||
		repo.createdPage.SupplierID != "sup-3" {
		t.Fatalf("создана неверная страница: %+v", repo.createdPage)
	}
	if repo.lastUpdatedPageID != 0 {
		t.Fatalf("при отсутствии страницы Update не нужен: %+v", repo)
	}
}

func TestEnsureSupplierPage_ExistingPageUntouched(t *testing.T) {
	linked := &domain.WikiPage{
		ID: 42, Title: "Мираторг",
		OrderDays: []int{1, 3}, DeliveryDays: []int{2, 5},
	}
	repo := &stubWikiRepo{linkedPage: linked}
	uc := NewWikiUseCase(repo)

	if err := uc.EnsureSupplierPage(context.Background(), "sup-1", "Мираторг"); err != nil {
		t.Fatalf("EnsureSupplierPage: %v", err)
	}
	if repo.createdPage != nil {
		t.Fatal("страница не должна создаваться при существующей привязке")
	}
	if repo.lastUpdatedPageID != 0 {
		t.Fatalf("существующая страница не должна обновляться: %+v", repo)
	}
	if len(linked.OrderDays) != 2 || len(linked.DeliveryDays) != 2 {
		t.Fatalf("график доставки изменён: %+v", linked)
	}
}

func TestEnsureSupplierPage_ClaimUnlinkedKeepsDays(t *testing.T) {
	unlinked := &domain.WikiPage{
		ID: 7, Title: "Мираторг",
		OrderDays: []int{1, 2}, DeliveryDays: []int{5, 6},
	}
	repo := &stubWikiRepo{unlinkedPage: unlinked}
	uc := NewWikiUseCase(repo)

	if err := uc.EnsureSupplierPage(context.Background(), "sup-2", "Мираторг"); err != nil {
		t.Fatalf("EnsureSupplierPage: %v", err)
	}
	if repo.createdPage != nil {
		t.Fatal("при наличии непривязанной страницы создание не нужно")
	}
	if repo.lastUpdatedPageID != 7 || repo.lastUpdatedSupplierID != "sup-2" {
		t.Fatalf("claim: ожидался Update страницы 7 с привязкой sup-2, получил %+v", repo)
	}
	// Claim не перезаписывает график доставки: передаются текущие дни страницы.
	if len(repo.lastUpdatedOrderDays) != 2 || len(repo.lastUpdatedDelivDays) != 2 {
		t.Fatalf("график доставки затёрт при claim: order=%v deliv=%v",
			repo.lastUpdatedOrderDays, repo.lastUpdatedDelivDays)
	}
}

func TestEnsureSupplierPage_CreateErrorWrapped(t *testing.T) {
	repo := &stubWikiRepo{createPageErr: domain.ErrTitleTaken}
	uc := NewWikiUseCase(repo)

	err := uc.EnsureSupplierPage(context.Background(), "sup-4", "Занятый заголовок")
	if !errors.Is(err, domain.ErrTitleTaken) {
		t.Fatalf("ожидалась обёрнутая ErrTitleTaken, получил %v", err)
	}
	if !strings.Contains(err.Error(), "создать страницу вики поставщика") {
		t.Fatalf("ошибка должна быть обёрнута с контекстом: %v", err)
	}
}

func TestEnsureSupplierPage_Validation(t *testing.T) {
	tests := []struct {
		name       string
		supplierID string
		pageName   string
	}{
		{name: "пустой id поставщика", supplierID: "  ", pageName: "Мираторг"},
		{name: "пустое имя поставщика", supplierID: "sup-1", pageName: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubWikiRepo{}
			uc := NewWikiUseCase(repo)
			if err := uc.EnsureSupplierPage(context.Background(), tt.supplierID, tt.pageName); err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			if repo.createdPage != nil || repo.lastUpdatedPageID != 0 {
				t.Fatalf("при ошибке валидации репозиторий не трогается: %+v", repo)
			}
		})
	}
}

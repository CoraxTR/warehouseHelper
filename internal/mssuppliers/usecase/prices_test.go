package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"warehouseHelper/internal/domain"
)

// fakeCatalog — каталог для названий товаров в ценах.
type fakeCatalog struct {
	titles map[string]string
	err    error
}

func (f *fakeCatalog) ProductTitles(_ context.Context, ids []string) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if t, ok := f.titles[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

func newPricesUC(repo *fakeRepo, catalog *fakeCatalog) *MSSuppliersUseCase {
	uc := NewMSSuppliersUseCase(repo, &fakeMS{}, &fakeWiki{}, nil)
	if catalog != nil {
		uc.SetCatalog(catalog)
	}
	return uc
}

func TestSetSupplierPrice_UpsertsAndClears(t *testing.T) {
	repo := newFakeRepo()
	uc := newPricesUC(repo, nil)

	if err := uc.SetSupplierPrice(context.Background(), "sup-1", "p-1", 19990); err != nil {
		t.Fatalf("SetSupplierPrice: %v", err)
	}
	if got := repo.prices[priceKey("sup-1", "p-1")]; got != 19990 {
		t.Fatalf("цена = %d, want 19990", got)
	}

	// 0 = «цена не задана»: строка снимается.
	if err := uc.SetSupplierPrice(context.Background(), "sup-1", "p-1", 0); err != nil {
		t.Fatalf("SetSupplierPrice(0): %v", err)
	}
	if _, ok := repo.prices[priceKey("sup-1", "p-1")]; ok {
		t.Fatal("строка цены осталась после снятия")
	}
}

func TestSetSupplierPrice_Validation(t *testing.T) {
	repo := newFakeRepo()
	uc := newPricesUC(repo, nil)

	if err := uc.SetSupplierPrice(context.Background(), " ", "p-1", 100); err == nil {
		t.Fatal("пустой поставщик принят")
	}
	if err := uc.SetSupplierPrice(context.Background(), "sup-1", "", 100); err == nil {
		t.Fatal("пустой товар принят")
	}
	if err := uc.SetSupplierPrice(context.Background(), "sup-1", "p-1", maxPriceKop+1); err == nil {
		t.Fatal("цена вне диапазона принята")
	}
	if len(repo.prices) != 0 {
		t.Fatalf("после ошибок что-то записалось: %+v", repo.prices)
	}
}

func TestSupplierPrices_FillsTitlesAndSorts(t *testing.T) {
	repo := newFakeRepo()
	repo.prices[priceKey("sup-1", "p-2")] = 500
	repo.prices[priceKey("sup-1", "p-1")] = 1500

	catalog := &fakeCatalog{titles: map[string]string{"p-1": "Говядина", "p-2": "Баранина"}}
	uc := newPricesUC(repo, catalog)

	got, err := uc.SupplierPrices(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("SupplierPrices: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("цен = %d, want 2", len(got))
	}
	if got[0].ProductName != "Баранина" || got[1].ProductName != "Говядина" {
		t.Fatalf("порядок/названия = %+v, want Баранина, Говядина", got)
	}
}

func TestSupplierPrices_CatalogError(t *testing.T) {
	repo := newFakeRepo()
	repo.prices[priceKey("sup-1", "p-1")] = 100
	uc := newPricesUC(repo, &fakeCatalog{err: errors.New("каталог недоступен")})

	_, err := uc.SupplierPrices(context.Background(), "sup-1")
	if err == nil || !strings.Contains(err.Error(), "названия товаров") {
		t.Fatalf("SupplierPrices = %v, want ошибку названий", err)
	}
}

func TestProductPrices_ReturnsSupplierNames(t *testing.T) {
	repo := newFakeRepo()
	repo.suppliers["sup-1"] = domain.Supplier{ID: "sup-1", Name: "Мираторг"}
	repo.prices[priceKey("sup-1", "p-1")] = 1234
	uc := newPricesUC(repo, nil)

	got, err := uc.ProductPrices(context.Background(), "p-1")
	if err != nil {
		t.Fatalf("ProductPrices: %v", err)
	}
	if len(got) != 1 || got[0].SupplierName != "Мираторг" || got[0].Price != 1234 {
		t.Fatalf("ProductPrices = %+v, want Мираторг 1234", got)
	}

	if _, err := uc.ProductPrices(context.Background(), ""); err == nil {
		t.Fatal("пустой товар принят")
	}
}

package usecase

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"warehouseHelper/internal/domain"
)

func mkop(v int64) *int64 { return &v }
func mpct(v int16) *int16 { return &v }

// TestComputeMarkup — формула наценки владельца (05.10.2026):
//
//	(цена продажи − (НДС исходящий − НДС входящий) − закупочная) / закупочная × 100.
//
// Проверяем разобранные вручную примеры и главное правило: неполные данные НЕ
// считаем — Missing перечисляет, чего не хватает (страница показывает это
// вместо наценки), а НДС -1 («без НДС») — ЗАПОЛНЕННОЕ значение и читается как 0 %.
func TestComputeMarkup(t *testing.T) {
	tests := []struct {
		name          string
		sale, buy     *int64
		vatOut, vatIn *int16
		wantPercent   float64
		wantMissing   []string
	}{
		{
			name: "100₽/60₽, НДС 20/20 — наценка по ценам без НДС",
			sale: mkop(10000), buy: mkop(6000), vatOut: mpct(20), vatIn: mpct(20),
			wantPercent: 160.0 / 3.0, // 3200/6000*100
		},
		{
			name: "входящий НДС 0 — вычет не учитывается",
			sale: mkop(10000), buy: mkop(6000), vatOut: mpct(20), vatIn: mpct(0),
			wantPercent: 100.0 / 3.0, // 2000/6000*100
		},
		{
			name: "наш НДС -1 («без НДС») — считаем как 0 %, данные НЕ считаем неполными",
			sale: mkop(10000), buy: mkop(6000), vatOut: mpct(-1), vatIn: mpct(20),
			wantPercent: 260.0 / 3.0, // 5200/6000*100
		},
		{
			name: "наш НДС 10, входящий 20 — вычет больше начисленного",
			sale: mkop(10000), buy: mkop(6000), vatOut: mpct(10), vatIn: mpct(20),
			wantPercent: 70,
		},
		{
			name: "нет цены продажи",
			sale: nil, buy: mkop(6000), vatOut: mpct(20), vatIn: mpct(20),
			wantMissing: []string{"цена продажи"},
		},
		{
			name: "закупочная NULL — делить не на что",
			sale: mkop(10000), buy: nil, vatOut: mpct(20), vatIn: mpct(20),
			wantMissing: []string{"закупочная цена"},
		},
		{
			name: "закупочная 0 — тоже неполные данные",
			sale: mkop(10000), buy: mkop(0), vatOut: mpct(20), vatIn: mpct(20),
			wantMissing: []string{"закупочная цена"},
		},
		{
			name: "наш НДС NULL (МС не отдала — товар наследует НДС группы)",
			sale: mkop(10000), buy: mkop(6000), vatOut: nil, vatIn: mpct(20),
			wantMissing: []string{"наш НДС"},
		},
		{
			name: "входящий НДС не задан — человек его ещё не вводил",
			sale: mkop(10000), buy: mkop(6000), vatOut: mpct(20), vatIn: nil,
			wantMissing: []string{"входящий НДС"},
		},
		{
			name: "данных нет вовсе — перечисляем всё, что нужно",
			sale: nil, buy: nil, vatOut: nil, vatIn: nil,
			wantMissing: []string{"цена продажи", "закупочная цена", "наш НДС", "входящий НДС"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeMarkup(tc.sale, tc.buy, tc.vatOut, tc.vatIn)
			if !reflect.DeepEqual(got.Missing, tc.wantMissing) {
				t.Fatalf("Missing = %v, want %v", got.Missing, tc.wantMissing)
			}
			if len(tc.wantMissing) > 0 {
				return // наценки нет по определению
			}
			if math.Abs(got.Percent-tc.wantPercent) > 1e-9 {
				t.Errorf("Percent = %v, want %v", got.Percent, tc.wantPercent)
			}
		})
	}
}

// TestPriceCheckList — строка страницы: наценка по значениям базы и снапшот
// песочницы (если он есть) со своей наценкой; товары без цен дают Missing.
func TestPriceCheckList(t *testing.T) {
	repo := &stubProductsRepo{
		search: []domain.Product{
			{
				ID: "p-1", Name: "Сыр", GroupName: "Молочка",
				SalePrice: mkop(10000), BuyPrice: mkop(6000),
				EffectiveVat: mpct(20), VATIncoming: mpct(20),
			},
			{ID: "p-2", Name: "Пустой", GroupName: "Молочка"},
		},
		sandboxes: map[string]domain.PriceSandbox{
			"p-1": {
				ProductID: "p-1",
				SalePrice: mkop(20000), BuyPrice: mkop(6000),
				EffectiveVat: mpct(20), VATIncoming: mpct(20),
			},
		},
	}
	uc := NewGoodsUseCase(nil, nil, repo, nil)

	items, err := uc.PriceCheckList(context.Background())
	if err != nil {
		t.Fatalf("PriceCheckList: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("строк %d, want 2", len(items))
	}

	if got := items[0].Markup; len(got.Missing) != 0 || math.Abs(got.Percent-160.0/3.0) > 1e-9 {
		t.Errorf("p-1 Markup = %+v, want 53.33 по базе", got)
	}
	if items[0].Sandbox == nil {
		t.Fatal("p-1: снапшот песочницы не подключён")
	}
	if got := items[0].SandboxMarkup; len(got.Missing) != 0 || math.Abs(got.Percent-560.0/3.0) > 1e-9 {
		t.Errorf("p-1 SandboxMarkup = %+v, want 186.67 по снапшоту", got)
	}
	if items[1].Sandbox != nil {
		t.Error("p-2: снапшота быть не должно")
	}
	if got := items[1].Markup.Missing; !reflect.DeepEqual(got,
		[]string{"цена продажи", "закупочная цена", "наш НДС", "входящий НДС"}) {
		t.Errorf("p-2 Missing = %v", got)
	}
}

// TestSetIncomingVAT — ручное поле пишет products.vat_incoming; диапазон 0..100
// проверяется ДО записи (ничего не пишем при неверном значении), nil — сброс.
func TestSetIncomingVAT(t *testing.T) {
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(nil, nil, repo, nil)
	ctx := context.Background()

	if err := uc.SetIncomingVAT(ctx, "p-1", mpct(20)); err != nil {
		t.Fatalf("SetIncomingVAT: %v", err)
	}
	if got := repo.incomingVAT["p-1"]; got == nil || *got != 20 {
		t.Fatalf("записано %v, want 20", got)
	}

	if err := uc.SetIncomingVAT(ctx, "p-1", mpct(101)); !errors.Is(err, ErrVATOutOfRange) {
		t.Fatalf("НДС 101: err = %v, want ErrVATOutOfRange", err)
	}
	if got := repo.incomingVAT["p-1"]; got == nil || *got != 20 {
		t.Errorf("после отказа записано %v — писать было нечего", got)
	}

	if err := uc.SetIncomingVAT(ctx, "p-1", nil); err != nil {
		t.Fatalf("сброс: %v", err)
	}
	if got, ok := repo.incomingVAT["p-1"]; !ok || got != nil {
		t.Errorf("сброс записал %v, want nil", got)
	}
}

// TestSavePriceSandbox — снапшот сохраняется и наценка считается по НЕМУ
// (а не по базе); неверные значения отсекаются до записи.
func TestSavePriceSandbox(t *testing.T) {
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(nil, nil, repo, nil)
	ctx := context.Background()

	box := domain.PriceSandbox{
		ProductID: "p-1", SalePrice: mkop(10000), BuyPrice: mkop(6000),
		EffectiveVat: mpct(20), VATIncoming: mpct(0),
	}
	res, err := uc.SavePriceSandbox(ctx, box)
	if err != nil {
		t.Fatalf("SavePriceSandbox: %v", err)
	}
	if math.Abs(res.Percent-100.0/3.0) > 1e-9 {
		t.Errorf("наценка песочницы = %v, want 33.33", res.Percent)
	}
	if repo.savedSandbox == nil || repo.savedSandbox.ProductID != "p-1" {
		t.Fatalf("снапшот не сохранён: %+v", repo.savedSandbox)
	}

	if _, err := uc.SavePriceSandbox(ctx, domain.PriceSandbox{ProductID: "p-1", BuyPrice: mkop(-1)}); !errors.Is(err, ErrPriceNegative) {
		t.Errorf("отрицательная цена: err = %v, want ErrPriceNegative", err)
	}
	if _, err := uc.SavePriceSandbox(ctx, domain.PriceSandbox{EffectiveVat: nil}); !errors.Is(err, domain.ErrProductNotFound) {
		t.Errorf("без product_id: err = %v, want ErrProductNotFound", err)
	}
}

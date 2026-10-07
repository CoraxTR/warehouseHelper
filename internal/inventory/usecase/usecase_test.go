package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"warehouseHelper/internal/inventory"
	"warehouseHelper/internal/msclient/client"
)

// Внутренние коды и даты для сборки валидных сканов (формат как в
// internal/inventory/inventory_test.go: код(8) + вес(5) + даты(8+8) = 29 цифр).
const (
	invWeightCode = "00210003" // весовой товар
	invPieceCode  = "11210004" // штучный товар
	invProdDate   = "29082026"
	invExpDate    = "29092026"
)

// invItem собирает внутренний штрих-код куска.
func invItem(code string, weightG int) string {
	return fmt.Sprintf("%s%05d%s%s", code, weightG, invProdDate, invExpDate)
}

// Позиции каталога для тестов.
var (
	invWeightProduct = inventory.Product{
		ID:           "prod-weight",
		InternalCode: invWeightCode,
		Name:         "Говядина охл",
		UOM:          "кг",
		BuyPriceKop:  new(int64(150000)),
	}
	invPieceProduct = inventory.Product{
		ID:           "prod-piece",
		InternalCode: invPieceCode,
		Name:         "Соус",
		UOM:          "шт",
		BuyPriceKop:  new(int64(50000)),
	}
	invNoCodeProduct = inventory.Product{
		ID:   "prod-nocode",
		Name: "Товар без кода",
		UOM:  "шт",
	}
)

// fakeInvCatalog — фейк каталога (модуль «Продукция»).
type fakeInvCatalog struct {
	types       []string
	typesErr    error
	products    []inventory.Product
	productsErr error
	askedType   string
}

func (f *fakeInvCatalog) InventoryTypes(context.Context) ([]string, error) {
	return f.types, f.typesErr
}

func (f *fakeInvCatalog) InventoryProducts(_ context.Context, inventoryType string) ([]inventory.Product, error) {
	f.askedType = inventoryType
	return f.products, f.productsErr
}

// fakeInvMS — фейк клиента МойСклад.
type fakeInvMS struct {
	positions       []client.MSInventoryPosition
	doc             client.MSInventoryDocument
	err             error
	storeConfigured bool
	calls           int
	trace           *[]string // общая лента порядка вызовов: сюда пишется "ms"
}

func (f *fakeInvMS) CreateInventory(
	_ context.Context,
	positions []client.MSInventoryPosition,
) (client.MSInventoryDocument, error) {
	f.calls++
	if f.trace != nil {
		*f.trace = append(*f.trace, "ms")
	}
	f.positions = positions
	return f.doc, f.err
}

func (f *fakeInvMS) InventoryStoreConfigured() bool {
	return f.storeConfigured
}

// fakeInvSroki — фейк модуля «Сроки» (шов Sroki): запоминает коды вида и сканы,
// чтобы Conduct проверял замену остатков без реального стока.
type fakeInvSroki struct {
	codes []string
	scans []string
	err   error
	calls int
	trace *[]string // общая лента порядка вызовов: сюда пишется "sroki"
}

func (f *fakeInvSroki) ReplaceInventoryLots(_ context.Context, codes, scans []string) error {
	f.calls++
	if f.trace != nil {
		*f.trace = append(*f.trace, "sroki")
	}
	f.codes = codes
	f.scans = scans
	return f.err
}

// 1. Types делегирует каталогу; ошибка каталога пробрасывается.
func TestTypes(t *testing.T) {
	ctx := context.Background()

	t.Run("список каталога", func(t *testing.T) {
		cat := &fakeInvCatalog{types: []string{"охл/Вагю", "заморозка"}}
		uc := New(cat, &fakeInvMS{}, &fakeInvSroki{})

		got, err := uc.Types(ctx)
		if err != nil {
			t.Fatalf("Types() error = %v, want nil", err)
		}
		if want := []string{"охл/Вагю", "заморозка"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Types() = %v, want %v", got, want)
		}
	})

	t.Run("ошибка каталога пробрасывается", func(t *testing.T) {
		wantErr := errors.New("каталог недоступен")
		uc := New(&fakeInvCatalog{typesErr: wantErr}, &fakeInvMS{}, &fakeInvSroki{})

		if _, err := uc.Types(ctx); !errors.Is(err, wantErr) {
			t.Fatalf("Types() error = %v, want errors.Is %v", err, wantErr)
		}
	})
}

// 2. Пустой/пробельный вид: ErrNoType и никаких обращений к каталогу.
func TestNoType(t *testing.T) {
	ctx := context.Background()
	views := []struct {
		name string
		view string
	}{
		{name: "пустой вид", view: ""},
		{name: "пробельный вид", view: "   "},
	}

	for _, tt := range views {
		t.Run(tt.name, func(t *testing.T) {
			cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
			ms := &fakeInvMS{}
			uc := New(cat, ms, &fakeInvSroki{})

			if _, err := uc.Group(ctx, tt.view); !errors.Is(err, ErrNoType) {
				t.Errorf("Group() error = %v, want ErrNoType", err)
			}
			if _, err := uc.Preview(ctx, tt.view, []string{invItem(invWeightCode, 250)}, nil); !errors.Is(err, ErrNoType) {
				t.Errorf("Preview() error = %v, want ErrNoType", err)
			}
			if _, _, err := uc.Conduct(ctx, tt.view, []string{invItem(invWeightCode, 250)}, nil); !errors.Is(err, ErrNoType) {
				t.Errorf("Conduct() error = %v, want ErrNoType", err)
			}

			if cat.askedType != "" {
				t.Errorf("каталог дёрнули при пустом виде: askedType = %q", cat.askedType)
			}
			if ms.calls != 0 {
				t.Errorf("клиент МС дёрнули при пустом виде: calls = %d", ms.calls)
			}
		})
	}
}

// 3. Preview: все позиции группы, факт по сканам, цены перенесены.
func TestPreview(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		view      string
		products  []inventory.Product
		scans     []string
		want      inventory.Preview
		wantAsked string
	}{
		{
			name:     "весовой с кодом и штучный без кода: один валидный скан",
			view:     "охл/Вагю",
			products: []inventory.Product{invWeightProduct, invNoCodeProduct},
			scans:    []string{invItem(invWeightCode, 250)},
			want: inventory.Preview{
				Lines: []inventory.Line{
					{
						ProductID:    "prod-weight",
						InternalCode: invWeightCode,
						Name:         "Говядина охл",
						UOM:          "кг",
						Weighted:     true,
						Fact:         0.25,
						Scans:        1,
						PriceKop:     150000,
						Scanable:     true,
					},
					{
						ProductID: "prod-nocode",
						Name:      "Товар без кода",
						UOM:       "шт",
						Scanable:  false,
					},
				},
				Total: 2, Scanned: 1, Scans: 1,
			},
			wantAsked: "охл/Вагю",
		},
		{
			name:     "вид с пробелами обрезается перед каталогом",
			view:     "  заморозка  ",
			products: []inventory.Product{invWeightProduct},
			scans:    nil,
			want: inventory.Preview{
				Lines: []inventory.Line{{
					ProductID:    "prod-weight",
					InternalCode: invWeightCode,
					Name:         "Говядина охл",
					UOM:          "кг",
					Weighted:     true,
					PriceKop:     150000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 0, Scans: 0,
			},
			wantAsked: "заморозка",
		},
		{
			name:     "штучный скан даёт штуки",
			view:     "сопутка",
			products: []inventory.Product{invPieceProduct},
			scans:    []string{invItem(invPieceCode, 100), invItem(invPieceCode, 200)},
			want: inventory.Preview{
				Lines: []inventory.Line{{
					ProductID:    "prod-piece",
					InternalCode: invPieceCode,
					Name:         "Соус",
					UOM:          "шт",
					Fact:         2,
					Scans:        2,
					PriceKop:     50000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 2,
			},
			wantAsked: "сопутка",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := &fakeInvCatalog{products: tt.products}
			uc := New(cat, &fakeInvMS{}, &fakeInvSroki{})

			got, err := uc.Preview(ctx, tt.view, tt.scans, nil)
			if err != nil {
				t.Fatalf("Preview() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Preview() = %+v, want %+v", got, tt.want)
			}
			if cat.askedType != tt.wantAsked {
				t.Fatalf("каталог спросили про %q, want %q", cat.askedType, tt.wantAsked)
			}
		})
	}
}

// 4. Preview: скан чужого кода — ошибка агрегации наружу как есть.
func TestPreviewForeignCode(t *testing.T) {
	cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
	uc := New(cat, &fakeInvMS{}, &fakeInvSroki{})

	_, err := uc.Preview(context.Background(), "охл", []string{invItem("21210005", 300)}, nil)
	if !errors.Is(err, inventory.ErrScanNotInGroup) {
		t.Fatalf("Preview() error = %v, want errors.Is %v", err, inventory.ErrScanNotInGroup)
	}
}

// 4б. Preview: невалидный скан — тоже как есть.
func TestPreviewInvalidScan(t *testing.T) {
	cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
	uc := New(cat, &fakeInvMS{}, &fakeInvSroki{})

	_, err := uc.Preview(context.Background(), "охл", []string{"1234567890"}, nil)
	if !errors.Is(err, inventory.ErrScanInvalid) {
		t.Fatalf("Preview() error = %v, want errors.Is %v", err, inventory.ErrScanInvalid)
	}
}

// 4в. Preview: ошибка каталога пробрасывается, агрегации не происходит.
func TestPreviewCatalogError(t *testing.T) {
	wantErr := errors.New("каталог упал")
	cat := &fakeInvCatalog{productsErr: wantErr}
	uc := New(cat, &fakeInvMS{}, &fakeInvSroki{})

	if _, err := uc.Preview(context.Background(), "охл", nil, nil); !errors.Is(err, wantErr) {
		t.Fatalf("Preview() error = %v, want errors.Is %v", err, wantErr)
	}
}

// 5. Conduct: позиции уходят в клиент в порядке строк отчёта, включая
// непросканированную с нулями; возвращается документ клиента.
func TestConduct(t *testing.T) {
	doc := client.MSInventoryDocument{
		ID:   "doc-1",
		Name: "Инвентаризация № 1",
		URL:  "https://api.moysklad.ru/entity/inventory/doc-1",
	}
	cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct, invNoCodeProduct}}
	ms := &fakeInvMS{doc: doc}
	uc := New(cat, ms, &fakeInvSroki{})

	got, _, err := uc.Conduct(context.Background(), "охл", []string{invItem(invWeightCode, 250)}, nil)
	if err != nil {
		t.Fatalf("Conduct() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("Conduct() doc = %+v, want %+v", got, doc)
	}
	if ms.calls != 1 {
		t.Fatalf("CreateInventory вызван %d раз, want 1", ms.calls)
	}
	want := []client.MSInventoryPosition{
		{AssortmentID: "prod-weight", Quantity: 0.25, PriceKop: 150000},
		{AssortmentID: "prod-nocode", Quantity: 0, PriceKop: 0},
	}
	if !reflect.DeepEqual(ms.positions, want) {
		t.Fatalf("позиции в клиент = %+v, want %+v", ms.positions, want)
	}
}

// 6. Conduct при пустой группе — ErrEmptyGroup, в клиент МС не ходим.
func TestConductEmptyGroup(t *testing.T) {
	cat := &fakeInvCatalog{products: nil}
	ms := &fakeInvMS{}
	uc := New(cat, ms, &fakeInvSroki{})

	got, _, err := uc.Conduct(context.Background(), "сопутка", nil, nil)
	if !errors.Is(err, ErrEmptyGroup) {
		t.Fatalf("Conduct() error = %v, want ErrEmptyGroup", err)
	}
	if got != (client.MSInventoryDocument{}) {
		t.Fatalf("Conduct() doc = %+v, want пустой", got)
	}
	if ms.calls != 0 {
		t.Fatalf("CreateInventory вызван %d раз при пустой группе, want 0", ms.calls)
	}
}

// 7. Conduct при ошибке клиента — ошибка наружу, документ пустой.
func TestConductClientError(t *testing.T) {
	wantErr := errors.New("МС 500")
	cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
	ms := &fakeInvMS{err: wantErr}
	uc := New(cat, ms, &fakeInvSroki{})

	got, _, err := uc.Conduct(context.Background(), "охл", []string{invItem(invWeightCode, 250)}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Conduct() error = %v, want errors.Is %v", err, wantErr)
	}
	if got != (client.MSInventoryDocument{}) {
		t.Fatalf("Conduct() doc = %+v, want пустой", got)
	}
	if ms.calls != 1 {
		t.Fatalf("CreateInventory вызван %d раз, want 1", ms.calls)
	}
}

// 7б. Conduct при ошибке агрегации — ошибка как есть, в клиент не ходим.
func TestConductAggregateError(t *testing.T) {
	cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
	ms := &fakeInvMS{}
	uc := New(cat, ms, &fakeInvSroki{})

	_, _, err := uc.Conduct(context.Background(), "охл", []string{invItem("21210005", 300)}, nil)
	if !errors.Is(err, inventory.ErrScanNotInGroup) {
		t.Fatalf("Conduct() error = %v, want errors.Is %v", err, inventory.ErrScanNotInGroup)
	}
	if ms.calls != 0 {
		t.Fatalf("CreateInventory вызван %d раз при ошибке агрегации, want 0", ms.calls)
	}
}

// 9. MergeGuestScans — склейка набора хоста со строками гостей совместной
// инвентаризации: строки гостей идут ПОСЛЕ своих и уходят в один Conduct
// (документ в МС создаёт только хозяин). Гость шлёт объекты {"raw":…,"seq":…};
// из строки берётся raw, остальное (курсор) склейке не нужно.
func TestMergeGuestScans(t *testing.T) {
	own := []string{invItem(invWeightCode, 250), invItem(invPieceCode, 100)}

	t.Run("строки гостей доклеиваются к своим", func(t *testing.T) {
		guests := []json.RawMessage{
			json.RawMessage(`{"raw":"` + invItem(invWeightCode, 300) + `","seq":1}`),
			json.RawMessage(`{"raw":"` + invItem(invPieceCode, 100) + `","seq":2}`),
		}
		want := []string{own[0], own[1], invItem(invWeightCode, 300), invItem(invPieceCode, 100)}

		got, err := MergeGuestScans(own, nil, guests)
		if err != nil {
			t.Fatalf("MergeGuestScans() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got.Scans, want) {
			t.Fatalf("MergeGuestScans() = %v, want %v", got.Scans, want)
		}
	})

	t.Run("пустой список гостей — свои без изменений", func(t *testing.T) {
		got, err := MergeGuestScans(own, nil, nil)
		if err != nil {
			t.Fatalf("MergeGuestScans() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got.Scans, own) {
			t.Fatalf("MergeGuestScans() = %v, want свои %v", got, own)
		}
	})

	t.Run("чужие коды не трогаем — склейка только вынимает raw", func(t *testing.T) {
		// Вид и валидность кода проверяет Conduct (Aggregate): склейка лишь
		// переносит строку, чтобы отказ был один и с понятным текстом.
		guests := []json.RawMessage{json.RawMessage(`{"raw":"1234567890","seq":1}`)}

		got, err := MergeGuestScans(own, nil, guests)
		if err != nil {
			t.Fatalf("MergeGuestScans() error = %v, want nil", err)
		}
		if want := []string{own[0], own[1], "1234567890"}; !reflect.DeepEqual(got.Scans, want) {
			t.Fatalf("MergeGuestScans() = %v, want %v", got, want)
		}
	})

	t.Run("мусорная строка (не JSON) — ошибка, строки не угадываем", func(t *testing.T) {
		guests := []json.RawMessage{json.RawMessage(`{"raw":"` + invItem(invWeightCode, 300) + `"}`), json.RawMessage(`не json`)}
		if got, err := MergeGuestScans(own, nil, guests); err == nil {
			t.Fatalf("MergeGuestScans() = %v, want ошибку разбора", got)
		}
	})

	t.Run("строка без raw пропускается, а не ломает склейку", func(t *testing.T) {
		// validCollabScans не пропускает строку без raw ещё на входе, но склейка
		// защищена сама: пустышка не должна ронять проведение всего документа.
		guests := []json.RawMessage{
			json.RawMessage(`{}`),
			json.RawMessage(`{"raw":"   "}`),
			json.RawMessage(`{"seq":3}`),
			json.RawMessage(`{"raw":"` + invItem(invPieceCode, 200) + `","seq":4}`),
		}
		want := []string{own[0], own[1], invItem(invPieceCode, 200)}

		got, err := MergeGuestScans(own, nil, guests)
		if err != nil {
			t.Fatalf("MergeGuestScans() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got.Scans, want) {
			t.Fatalf("MergeGuestScans() = %v, want %v", got.Scans, want)
		}
	})

	t.Run("товар по internal id вместо штрих-кода — ошибка, а не тихая потеря", func(t *testing.T) {
		// Такую строку общая валидация комнаты пропускает (manual_product_id
		// непустой), но провести по ней инвентаризацию нельзя: строка гостя
		// должна дать отказ, а не исчезнуть из документа молча.
		guests := []json.RawMessage{json.RawMessage(`{"manual_product_id":"prod-piece","seq":5}`)}

		if got, err := MergeGuestScans(own, nil, guests); err == nil {
			t.Fatalf("MergeGuestScans() = %v, want ошибку про строку без штрих-кода", got)
		}
	})
}

// 8. StoreConfigured — значение из клиента МС.
func TestStoreConfigured(t *testing.T) {
	tests := []struct {
		name string
		flag bool
	}{
		{name: "склад задан", flag: true},
		{name: "склад пуст", flag: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc := New(&fakeInvCatalog{}, &fakeInvMS{storeConfigured: tt.flag}, &fakeInvSroki{})
			if got := uc.StoreConfigured(); got != tt.flag {
				t.Fatalf("StoreConfigured() = %v, want %v", got, tt.flag)
			}
		})
	}
}

// 10. Conduct со швом «Сроки»: порядок шагов сроки→документ и страховка от
// обнуления вида (пустая общая строка). Состав аргументов шва и поведение при
// сбоях шва/МС — в TestConductSrokiCalls.
func TestConductSroki(t *testing.T) {
	ctx := context.Background()
	doc := client.MSInventoryDocument{
		ID:   "doc-1",
		Name: "Инвентаризация № 1",
		URL:  "https://api.moysklad.ru/entity/inventory/doc-1",
	}

	t.Run("порядок вызовов сроки→документ, успех = SrokiUpdated", func(t *testing.T) {
		trace := []string{}
		cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
		ms := &fakeInvMS{doc: doc, trace: &trace}
		sroki := &fakeInvSroki{trace: &trace}
		uc := New(cat, ms, sroki)

		got, outcome, err := uc.Conduct(ctx, "охл", []string{invItem(invWeightCode, 250)}, nil)
		if err != nil {
			t.Fatalf("Conduct() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got, doc) {
			t.Fatalf("Conduct() doc = %+v, want %+v", got, doc)
		}
		if outcome != SrokiUpdated {
			t.Fatalf("outcome = %q, want %q", outcome, SrokiUpdated)
		}
		if want := []string{"sroki", "ms"}; !reflect.DeepEqual(trace, want) {
			t.Fatalf("порядок вызовов = %v, want %v", trace, want)
		}
	})

	t.Run("только отложка: шов не вызван, документ создан, SrokiSkipped", func(t *testing.T) {
		cat := &fakeInvCatalog{products: []inventory.Product{invPieceProduct}}
		ms := &fakeInvMS{doc: doc}
		sroki := &fakeInvSroki{}
		uc := New(cat, ms, sroki)

		got, outcome, err := uc.Conduct(ctx, "сопутка", nil, []string{invItem(invPieceCode, 100)})
		if err != nil {
			t.Fatalf("Conduct() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got, doc) {
			t.Fatalf("Conduct() doc = %+v, want %+v", got, doc)
		}
		if outcome != SrokiSkipped {
			t.Fatalf("outcome = %q, want %q", outcome, SrokiSkipped)
		}
		if sroki.calls != 0 {
			t.Fatalf("шов сроков вызван %d раз без общей строки, want 0", sroki.calls)
		}
		if ms.calls != 1 {
			t.Fatalf("CreateInventory вызван %d раз, want 1", ms.calls)
		}
		want := []client.MSInventoryPosition{{AssortmentID: "prod-piece", Quantity: 1, PriceKop: 50000}}
		if !reflect.DeepEqual(ms.positions, want) {
			t.Fatalf("позиции = %+v, want %+v (отложка входит в количество документа)", ms.positions, want)
		}
	})

}

// TestConductSrokiCalls — состав аргументов шва и поведение при сбоях: ошибка шва
// не пускает создание документа (сканы снаружи живы), ошибка МС на шаге документа —
// сроки уже заменены (outcome SrokiUpdated).
func TestConductSrokiCalls(t *testing.T) {
	ctx := context.Background()
	doc := client.MSInventoryDocument{
		ID:   "doc-1",
		Name: "Инвентаризация № 1",
		URL:  "https://api.moysklad.ru/entity/inventory/doc-1",
	}

	t.Run("ошибка шва: документ не создаём, сканы снаружи живы", func(t *testing.T) {
		wantErr := errors.New("сток упал")
		cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
		ms := &fakeInvMS{doc: doc}
		sroki := &fakeInvSroki{err: wantErr}
		uc := New(cat, ms, sroki)

		scans := []string{invItem(invWeightCode, 250)}
		got, outcome, err := uc.Conduct(ctx, "охл", scans, nil)
		if !errors.Is(err, wantErr) {
			t.Fatalf("Conduct() error = %v, want errors.Is %v", err, wantErr)
		}
		if got != (client.MSInventoryDocument{}) {
			t.Fatalf("Conduct() doc = %+v, want пустой", got)
		}
		if outcome != "" {
			t.Fatalf("outcome = %q, want пусто", outcome)
		}
		if ms.calls != 0 {
			t.Fatalf("CreateInventory вызван %d раз при ошибке шва, want 0", ms.calls)
		}
		if sroki.calls != 1 {
			t.Fatalf("шов вызван %d раз, want 1", sroki.calls)
		}
		if len(scans) != 1 || scans[0] != invItem(invWeightCode, 250) {
			t.Fatalf("сканы оператора изменились при ошибке шва: %v", scans)
		}
	})

	t.Run("шов получает коды всего вида и только сканы общей строки", func(t *testing.T) {
		cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct, invNoCodeProduct, invPieceProduct}}
		ms := &fakeInvMS{doc: doc}
		sroki := &fakeInvSroki{}
		uc := New(cat, ms, sroki)

		scans := []string{invItem(invWeightCode, 250)}
		hold := []string{invItem(invPieceCode, 100)}
		_, outcome, err := uc.Conduct(ctx, "охл", scans, hold)
		if err != nil {
			t.Fatalf("Conduct() error = %v, want nil", err)
		}
		if outcome != SrokiUpdated {
			t.Fatalf("outcome = %q, want %q", outcome, SrokiUpdated)
		}
		if want := []string{invWeightCode, invPieceCode}; !reflect.DeepEqual(sroki.codes, want) {
			t.Fatalf("коды шва = %v, want %v (порядок группы, без товара без кода)", sroki.codes, want)
		}
		if !reflect.DeepEqual(sroki.scans, scans) {
			t.Fatalf("сканы шва = %v, want только общую строку %v (отложка не уходит)", sroki.scans, scans)
		}
		if sroki.calls != 1 {
			t.Fatalf("шов вызван %d раз, want 1", sroki.calls)
		}
	})

	t.Run("ошибка МС на шаге документа: outcome всё равно SrokiUpdated", func(t *testing.T) {
		wantErr := errors.New("МС 500")
		cat := &fakeInvCatalog{products: []inventory.Product{invWeightProduct}}
		ms := &fakeInvMS{err: wantErr}
		sroki := &fakeInvSroki{}
		uc := New(cat, ms, sroki)

		got, outcome, err := uc.Conduct(ctx, "охл", []string{invItem(invWeightCode, 250)}, nil)
		if !errors.Is(err, wantErr) {
			t.Fatalf("Conduct() error = %v, want errors.Is %v", err, wantErr)
		}
		if got != (client.MSInventoryDocument{}) {
			t.Fatalf("Conduct() doc = %+v, want пустой", got)
		}
		if outcome != SrokiUpdated {
			t.Fatalf("outcome = %q, want %q (сроки уже заменены)", outcome, SrokiUpdated)
		}
		if sroki.calls != 1 {
			t.Fatalf("шов вызван %d раз, want 1", sroki.calls)
		}
		if ms.calls != 1 {
			t.Fatalf("CreateInventory вызван %d раз, want 1", ms.calls)
		}
	})
}

// 10б. MergeGuestScans: гостевой скан с hold:true идёт в Hold, без hold — в
// Scans; свои отложка-сканы сохраняются в Hold; пустышки пропускаются.
func TestMergeGuestScansHold(t *testing.T) {
	own := []string{invItem(invWeightCode, 250)}
	ownHold := []string{invItem(invPieceCode, 300)}

	t.Run("hold:true → Hold, без hold → Scans, порядок свои→гостевые", func(t *testing.T) {
		guests := []json.RawMessage{
			json.RawMessage(`{"raw":"` + invItem(invWeightCode, 300) + `","seq":1}`),
			json.RawMessage(`{"raw":"` + invItem(invPieceCode, 100) + `","hold":true,"seq":2}`),
			json.RawMessage(`{"raw":"` + invItem(invWeightCode, 400) + `","hold":false,"seq":3}`),
		}
		wantScans := []string{own[0], invItem(invWeightCode, 300), invItem(invWeightCode, 400)}
		wantHold := []string{ownHold[0], invItem(invPieceCode, 100)}

		got, err := MergeGuestScans(own, ownHold, guests)
		if err != nil {
			t.Fatalf("MergeGuestScans() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got.Scans, wantScans) {
			t.Fatalf("Scans = %v, want %v", got.Scans, wantScans)
		}
		if !reflect.DeepEqual(got.Hold, wantHold) {
			t.Fatalf("Hold = %v, want %v", got.Hold, wantHold)
		}
	})

	t.Run("свои отложка-сканы остаются в Hold и без гостей", func(t *testing.T) {
		got, err := MergeGuestScans(own, ownHold, nil)
		if err != nil {
			t.Fatalf("MergeGuestScans() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got.Scans, own) {
			t.Fatalf("Scans = %v, want %v", got.Scans, own)
		}
		if !reflect.DeepEqual(got.Hold, ownHold) {
			t.Fatalf("Hold = %v, want %v", got.Hold, ownHold)
		}
	})

	t.Run("пустышка в hold-строке пропускается", func(t *testing.T) {
		guests := []json.RawMessage{
			json.RawMessage(`{"raw":"   ","hold":true,"seq":1}`),
			json.RawMessage(`{"hold":true,"seq":2}`),
		}
		got, err := MergeGuestScans(own, ownHold, guests)
		if err != nil {
			t.Fatalf("MergeGuestScans() error = %v, want nil", err)
		}
		if !reflect.DeepEqual(got.Scans, own) || !reflect.DeepEqual(got.Hold, ownHold) {
			t.Fatalf("гостевые пустышки попали в набор: Scans=%v Hold=%v", got.Scans, got.Hold)
		}
	})

	t.Run("мусорная hold-строка (не JSON) — ошибка", func(t *testing.T) {
		guests := []json.RawMessage{json.RawMessage(`{"raw":"` + invItem(invPieceCode, 100) + `","hold":true`)}
		if got, err := MergeGuestScans(own, ownHold, guests); err == nil {
			t.Fatalf("MergeGuestScans() = %+v, want ошибку разбора", got)
		}
	})

	t.Run("hold-строка с manual_product_id — ошибка, а не тихая потеря", func(t *testing.T) {
		guests := []json.RawMessage{json.RawMessage(`{"hold":true,"manual_product_id":"prod-piece","seq":1}`)}
		if got, err := MergeGuestScans(own, ownHold, guests); err == nil {
			t.Fatalf("MergeGuestScans() = %+v, want ошибку про строку без штрих-кода", got)
		}
	})
}

package inventory

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// Внутренние коды и даты для сборки валидных сканов в тестах.
const (
	weightCode = "00210003" // весовой товар
	pieceCode  = "11210004" // штучный товар
	prodDate   = "29082026"
	expDate    = "29092026"
)

// item собирает внутренний штрих-код куска: код(8) + вес(5) + даты(8+8).
func item(code string, weightG int) string {
	return fmt.Sprintf("%s%05d%s%s", code, weightG, prodDate, expDate)
}

// box собирает внутренний штрих-код коробки: код(8) + вес(6) + кол-во(3) + даты(8+8).
func box(code string, weightG, qty int) string {
	return fmt.Sprintf("%s%06d%03d%s%s", code, weightG, qty, prodDate, expDate)
}

func TestWeighted(t *testing.T) {
	tests := []struct {
		name string
		uom  string
		want bool
	}{
		{name: "килограммы", uom: "кг", want: true},
		{name: "граммы", uom: "г", want: true},
		{name: "тонны", uom: "т", want: true},
		{name: "регистр и пробелы", uom: "  КГ ", want: true},
		{name: "штуки", uom: "шт", want: false},
		{name: "упаковки", uom: "упак", want: false},
		{name: "пусто", uom: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Weighted(tt.uom); got != tt.want {
				t.Fatalf("Weighted(%q) = %v, want %v", tt.uom, got, tt.want)
			}
		})
	}
}

// Продукты для таблицы успешных сборок.
var (
	weightProduct = Product{
		ID:           "prod-weight",
		InternalCode: weightCode,
		Name:         "Говядина охл",
		UOM:          "кг",
		BuyPriceKop:  new(int64(150000)),
	}
	pieceProduct = Product{
		ID:           "prod-piece",
		InternalCode: pieceCode,
		Name:         "Соус",
		UOM:          "шт",
		BuyPriceKop:  new(int64(50000)),
	}
	noCodeProduct = Product{
		ID:   "prod-nocode",
		Name: "Товар без кода",
		UOM:  "шт",
	}
)

func TestAggregate(t *testing.T) {
	tests := []struct {
		name     string
		products []Product
		scans    []string
		want     Preview
	}{
		{
			name:     "весовой: кусок и коробка суммируются в килограммы",
			products: []Product{weightProduct},
			scans:    []string{item(weightCode, 250), box(weightCode, 2500, 10)},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-weight",
					InternalCode: weightCode,
					Name:         "Говядина охл",
					UOM:          "кг",
					Weighted:     true,
					Fact:         2.75,
					Scans:        2,
					PriceKop:     150000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 2,
			},
		},
		{
			name:     "штучный: куски и коробка с вложениями суммируются в штуках",
			products: []Product{pieceProduct},
			scans:    []string{item(pieceCode, 100), item(pieceCode, 200), box(pieceCode, 12000, 12)},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-piece",
					InternalCode: pieceCode,
					Name:         "Соус",
					UOM:          "шт",
					Weighted:     false,
					Fact:         14,
					Scans:        3,
					PriceKop:     50000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 3,
			},
		},
		{
			name:     "тот же код несколько раз: суммируется, Scanned один",
			products: []Product{weightProduct},
			scans:    []string{item(weightCode, 250), item(weightCode, 250)},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-weight",
					InternalCode: weightCode,
					Name:         "Говядина охл",
					UOM:          "кг",
					Weighted:     true,
					Fact:         0.5,
					Scans:        2,
					PriceKop:     150000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 2,
			},
		},
		{
			name:     "пустой список сканов: все нули, ошибки нет",
			products: []Product{weightProduct, pieceProduct},
			scans:    nil,
			want: Preview{
				Lines: []Line{
					{
						ProductID:    "prod-weight",
						InternalCode: weightCode,
						Name:         "Говядина охл",
						UOM:          "кг",
						Weighted:     true,
						PriceKop:     150000,
						Scanable:     true,
					},
					{
						ProductID:    "prod-piece",
						InternalCode: pieceCode,
						Name:         "Соус",
						UOM:          "шт",
						PriceKop:     50000,
						Scanable:     true,
					},
				},
				Total: 2, Scanned: 0, Scans: 0,
			},
		},
		{
			name:     "товар без кода склада: строка есть, Scanable false",
			products: []Product{noCodeProduct, pieceProduct},
			scans:    []string{item(pieceCode, 100)},
			want: Preview{
				Lines: []Line{
					{
						ProductID: "prod-nocode",
						Name:      "Товар без кода",
						UOM:       "шт",
						Scanable:  false,
					},
					{
						ProductID:    "prod-piece",
						InternalCode: pieceCode,
						Name:         "Соус",
						UOM:          "шт",
						Fact:         1,
						Scans:        1,
						PriceKop:     50000,
						Scanable:     true,
					},
				},
				Total: 2, Scanned: 1, Scans: 1,
			},
		},
		{
			name:     "цена не задана: PriceKop 0",
			products: []Product{{ID: "prod-nil", InternalCode: weightCode, Name: "Без цены", UOM: "кг"}},
			scans:    []string{item(weightCode, 250)},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-nil",
					InternalCode: weightCode,
					Name:         "Без цены",
					UOM:          "кг",
					Weighted:     true,
					Fact:         0.25,
					Scans:        1,
					PriceKop:     0,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 1,
			},
		},
		{
			name:     "скан с пробелами по краям обрезается",
			products: []Product{weightProduct},
			scans:    []string{"  " + item(weightCode, 250) + "  "},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-weight",
					InternalCode: weightCode,
					Name:         "Говядина охл",
					UOM:          "кг",
					Weighted:     true,
					Fact:         0.25,
					Scans:        1,
					PriceKop:     150000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Aggregate(tt.products, tt.scans, nil)
			if err != nil {
				t.Fatalf("Aggregate() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Aggregate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAggregateErrors(t *testing.T) {
	tests := []struct {
		name     string
		products []Product
		scans    []string
		wantErr  error
	}{
		{
			name:     "чужой код: товара с таким кодом нет в группе",
			products: []Product{weightProduct},
			scans:    []string{item("21210005", 300)},
			wantErr:  ErrScanNotInGroup,
		},
		{
			name:     "чужая длина: штрих-код поставщика",
			products: []Product{weightProduct},
			scans:    []string{"1234567890"},
			wantErr:  ErrScanInvalid,
		},
		{
			name:     "пустая строка",
			products: []Product{weightProduct},
			scans:    []string{""},
			wantErr:  ErrScanInvalid,
		},
		{
			name:     "только пробелы",
			products: []Product{weightProduct},
			scans:    []string{"   "},
			wantErr:  ErrScanInvalid,
		},
		{
			name:     "внутренняя длина, но невалидные данные (нулевой вес)",
			products: []Product{weightProduct},
			scans:    []string{weightCode + "00000" + prodDate + expDate},
			wantErr:  ErrScanInvalid,
		},
		{
			name:     "первый плохой скан решает: хороший до него не спасает",
			products: []Product{weightProduct},
			scans:    []string{item(weightCode, 250), "junk"},
			wantErr:  ErrScanInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Aggregate(tt.products, tt.scans, nil)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Aggregate() error = %v, want errors.Is %v", err, tt.wantErr)
			}
		})
	}
}

func TestPositions(t *testing.T) {
	tests := []struct {
		name    string
		preview Preview
		want    []Position
	}{
		{
			name: "порядок сохраняется, непросканированные идут с нулём",
			preview: Preview{
				Lines: []Line{
					{ProductID: "a", Fact: 2.75, PriceKop: 100},
					{ProductID: "b", Fact: 0, PriceKop: 0},
					{ProductID: "c", Fact: 14, PriceKop: 50000},
				},
			},
			want: []Position{
				{ProductID: "a", Quantity: 2.75, PriceKop: 100},
				{ProductID: "b", Quantity: 0, PriceKop: 0},
				{ProductID: "c", Quantity: 14, PriceKop: 50000},
			},
		},
		{
			name:    "пустой отчёт — пустой документ",
			preview: Preview{},
			want:    []Position{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Positions(tt.preview)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Positions() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPositionsFromAggregate(t *testing.T) {
	preview, err := Aggregate([]Product{weightProduct, noCodeProduct}, []string{item(weightCode, 250)}, nil)
	if err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}
	want := []Position{
		{ProductID: "prod-weight", Quantity: 0.25, PriceKop: 150000},
		{ProductID: "prod-nocode", Quantity: 0, PriceKop: 0},
	}
	if got := Positions(preview); !reflect.DeepEqual(got, want) {
		t.Fatalf("Positions() = %+v, want %+v", got, want)
	}
}

// TestWeightQuantity — граммы из штрих-кода переводятся в единицы учёта
// товара (uom), а не всегда в килограммы: в системе живут шт/кг, но правило
// должно быть верным и для г/т, иначе количество в документе МС разойдётся.
func TestWeightQuantity(t *testing.T) {
	tests := []struct {
		name  string
		uom   string
		grams int64
		want  float64
	}{
		{name: "килограммы", uom: "кг", grams: 1234, want: 1.234},
		{name: "граммы — как есть", uom: "г", grams: 1234, want: 1234},
		{name: "тонны", uom: "т", grams: 1500000, want: 1.5},
		{name: "регистр и пробелы", uom: "  КГ ", grams: 500, want: 0.5},
		{name: "штучный uom — ноль", uom: "шт", grams: 500, want: 0},
		{name: "пустой uom — ноль", uom: "", grams: 500, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WeightQuantity(tt.uom, tt.grams); got != tt.want {
				t.Errorf("WeightQuantity(%q, %d) = %v, want %v", tt.uom, tt.grams, got, tt.want)
			}
		})
	}
}

// TestWeightDecimals — знаков после точки в показе веса по uom.
func TestWeightDecimals(t *testing.T) {
	if got := WeightDecimals("г"); got != 0 {
		t.Errorf("WeightDecimals(г) = %d, want 0", got)
	}
	for _, uom := range []string{"кг", "т", " КГ "} {
		if got := WeightDecimals(uom); got != 3 {
			t.Errorf("WeightDecimals(%q) = %d, want 3", uom, got)
		}
	}
}

// TestAggregateGramsUOM — весовой товар в граммах: факт в граммах, не в кг.
func TestAggregateGramsUOM(t *testing.T) {
	products := []Product{{ID: "p-1", InternalCode: "10210001", Name: "Специи", UOM: "г"}}
	preview, err := Aggregate(products, []string{item("10210001", 250)}, nil)
	if err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}
	if got := preview.Lines[0].Fact; got != 250 {
		t.Errorf("Fact = %v, want 250 (граммы в единицах товара)", got)
	}
	if got := preview.Lines[0].Weighted; !got {
		t.Error("Weighted = false, want true")
	}
}

// TestAggregateHold — сканы «Отложки» ложатся в HoldFact/HoldScans отдельно от
// общей строки; Total складывает обе колонки, а позиция считается
// просканированной при скане в ЛЮБОЙ из строк (Scanned), при этом Scans и
// HoldScans считают сканы каждой колонки раздельно.
func TestAggregateHold(t *testing.T) {
	tests := []struct {
		name     string
		products []Product
		scans    []string
		hold     []string
		want     Preview
	}{
		{
			name:     "весовой: общая и отложка раздельно, Total складывает",
			products: []Product{weightProduct},
			scans:    []string{item(weightCode, 250)},
			hold:     []string{item(weightCode, 500)},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-weight",
					InternalCode: weightCode,
					Name:         "Говядина охл",
					UOM:          "кг",
					Weighted:     true,
					Fact:         0.25,
					Scans:        1,
					HoldFact:     0.5,
					HoldScans:    1,
					PriceKop:     150000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 1, HoldScans: 1,
			},
		},
		{
			name:     "штучный: сканы только в отложке — позиция просканирована, Scans 0",
			products: []Product{pieceProduct},
			hold:     []string{item(pieceCode, 100), item(pieceCode, 200)},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-piece",
					InternalCode: pieceCode,
					Name:         "Соус",
					UOM:          "шт",
					HoldFact:     2,
					HoldScans:    2,
					PriceKop:     50000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 0, HoldScans: 2,
			},
		},
		{
			name:     "разные позиции: одна в общей, другая в отложке — Scanned обе",
			products: []Product{weightProduct, pieceProduct},
			scans:    []string{item(weightCode, 250)},
			hold:     []string{item(pieceCode, 100)},
			want: Preview{
				Lines: []Line{
					{
						ProductID:    "prod-weight",
						InternalCode: weightCode,
						Name:         "Говядина охл",
						UOM:          "кг",
						Weighted:     true,
						Fact:         0.25,
						Scans:        1,
						PriceKop:     150000,
						Scanable:     true,
					},
					{
						ProductID:    "prod-piece",
						InternalCode: pieceCode,
						Name:         "Соус",
						UOM:          "шт",
						HoldFact:     1,
						HoldScans:    1,
						PriceKop:     50000,
						Scanable:     true,
					},
				},
				Total: 2, Scanned: 2, Scans: 1, HoldScans: 1,
			},
		},
		{
			name:     "отложка пустая — поведение как раньше",
			products: []Product{weightProduct},
			scans:    []string{item(weightCode, 250)},
			want: Preview{
				Lines: []Line{{
					ProductID:    "prod-weight",
					InternalCode: weightCode,
					Name:         "Говядина охл",
					UOM:          "кг",
					Weighted:     true,
					Fact:         0.25,
					Scans:        1,
					PriceKop:     150000,
					Scanable:     true,
				}},
				Total: 1, Scanned: 1, Scans: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Aggregate(tt.products, tt.scans, tt.hold)
			if err != nil {
				t.Fatalf("Aggregate() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Aggregate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestAggregateHoldErrors — плохой скан «Отложки» валидируется тем же правилом,
// что и общая строка (чужой код / битый скан). Порядок разбора детерминирован:
// общая строка идёт первой, и её ошибка решает, даже если в отложке свой мусор —
// разные сентинелы на входе доказывают, что сработал именно первый список.
func TestAggregateHoldErrors(t *testing.T) {
	foreign := item("21210005", 300) // код разобран, но товара с ним нет в группе

	tests := []struct {
		name    string
		scans   []string
		hold    []string
		wantErr error
	}{
		{
			name:    "чужой код в отложке",
			scans:   []string{item(weightCode, 250)},
			hold:    []string{foreign},
			wantErr: ErrScanNotInGroup,
		},
		{
			name:    "битый скан в отложке",
			scans:   []string{item(weightCode, 250)},
			hold:    []string{"junk-hold"},
			wantErr: ErrScanInvalid,
		},
		{
			name:    "пустой скан в отложке",
			hold:    []string{""},
			wantErr: ErrScanInvalid,
		},
		{
			name:    "порядок: чужой код в общей решает над мусором в отложке",
			scans:   []string{foreign},     // ErrScanNotInGroup
			hold:    []string{"junk-hold"}, // ErrScanInvalid, если бы отложка шла первой
			wantErr: ErrScanNotInGroup,
		},
		{
			name:    "порядок: мусор в общей решает над чужим кодом в отложке",
			scans:   []string{"junk-own"}, // ErrScanInvalid
			hold:    []string{foreign},    // ErrScanNotInGroup, если бы отложка шла первой
			wantErr: ErrScanInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Aggregate([]Product{weightProduct}, tt.scans, tt.hold)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Aggregate() error = %v, want errors.Is %v", err, tt.wantErr)
			}
		})
	}
}

// TestLineTotal — итог позиции = общая строка + отложка.
func TestLineTotal(t *testing.T) {
	tests := []struct {
		name string
		line Line
		want float64
	}{
		{name: "общая и отложка", line: Line{Fact: 2.75, HoldFact: 0.5}, want: 3.25},
		{name: "только отложка", line: Line{HoldFact: 1}, want: 1},
		{name: "пусто", line: Line{}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.line.Total(); got != tt.want {
				t.Fatalf("Total() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPositionsTotalWithHold — количество в документ МС = итог (общая + отложка),
// отложка отдельной строкой не идёт.
func TestPositionsTotalWithHold(t *testing.T) {
	preview, err := Aggregate(
		[]Product{weightProduct, pieceProduct},
		[]string{item(weightCode, 250)},
		[]string{item(weightCode, 500), item(pieceCode, 100)},
	)
	if err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}
	want := []Position{
		{ProductID: "prod-weight", Quantity: 0.75, PriceKop: 150000},
		{ProductID: "prod-piece", Quantity: 1, PriceKop: 50000},
	}
	if got := Positions(preview); !reflect.DeepEqual(got, want) {
		t.Fatalf("Positions() = %+v, want %+v", got, want)
	}
}

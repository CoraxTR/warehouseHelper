package registry

import (
	"testing"

	"github.com/xuri/excelize/v2"
)

// buildTestRegistry собирает xlsx-реестр в памяти. Первый срез — строки,
// внутри — значения по столбцам (индекс 0 = столбец A).
func buildTestRegistry(t *testing.T, rows [][]any) []byte {
	t.Helper()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	for i, row := range rows {
		for j, v := range row {
			cell, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatalf("cell name: %v", err)
			}

			if err := f.SetCellValue(sheet, cell, v); err != nil {
				t.Fatalf("set cell %s: %v", cell, err)
			}
		}
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("write buffer: %v", err)
	}

	return buf.Bytes()
}

// blankRow возвращает строку с заглушкой в столбце A, чтобы excelize не
// выбросил пустую строку из результата GetRows.
func blankRow() []any {
	return []any{" "}
}

func TestParseRefGoCheckFile(t *testing.T) {
	data := buildTestRegistry(t, [][]any{
		{"Реестр", "Наименование"}, // строка 1 — шапка
		{"Номер", "Наименование"},  // строка 2 — шапка
		blankRow(),                  // строка 3 — пустая
		{"", "", "", "", "Забор 1"}, // строка 4 — забор
		{"", "", "", "", "1001", "", "", "", "", "", "", "", "100,00", "50,00", "500,00", "", "", "", "", "", "", "7,50"}, // строка 5 — заказ
		{"", "", "", "", "1002", "", "", "", "", "", "", "", 200.5, "", "1 000,00", "", "", "", "", "", "", "10,00"},      // строка 6 — заказ
		blankRow(),               // строка 7
		blankRow(),               // строка 8
		blankRow(),               // строка 9
		{"", "", "", "", "1003"}, // строка 10 — не должна прочитаться
	})

	imp := NewxlsxImporter()

	retrieves, orders, err := imp.ParseRefGoCheckFile(data)
	if err != nil {
		t.Fatalf("ParseRefGoCheckFile error: %v", err)
	}

	if retrieves != 1 {
		t.Errorf("retrieves = %d, want 1", retrieves)
	}

	if len(orders) != 2 {
		t.Fatalf("orders len = %d, want 2", len(orders))
	}

	first, ok := orders[5]
	if !ok {
		t.Fatalf("order with row 5 not found, keys: %v", mapKeys(orders))
	}
	if first.RefGoNumber != "1001" {
		t.Errorf("RefGoNumber = %q, want 1001", first.RefGoNumber)
	}
	if first.CashFact != 10000 {
		t.Errorf("CashFact = %d, want 10000", first.CashFact)
	}
	if first.TerminalFact != 5000 {
		t.Errorf("TerminalFact = %d, want 5000", first.TerminalFact)
	}
	if first.DeliveryPrice != 50000 {
		t.Errorf("DeliveryPrice = %d, want 50000", first.DeliveryPrice)
	}
	if first.TaxFact != 750 {
		t.Errorf("TaxFact = %d, want 750", first.TaxFact)
	}

	second, ok := orders[6]
	if !ok {
		t.Fatal("order with row 6 not found")
	}
	if second.RefGoNumber != "1002" {
		t.Errorf("RefGoNumber = %q, want 1002", second.RefGoNumber)
	}
	if second.CashFact != 20050 {
		t.Errorf("CashFact = %d, want 20050", second.CashFact)
	}
	if second.DeliveryPrice != 100000 {
		t.Errorf("DeliveryPrice = %d, want 100000", second.DeliveryPrice)
	}
	if second.TaxFact != 1000 {
		t.Errorf("TaxFact = %d, want 1000", second.TaxFact)
	}

	if _, ok := orders[10]; ok {
		t.Error("order on row 10 read despite three empty rows before it")
	}
}

func TestParseRefGoCheckFileStopsAfterThreeEmptyRows(t *testing.T) {
	data := buildTestRegistry(t, [][]any{
		{"Реестр", "Наименование"},
		{"Номер", "Наименование"},
		{"", "", "", "", "1001", "", "", "", "", "", "", "", "100,00"},
		blankRow(),
		blankRow(),
		blankRow(),
		{"", "", "", "", "1002"},
	})

	imp := NewxlsxImporter()

	_, orders, err := imp.ParseRefGoCheckFile(data)
	if err != nil {
		t.Fatalf("ParseRefGoCheckFile error: %v", err)
	}

	if len(orders) != 1 {
		t.Fatalf("orders len = %d, want 1", len(orders))
	}

	if _, ok := orders[3]; !ok {
		t.Error("order on row 3 not found")
	}
}

func TestParseRefGoCheckFileInvalidFile(t *testing.T) {
	imp := NewxlsxImporter()

	_, _, err := imp.ParseRefGoCheckFile([]byte("not an xlsx"))
	if err == nil {
		t.Fatal("expected error for invalid file")
	}
}

func TestCellToKopecks(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"100,00", 10000},
		{"1 234,50", 123450},
		{"1234.5", 123450},
		{"200.5", 20050},
		{"0", 0},
		{"", 0},
		{"abc", 0},
	}

	for _, tt := range tests {
		if got := cellToKopecks(tt.in); got != tt.want {
			t.Errorf("cellToKopecks(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestValidateTaxFact(t *testing.T) {
	tests := []struct {
		name             string
		order            RefGoCheckAgainstOrder
		cashTax, cardTax float64
		wantDiff         float64
	}{
		{
			name:     "cash only exact",
			order:    RefGoCheckAgainstOrder{CashFact: 10000, TaxFact: 500},
			cashTax:  5,
			cardTax:  5,
			wantDiff: 0,
		},
		{
			name:     "cash and terminal exact",
			order:    RefGoCheckAgainstOrder{CashFact: 10000, TerminalFact: 20000, TaxFact: 1500},
			cashTax:  5,
			cardTax:  5,
			wantDiff: 0,
		},
		{
			name:     "within tolerance one kopeck",
			order:    RefGoCheckAgainstOrder{CashFact: 10000, TaxFact: 501},
			cashTax:  5,
			cardTax:  5,
			wantDiff: 0,
		},
		{
			name:     "mismatch one ruble",
			order:    RefGoCheckAgainstOrder{CashFact: 10000, TaxFact: 600},
			cashTax:  5,
			cardTax:  5,
			wantDiff: 1.0,
		},
		{
			name:     "fractional percents cash 1% card 2.5%",
			order:    RefGoCheckAgainstOrder{CashFact: 10000, TerminalFact: 20000, TaxFact: 600},
			cashTax:  1,
			cardTax:  2.5,
			wantDiff: 0,
		},
		{
			name:     "no facts",
			order:    RefGoCheckAgainstOrder{},
			cashTax:  5,
			cardTax:  5,
			wantDiff: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.order.ValidateTaxFact(tt.cashTax, tt.cardTax); got != tt.wantDiff {
				t.Errorf("ValidateTaxFact() = %v, want %v", got, tt.wantDiff)
			}
		})
	}
}

func mapKeys(m map[int64]RefGoCheckAgainstOrder) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

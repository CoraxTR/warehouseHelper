package scanmatch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/innercode"
)

// Тесты общего ядра сверки: правила перенесены из internal/returns без
// изменений — строгий вес, построчное гашение, отбор строк ожидания.

const (
	codeA = "00100101"
	codeD = "00200101"
	prodA = "a1c09121-7ef5-11e5-7a40-e897001b4cc6"
	prodD = "d1c09121-7ef5-11e5-7a40-e897001b4cc6"
)

// etiketa — этикетка куска (29 цифр) каноническим кодировщиком innercode.
func etiketa(t *testing.T, code string, weightG int64) string {
	t.Helper()
	raw, err := innercode.EncodeItem(code, weightG,
		time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EncodeItem: %v", err)
	}
	return raw
}

func TestParseScan_Item(t *testing.T) {
	scan, err := ParseScan(etiketa(t, codeA, 657))
	if err != nil {
		t.Fatalf("ParseScan: %v", err)
	}
	if scan.InternalCode != codeA || scan.WeightG != 657 {
		t.Errorf("scan = %+v, want код %s и вес 657", scan, codeA)
	}
	if got := scan.ExpDate.Format(time.DateOnly); got != "2026-09-15" {
		t.Errorf("срок = %s, want 2026-09-15", got)
	}
}

// Коробка (33) и чужой штрих-код в сверку не идут.
func TestParseScan_BoxAndForeignRejected(t *testing.T) {
	box := codeA + "002500" + "002" + "01092026" + "15092026"
	if _, err := ParseScan(box); !errors.Is(err, ErrBox) {
		t.Errorf("коробка: err = %v, want ErrBox", err)
	}
	if _, err := ParseScan("4600000000017"); err == nil {
		t.Error("чужой штрих-код: want ошибку разбора")
	}
}

// Весовую строку гасит ровно один скан с тем же весом; допуска нет.
func TestMatch_WeightStrictNoTolerance(t *testing.T) {
	expected := []Expected{{Idx: 0, ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	if _, err := Match([]string{etiketa(t, codeA, 654)}, expected); err == nil {
		t.Fatal("скан 654 г на строку 657 г: want ValidationError")
	} else {
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "не подходит ни одной строке") {
			t.Fatalf("want ValidationError «не подходит ни одной строке», got %v", err)
		}
	}

	units, err := Match([]string{etiketa(t, codeA, 657)}, expected)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(units) != 1 || units[0].Row.Idx != 0 || units[0].WeightG != 657 {
		t.Fatalf("units = %+v, want один скан в строку 0", units)
	}
}

// Две строки одного кода с одинаковым весом различимы только порядком.
func TestMatch_DuplicateWeightsByOrder(t *testing.T) {
	expected := []Expected{
		{Idx: 0, ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657},
		{Idx: 1, ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657},
	}

	if _, err := Match([]string{etiketa(t, codeA, 657), etiketa(t, codeA, 657)}, expected); err != nil {
		t.Fatalf("две строки 657 г двумя сканами: %v", err)
	}

	_, err := Match([]string{etiketa(t, codeA, 657), etiketa(t, codeA, 657), etiketa(t, codeA, 657)}, expected)
	if err == nil {
		t.Fatal("третий скан на две закрытые строки: want ValidationError")
	}
}

// Штучную строку «5 шт» гасят пять сканов; недобор — отказ.
func TestMatch_PiecesByCount(t *testing.T) {
	expected := []Expected{{Idx: 0, ProductID: prodD, InternalCode: codeD, Name: "Соус", Weighted: false, ExpectedQty: 5}}

	scans := make([]string, 0, 5)
	for range 5 {
		scans = append(scans, etiketa(t, codeD, 1))
	}
	if _, err := Match(scans, expected); err != nil {
		t.Fatalf("5 сканов на «5 шт»: %v", err)
	}

	_, err := Match(scans[:4], expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "вес не сходится") {
		t.Fatalf("want ValidationError «вес не сходится» на недобор, got %v", err)
	}
}

// Отбор строк ожидания: только отложенные (quantity == reserve), с кодом
// склада и ненулевым количеством; порядок строк задаёт Idx.
func TestBuildExpected_Filters(t *testing.T) {
	cands := []Candidate{
		{ProductID: prodA, Name: "Чак ролл", Quantity: 0.657, Reserve: 0.657}, // весовой, отложен
		{ProductID: prodD, Name: "Соус", Quantity: 2, Reserve: 2},             // штучный, отложен
		{ProductID: prodA, Name: "Чак ролл", Quantity: 0.4, Reserve: 0},       // не отложен
		{ProductID: "no-code", Name: "Без кода", Quantity: 1, Reserve: 1},     // без кода склада
		{ProductID: "empty", Name: "Ноль", Quantity: 0, Reserve: 0},           // нулевая строка
	}
	products := map[string]CatalogProduct{
		prodA:     {ProductID: prodA, InternalCode: codeA, Weighted: true},
		prodD:     {ProductID: prodD, InternalCode: codeD, Weighted: false},
		"no-code": {ProductID: "no-code", InternalCode: "", Weighted: true},
		"empty":   {ProductID: "empty", InternalCode: codeD, Weighted: true},
	}

	expected := BuildExpected(cands, products)
	if len(expected) != 2 {
		t.Fatalf("ожиданий = %d (%+v), want 2", len(expected), expected)
	}
	if expected[0].Idx != 0 || expected[0].ExpectedQty != 657 || !expected[0].Weighted {
		t.Errorf("строка 0 = %+v, want вес 657 г весовой", expected[0])
	}
	if expected[1].Idx != 1 || expected[1].ExpectedQty != 2 || expected[1].Weighted {
		t.Errorf("строка 1 = %+v, want 2 шт штучная", expected[1])
	}

	if got := BuildExpected(cands[2:], products); len(got) != 0 {
		t.Errorf("без отложенных строк want пусто, got %+v", got)
	}
}

// Существующий лот: сканы одного товара с одним сроком складываются, с разными
// сроками — разные лоты.
func TestAggregateLots_GroupByProductAndDate(t *testing.T) {
	exp := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)
	units := []ScannedUnit{
		{Row: Expected{ProductID: prodD}, ExpDate: exp},
		{Row: Expected{ProductID: prodD}, ExpDate: exp},
		{Row: Expected{ProductID: prodD}, ExpDate: exp.AddDate(0, 0, 3)},
	}

	lots := AggregateLots(units)
	if len(lots) != 2 {
		t.Fatalf("лотов = %d (%+v), want 2", len(lots), lots)
	}
	var total int64
	for _, l := range lots {
		total += l.Qty
	}
	if total != 3 {
		t.Errorf("сумма лотов = %d, want 3", total)
	}
}

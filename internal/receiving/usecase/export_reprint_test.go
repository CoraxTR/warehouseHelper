package usecase

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/receiving"

	"github.com/xuri/excelize/v2"
)

// Фикстуры перепечатки: кусок 29 цифр (код 8 + вес 5 + даты 8+8) и коробка
// 33 (код 8 + вес 6 + кол-во 3 + даты 8+8) — те же, что на странице приёмки.
const (
	reprintItemCode = "00210003012502908202629092026"     // стейк, 1250 г
	reprintPieceRaw = "10210003000012908202629092026"     // штучный, заглушка 1 г
	reprintOtherRaw = "00210004000502908202629092026"     // фарш, 50 г
	reprintBoxCode  = "002100030250000102908202629092026" // коробка: 25000 г, 10 вложений
)

// reprintRefs — каталог фикстуры: два весовых товара и один штучный.
func reprintRefs() map[string]receiving.ProductRef {
	return map[string]receiving.ProductRef{
		"00210003": {ProductID: "p1", InternalCode: "00210003", Name: "Стейк Рибай", Weighted: true},
		"00210004": {ProductID: "p2", InternalCode: "00210004", Name: "Фарш", Weighted: true},
		"10210003": {ProductID: "p3", InternalCode: "10210003", Name: "Пакет", Weighted: false},
	}
}

// reprintUC — сценарий с подставным хранилищем: каталог фикстуры, остатки
// и веса не нужны (перепечатка ничего не пишет).
func reprintUC(catalog map[string]receiving.ProductRef) *ReceivingUseCase {
	return NewReceivingUseCase(&stubReceiveRepo{catalog: catalog}, nil, nil)
}

// TestReprintCodes_FilterByVariant — страница шлёт все сканы, а в файл идут
// только коды под выбранный вид наклейки: куски (29) — для длинной и короткой,
// коробки (33) — для наклейки коробки.
func TestReprintCodes_FilterByVariant(t *testing.T) {
	t.Parallel()

	scans := []string{reprintItemCode, reprintBoxCode, " " + reprintOtherRaw + " "}

	for _, tc := range []struct {
		name    string
		variant ReprintVariant
		want    []string
	}{
		{
			name:    "длинная: только куски",
			variant: ReprintLong,
			want:    []string{reprintItemCode, reprintOtherRaw},
		},
		{
			name:    "короткая: только куски",
			variant: ReprintShort,
			want:    []string{reprintItemCode, reprintOtherRaw},
		},
		{
			name:    "коробка: только 33",
			variant: ReprintBox,
			want:    []string{reprintBoxCode},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			codes, err := reprintCodes(scans, tc.variant)
			if err != nil {
				t.Fatalf("reprintCodes: %v", err)
			}
			if len(codes) != len(tc.want) {
				t.Fatalf("кодов %d, want %d", len(codes), len(tc.want))
			}
			for i, want := range tc.want {
				got, err := innercode.Parse(want)
				if err != nil {
					t.Fatalf("фикстура %q: %v", want, err)
				}
				if codes[i] != got {
					t.Errorf("код %d = %+v, want %+v", i, codes[i], got)
				}
			}
		})
	}
}

// TestReprintCodes_Errors — пустой список нужного вида и неизвестный вид
// отклоняются с понятным текстом, а неразобранный скан — с указанием скана.
func TestReprintCodes_Errors(t *testing.T) {
	t.Parallel()

	if _, err := reprintCodes([]string{reprintBoxCode}, ReprintLong); err == nil ||
		!strings.Contains(err.Error(), "кусков (29 цифр)") {
		t.Errorf("нет кусков: err = %v, want упоминание кусков", err)
	}
	if _, err := reprintCodes([]string{reprintItemCode}, ReprintBox); err == nil ||
		!strings.Contains(err.Error(), "коробок (33 цифры)") {
		t.Errorf("нет коробок: err = %v, want упоминание коробок", err)
	}
	if _, err := reprintCodes([]string{reprintItemCode}, "wide"); err == nil ||
		!strings.Contains(err.Error(), "неизвестный вид наклейки") {
		t.Errorf("чужой вид: err = %v, want отказ по виду наклейки", err)
	}
	if _, err := reprintCodes([]string{"12345"}, ReprintLong); err == nil ||
		!strings.Contains(err.Error(), `"12345"`) {
		t.Errorf("мусор: err = %v, want упоминание скана", err)
	}
}

// TestReprintCatalogRefs_MissingCode — код, которого нет в каталоге, отклоняет
// батч и назван в отказе: наклейка без наименования не печатается.
func TestReprintCatalogRefs_MissingCode(t *testing.T) {
	t.Parallel()

	codes, err := reprintCodes([]string{reprintItemCode, "00210009000502908202629092026"}, ReprintLong)
	if err != nil {
		t.Fatalf("reprintCodes: %v", err)
	}

	uc := reprintUC(reprintRefs())
	if _, err := uc.reprintCatalogRefs(context.Background(), codes); err == nil ||
		!strings.Contains(err.Error(), "нет в каталоге: 00210009") {
		t.Errorf("err = %v, want отказ с кодом 00210009", err)
	}

	// Все коды известны — отказ не нужен.
	ok, err := reprintCodes([]string{reprintItemCode, reprintOtherRaw}, ReprintLong)
	if err != nil {
		t.Fatalf("reprintCodes: %v", err)
	}
	refs, err := uc.reprintCatalogRefs(context.Background(), ok)
	if err != nil {
		t.Fatalf("reprintCatalogRefs: %v", err)
	}
	if refs["00210003"].Name != "Стейк Рибай" {
		t.Errorf("refs[00210003] = %+v, want Стейк Рибай", refs["00210003"])
	}
}

// TestReprintUnits_RoundTrip — перепечатанный код совпадает со сканом: вес
// уходит в код тот же, у штучного — та же заглушка 1 г (иначе наклейка
// перестала бы распознаваться как тот же кусок).
func TestReprintUnits_RoundTrip(t *testing.T) {
	t.Parallel()

	codes, err := reprintCodes([]string{reprintItemCode, reprintPieceRaw}, ReprintLong)
	if err != nil {
		t.Fatalf("reprintCodes: %v", err)
	}
	units := reprintUnits(codes, reprintRefs())
	if len(units) != 2 {
		t.Fatalf("кусков %d, want 2", len(units))
	}

	for i, want := range []string{reprintItemCode, reprintPieceRaw} {
		got, ok := labelUnitCode(units[i])
		if !ok {
			t.Fatalf("кусок %d: код не собран", i)
		}
		if got != want {
			t.Errorf("код %d = %s, want %s (наклейка обязана сканироваться как скан)", i, got, want)
		}
	}
	if got := labelCaption(units[1]); strings.Contains(got, "вес:") {
		t.Errorf("подпись штучного = %q, вес печатать не нужно", got)
	}
	if got := labelCaption(units[0]); !strings.Contains(got, "вес: 1250") {
		t.Errorf("подпись весового = %q, want вес 1250", got)
	}
}

// TestReprintBoxes_Fields — 33-значный скан раскладывается в данные наклейки
// коробки: код, вес, число вложений и обе даты из самого кода.
func TestReprintBoxes_Fields(t *testing.T) {
	t.Parallel()

	codes, err := reprintCodes([]string{reprintBoxCode}, ReprintBox)
	if err != nil {
		t.Fatalf("reprintCodes: %v", err)
	}
	boxes := reprintBoxes(codes, reprintRefs())
	if len(boxes) != 1 {
		t.Fatalf("коробок %d, want 1", len(boxes))
	}
	b := boxes[0]
	if b.InternalCode != "00210003" || b.ProductName != "Стейк Рибай" || !b.Weighted {
		t.Errorf("шапка коробки = %+v, want код 00210003 / Стейк Рибай / весовой", b)
	}
	if b.WeightG != 25000 || b.Qty != 10 {
		t.Errorf("вес/вложения = %d/%d, want 25000/10", b.WeightG, b.Qty)
	}
	if b.ProducedOn == nil || b.BestBefore == nil {
		t.Fatalf("даты = %v/%v, want обе из кода", b.ProducedOn, b.BestBefore)
	}
	if got := b.ProducedOn.Format("02.01.2006") + "—" + b.BestBefore.Format("02.01.2006"); got != "29.08.2026—29.09.2026" {
		t.Errorf("даты = %s, want 29.08.2026—29.09.2026", got)
	}
}

// TestExportReprint_LongDuplicates — сквозной прогон «длинной» наклейки: дубли
// не схлопываются (три скана — три этикетки), код и подпись каждой — как на
// исходной этикетке. Файл пишется в tempdir пакета, поэтому рабочий каталог
// теста подменяется на временный (tempdir.Dir = "../temp").
func TestExportReprint_LongDuplicates(t *testing.T) {
	reprintWorkdir(t)

	uc := reprintUC(reprintRefs())
	path, err := uc.ExportReprint(context.Background(), []string{reprintItemCode, reprintItemCode, reprintOtherRaw}, ReprintLong)
	if err != nil {
		t.Fatalf("ExportReprint: %v", err)
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("открыть %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	sheet := f.GetSheetName(0)

	wantCodes := []string{reprintItemCode, reprintItemCode, reprintOtherRaw}
	for i, want := range wantCodes {
		axis := "B" + strconv.Itoa(1+2*i)
		if got, _ := f.GetCellValue(sheet, axis); got != want {
			t.Errorf("%s = %q, want %q (дубль обязан печататься дважды)", axis, got, want)
		}
	}
	if got, _ := f.GetCellValue(sheet, "B2"); !strings.Contains(got, "Стейк Рибай") || !strings.Contains(got, "вес: 1250") {
		t.Errorf("подпись этикетки = %q, want наименование и вес", got)
	}
}

// TestExportReprint_Short — сквозной прогон «короткой» наклейки: код идёт в
// верхнюю строку пары, две наклейки на печатную страницу (раскладку проверяет
// отдельный тест билдера).
func TestExportReprint_Short(t *testing.T) {
	reprintWorkdir(t)

	uc := reprintUC(reprintRefs())
	path, err := uc.ExportReprint(context.Background(), []string{reprintBoxCode, reprintItemCode}, ReprintShort)
	if err != nil {
		t.Fatalf("ExportReprint: %v", err)
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("открыть %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	sheet := f.GetSheetName(0)

	// Коробка в «короткой» наклейке не участвует: печатается только кусок.
	if got, _ := f.GetCellValue(sheet, "B1"); got != reprintItemCode {
		t.Errorf("B1 = %q, want код куска %q", got, reprintItemCode)
	}
	if got, _ := f.GetCellValue(sheet, "C1"); got != "" {
		t.Errorf("C1 = %q, want пусто: кусок один", got)
	}
}

// TestExportReprint_Box — сквозной прогон наклейки коробки: 33-значный скан
// перепечатывается тем же кодом (вес и вложения — из скана), плюс
// наименование из каталога.
func TestExportReprint_Box(t *testing.T) {
	reprintWorkdir(t)

	uc := reprintUC(reprintRefs())
	path, err := uc.ExportReprint(context.Background(), []string{reprintBoxCode}, ReprintBox)
	if err != nil {
		t.Fatalf("ExportReprint: %v", err)
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("открыть %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	sheet := f.GetSheetName(0)

	if got, _ := f.GetCellValue(sheet, "B2"); got != reprintBoxCode {
		t.Errorf("B2 = %q, want код коробки %q", got, reprintBoxCode)
	}
	if got, _ := f.GetCellValue(sheet, "B3"); got != "Стейк Рибай" {
		t.Errorf("B3 = %q, want наименование из каталога", got)
	}
	if got, _ := f.GetCellValue(sheet, "B4"); !strings.Contains(got, "25") || !strings.Contains(got, "10") {
		t.Errorf("B4 = %q, want вес 25 кг и 10 вложений", got)
	}
}

// reprintWorkdir подменяет рабочий каталог теста на временный: tempdir.Dir
// ("../temp") — константа пакета, а ExportReprint пишет файл именно туда.
func reprintWorkdir(t *testing.T) {
	t.Helper()

	work := t.TempDir()
	for _, dir := range []string{"app", "temp"} {
		if err := os.MkdirAll(filepath.Join(work, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Join(work, "app"))
}

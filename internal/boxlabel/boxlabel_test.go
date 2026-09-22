package boxlabel

import (
	"archive/zip"
	"bytes"
	"errors"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/tempdir"

	"github.com/xuri/excelize/v2"
)

// d — дата 2026 года: в фикстурах наклеек других лет нет.
func d(m time.Month) time.Time {
	return time.Date(2026, m, 29, 0, 0, 0, 0, time.UTC)
}

// weightBox — весовая коробка: 10 вложений по 250 г, код товара 00210003.
func weightBox() Box {
	return Box{
		InternalCode: "00210003",
		ProductName:  "Говядина охл.",
		Weighted:     true,
		WeightG:      2500,
		Qty:          10,
		ProducedOn:   d(time.August),
		BestBefore:   d(time.September),
	}
}

// pieceBox — штучная коробка: веса нет, в код идёт заглушка 1 г.
func pieceBox() Box {
	return Box{
		InternalCode: "00210010",
		ProductName:  "Хлеб Бородинский",
		Weighted:     false,
		Qty:          6,
		ProducedOn:   d(time.August),
		BestBefore:   d(time.September),
	}
}

// sheetXML отдаёт XML первого листа из собранной книги — по нему проверяются
// разрывы страниц (в excelize нет чтения rowBreaks).
func sheetXML(t *testing.T, f *excelize.File) string {
	t.Helper()
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	for _, file := range zr.File {
		if file.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open sheet1.xml: %v", err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read sheet1.xml: %v", err)
		}
		return string(data)
	}
	t.Fatal("sheet1.xml не найден в книге")
	return ""
}

// imageSize отдаёт размер PNG штрих-кода в пикселях: высота картинки — это
// высота штрих-кода на наклейке (15 мм = 57 px образца).
func imageSize(t *testing.T, f *excelize.File) (width, height int) {
	t.Helper()
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	for _, file := range zr.File {
		if !strings.HasPrefix(file.Name, "xl/media/") {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		cfg, err := png.DecodeConfig(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("DecodeConfig(%s): %v", file.Name, err)
		}
		return cfg.Width, cfg.Height
	}
	t.Fatal("картинка штрих-кода не найдена в книге")
	return 0, 0
}

func printArea(t *testing.T, f *excelize.File) string {
	t.Helper()
	for _, n := range f.GetDefinedName() {
		if n.Name == "_xlnm.Print_Area" {
			return n.RefersTo
		}
	}
	return ""
}

func TestNewWorkbookWeightBox(t *testing.T) {
	f, labels, err := newWorkbook([]Box{weightBox()})
	if err != nil {
		t.Fatalf("newWorkbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 1 {
		t.Fatalf("наклеек: %d, want 1", labels)
	}
	sheet := f.GetSheetName(0)

	// Цифры кода — тот же 33-значный код, что кодируется в штрих-код.
	if got, _ := f.GetCellValue(sheet, "B2"); got != "002100030025000102908202629092026" {
		t.Errorf("цифры кода = %q", got)
	}
	if got, _ := f.GetCellValue(sheet, "B3"); got != "Говядина охл." {
		t.Errorf("наименование = %q", got)
	}
	if got, _ := f.GetCellValue(sheet, "B4"); got != "вес: 2,5 кг   вложений: 10" {
		t.Errorf("строка веса = %q", got)
	}
	// Даты — по строке на дату: выработка «от», срок «до».
	if got, _ := f.GetCellValue(sheet, "B5"); got != "от 29.08.2026" {
		t.Errorf("строка выработки = %q", got)
	}
	if got, _ := f.GetCellValue(sheet, "B6"); got != "до 29.09.2026" {
		t.Errorf("строка срока = %q", got)
	}

	// Штрих-код — картинка в первой строке блока, 72 × 15 мм.
	pics, err := f.GetPictureCells(sheet)
	if err != nil {
		t.Fatalf("GetPictureCells: %v", err)
	}
	if len(pics) != 1 || pics[0] != "B1" {
		t.Errorf("картинки = %v, want [B1]", pics)
	}
	if w, h := imageSize(t, f); w != barcodeW || h != barcodeH {
		t.Errorf("размер штрих-кода = %d×%d px, want %d×%d", w, h, barcodeW, barcodeH)
	}

	assertLabelLayout(t, f)
}

// assertLabelLayout проверяет геометрию печати одной наклейки: высоты строк,
// ширину колонки, область печати и разрыв страницы после блока.
func assertLabelLayout(t *testing.T, f *excelize.File) {
	t.Helper()
	sheet := f.GetSheetName(0)

	// Высоты строк — лейаут наклейки (pt), блок = 48,75 + 5 × 15,75 pt.
	var totalPt float64
	for i, wantPt := range rowHeightPt {
		got, err := f.GetRowHeight(sheet, i+1)
		if err != nil {
			t.Fatalf("GetRowHeight(%d): %v", i+1, err)
		}
		totalPt += wantPt
		if math.Abs(got-wantPt) > 0.01 {
			t.Errorf("высота строки %d = %.2f pt, want %.2f pt", i+1, got, wantPt)
		}
	}
	if totalPt != 127.5 {
		t.Errorf("сумма высот блока = %.2f pt, want 127,5", totalPt)
	}

	// Ширина колонки — 75 мм в «символах».
	if got, err := f.GetColWidth(sheet, "B"); err != nil || math.Abs(got-colWidthChars) > 0.01 {
		t.Errorf("ширина колонки B = %v (err %v), want %.2f", got, err, colWidthChars)
	}

	// Печать: область — блок наклейки, разрыв страницы после каждой наклейки.
	if area := printArea(t, f); !strings.HasSuffix(area, "$B$1:$B$6") {
		t.Errorf("область печати = %q, want …$B$1:$B$6", area)
	}
	// Разрыв после первой наклейки: строка 7 в Excel = id 6 в XML (нумерация с нуля).
	xml := sheetXML(t, f)
	if !strings.Contains(xml, "<brk id=\"6\"") {
		t.Error("нет разрыва страницы после первой наклейки (строка 7)")
	}
}

func TestNewWorkbookPieceBoxSentinelWeight(t *testing.T) {
	f, labels, err := newWorkbook([]Box{pieceBox()})
	if err != nil {
		t.Fatalf("newWorkbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 1 {
		t.Fatalf("наклеек: %d, want 1", labels)
	}
	sheet := f.GetSheetName(0)

	// Штучному в поле веса идёт заглушка 1 г: 00210010 + 000001 + 006 + даты.
	if got, _ := f.GetCellValue(sheet, "B2"); got != "002100100000010062908202629092026" {
		t.Errorf("цифры кода штучной коробки = %q", got)
	}
	// Веса в подписи нет — только число вложений.
	if got, _ := f.GetCellValue(sheet, "B4"); got != "вложений: 6" {
		t.Errorf("строка штучной коробки = %q", got)
	}
}

func TestNewWorkbookTwoBoxesBreakPages(t *testing.T) {
	f, labels, err := newWorkbook([]Box{weightBox(), pieceBox()})
	if err != nil {
		t.Fatalf("newWorkbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 2 {
		t.Fatalf("наклеек: %d, want 2", labels)
	}

	if area := printArea(t, f); !strings.HasSuffix(area, "$B$1:$B$12") {
		t.Errorf("область печати = %q, want …$B$1:$B$12", area)
	}
	xml := sheetXML(t, f)
	// Строки 7 и 13 в Excel = id 6 и 12 в XML.
	for _, brk := range []string{"6", "12"} {
		if !strings.Contains(xml, "<brk id=\""+brk+"\"") {
			t.Errorf("нет разрыва страницы (id %s)", brk)
		}
	}
}

// Коробка без даты выработки: 33-значный код не собрать — наклейка пропускается.
func TestNewWorkbookSkipsBoxWithoutDates(t *testing.T) {
	b := weightBox()
	b.ProducedOn = time.Time{}
	f, labels, err := newWorkbook([]Box{b})
	if err != nil {
		t.Fatalf("newWorkbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 0 {
		t.Fatalf("наклеек: %d, want 0", labels)
	}
	if _, err := Export([]Box{b}); !errors.Is(err, errNoLabels) {
		t.Fatalf("Export без дат: %v, want errNoLabels", err)
	}
}

// Export пишет файл в tempdir и возвращает путь к нему.
func TestExportWritesFile(t *testing.T) {
	// temp/ — рабочая директория приложения; в тесте её может не быть.
	if err := os.MkdirAll(tempdir.Dir, 0o750); err != nil {
		t.Fatalf("MkdirAll(%s): %v", tempdir.Dir, err)
	}
	path, err := Export([]Box{weightBox()})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if filepath.Ext(path) != ".xlsx" || !strings.Contains(filepath.Base(path), "box_labels_") {
		t.Fatalf("путь файла наклеек: %q", path)
	}
}

func TestFormatKg(t *testing.T) {
	tests := []struct {
		weightG int64
		want    string
	}{
		{weightG: 2500, want: "2,5"},
		{weightG: 2550, want: "2,55"},
		{weightG: 1, want: "0,001"},
		{weightG: 12000, want: "12"},
	}
	for _, tt := range tests {
		if got := formatKg(tt.weightG); got != tt.want {
			t.Errorf("formatKg(%d) = %q, want %q", tt.weightG, got, tt.want)
		}
	}
}

// Наклейка спец-кода «666»: тот же штрих-код, что на наклейке коробки, и цифры
// под ним — печатают, чтобы закрывать коробку сканом.
func TestBreakMarker(t *testing.T) {
	f, err := newBreakWorkbook()
	if err != nil {
		t.Fatalf("newBreakWorkbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)

	if got, _ := f.GetCellValue(sheet, "B2"); got != breakCode {
		t.Errorf("цифры кода = %q, want %q", got, breakCode)
	}
	pics, err := f.GetPictureCells(sheet)
	if err != nil {
		t.Fatalf("GetPictureCells: %v", err)
	}
	if len(pics) != 1 || pics[0] != "B1" {
		t.Errorf("картинки = %v, want [B1]", pics)
	}
	if w, h := imageSize(t, f); w != barcodeW || h != barcodeH {
		t.Errorf("размер штрих-кода = %d×%d px, want %d×%d", w, h, barcodeW, barcodeH)
	}
	if got, err := f.GetRowHeight(sheet, 1); err != nil || math.Abs(got-rowHeightPt[0]) > 0.01 {
		t.Errorf("высота строки штрих-кода = %v (err %v), want %.2f", got, err, rowHeightPt[0])
	}
	if area := printArea(t, f); !strings.HasSuffix(area, "$B$1:$B$2") {
		t.Errorf("область печати = %q, want …$B$1:$B$2", area)
	}
	if xml := sheetXML(t, f); !strings.Contains(xml, "<brk id=\"2\"") {
		t.Error("нет разрыва страницы после наклейки (строка 3)")
	}
}

// ExportBreakMarker пишет файл в tempdir: наклейку печатают и клеят у сканера.
func TestExportBreakMarkerWritesFile(t *testing.T) {
	if err := os.MkdirAll(tempdir.Dir, 0o750); err != nil {
		t.Fatalf("MkdirAll(%s): %v", tempdir.Dir, err)
	}
	path, err := ExportBreakMarker()
	if err != nil {
		t.Fatalf("ExportBreakMarker: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if !strings.Contains(filepath.Base(path), "box_break_666_") {
		t.Fatalf("имя файла наклейки: %q", filepath.Base(path))
	}
}

package boxlabel

import (
	"archive/zip"
	"bytes"
	"errors"
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
	if got, _ := f.GetCellValue(sheet, "B3"); got != "002100030025000102908202629092026" {
		t.Errorf("цифры кода = %q", got)
	}
	if got, _ := f.GetCellValue(sheet, "B4"); got != "Говядина охл." {
		t.Errorf("наименование = %q", got)
	}
	if got, _ := f.GetCellValue(sheet, "B5"); got != "вес: 2,5 кг   вложений: 10" {
		t.Errorf("строка веса = %q", got)
	}
	// Дата — строкой, срок — второй строкой ячейки (перенос).
	if got, _ := f.GetCellValue(sheet, "B6"); got != "выработка 29.08.2026\nсрок 29.09.2026" {
		t.Errorf("строка дат = %q", got)
	}

	// Штрих-код — картинка в строке 2.
	pics, err := f.GetPictureCells(sheet)
	if err != nil {
		t.Fatalf("GetPictureCells: %v", err)
	}
	if len(pics) != 1 || pics[0] != "B2" {
		t.Errorf("картинки = %v, want [B2]", pics)
	}

	// Высоты строк — лейаут наклейки (мм → pt), сумма = высота листа 120 мм.
	var totalMM float64
	for i, hMM := range rowHeightMM {
		got, err := f.GetRowHeight(sheet, i+1)
		if err != nil {
			t.Fatalf("GetRowHeight(%d): %v", i+1, err)
		}
		totalMM += hMM
		if math.Abs(got-hMM*mmToPt) > 0.01 {
			t.Errorf("высота строки %d = %.2f pt, want %.2f pt", i+1, got, hMM*mmToPt)
		}
	}
	if totalMM != 120 {
		t.Errorf("сумма высот блока = %.1f мм, want 120", totalMM)
	}

	// Ширина колонки — 75 мм в «символах».
	if got, err := f.GetColWidth(sheet, "B"); err != nil || math.Abs(got-colWidthChars) > 0.01 {
		t.Errorf("ширина колонки B = %v (err %v), want %.2f", got, err, colWidthChars)
	}

	// Печать: область — блок наклейки, разрыв страницы после каждой наклейки.
	if area := printArea(t, f); !strings.HasSuffix(area, "$B$1:$B$7") {
		t.Errorf("область печати = %q, want …$B$1:$B$7", area)
	}
	// Разрыв после первой наклейки: строка 8 в Excel = id 7 в XML (нумерация с нуля).
	xml := sheetXML(t, f)
	if !strings.Contains(xml, "<brk id=\"7\"") {
		t.Error("нет разрыва страницы после первой наклейки (строка 8)")
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
	if got, _ := f.GetCellValue(sheet, "B3"); got != "002100100000010062908202629092026" {
		t.Errorf("цифры кода штучной коробки = %q", got)
	}
	// Веса в подписи нет — только число вложений.
	if got, _ := f.GetCellValue(sheet, "B5"); got != "вложений: 6" {
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

	if area := printArea(t, f); !strings.HasSuffix(area, "$B$1:$B$14") {
		t.Errorf("область печати = %q, want …$B$1:$B$14", area)
	}
	xml := sheetXML(t, f)
	// Строки 8 и 15 в Excel = id 7 и 14 в XML.
	for _, brk := range []string{"7", "14"} {
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

package usecase

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image/png"
	"io"
	"testing"
	"time"

	"warehouseHelper/internal/receiving"

	"github.com/xuri/excelize/v2"
)

// Ожидаемые 29-значные коды фикстуры (код + вес + выработка + срок):
// стейк весом 1250 г и фарш весом 1 г — тот же код, что даёт sentinel-вес
// штучного товара. qrImagePx — сторона картинки QR: 21 модуль × масштаб 3.
const (
	qrCodeSteak  = "00210003012502908202629092026"
	qrCodeMinced = "10210003000012908202629092026"
	qrImagePx    = 63
)

// qrUnits — фикстура тестов: два куска с полными данными и один без даты
// выработки (наклейка из него не собирается и раскладку соседних не сдвигает).
// Меньший вес у куска без выработки — данные куска, не код: кода у него нет.
func qrUnits() []receiving.Unit {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)
	return []receiving.Unit{
		{
			InternalCode: "00210003",
			ProductName:  "Стейк Рибай",
			Weighted:     true,
			WeightG:      1250,
			ProducedOn:   &prod,
			BestBefore:   exp,
		},
		{
			InternalCode: "00210003",
			ProductName:  "Стейк Рибай",
			Weighted:     true,
			WeightG:      980,
			ProducedOn:   nil,
			BestBefore:   exp,
		},
		{
			InternalCode: "10210003",
			ProductName:  "Фарш",
			Weighted:     true,
			WeightG:      1,
			ProducedOn:   &prod,
			BestBefore:   exp,
		},
	}
}

// buildQRWorkbook собирает книгу QR-наклеек и открывает её обратно — так же, как
// это делает Excel: проверяются сохранённые значения, картинки и параметры
// печати, а не объекты в памяти. Рядом возвращаются сырые байты xlsx: отсечки
// страниц в excelize v2.10.1 читаются только из XML листа.
func buildQRWorkbook(t *testing.T, units []receiving.Unit) (f *excelize.File, raw []byte, labels int) {
	t.Helper()
	book, count, err := newQRLabelsWorkbook(units)
	if err != nil {
		t.Fatalf("newQRLabelsWorkbook error: %v", err)
	}
	defer func() { _ = book.Close() }()

	buf, err := book.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer error: %v", err)
	}
	rawBytes := buf.Bytes()
	got, err := excelize.OpenReader(bytes.NewReader(rawBytes))
	if err != nil {
		t.Fatalf("OpenReader error: %v", err)
	}
	t.Cleanup(func() { _ = got.Close() })
	return got, rawBytes, count
}

// rowBreakIDs вычитывает ручные отсечки страниц (горизонтальные) из XML листа:
// id — индекс строки, ПЕРЕД которой начинается новая страница (0-based).
func rowBreakIDs(t *testing.T, raw []byte) []int {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader error: %v", err)
	}
	var sheetXML []byte
	for _, zf := range zr.File {
		if zf.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		sheetXML, err = readZipFile(zf)
		if err != nil {
			t.Fatalf("read %s: %v", zf.Name, err)
		}
	}
	if sheetXML == nil {
		t.Fatal("в файле нет xl/worksheets/sheet1.xml")
	}

	var doc struct {
		Breaks []struct {
			ID  int  `xml:"id,attr"`
			Man bool `xml:"man,attr"`
		} `xml:"rowBreaks>brk"`
	}
	if err := xml.Unmarshal(sheetXML, &doc); err != nil {
		t.Fatalf("xml.Unmarshal листа: %v", err)
	}
	ids := make([]int, 0, len(doc.Breaks))
	for _, b := range doc.Breaks {
		if !b.Man {
			t.Errorf("отсечка строки %d не ручная", b.ID)
		}
		ids = append(ids, b.ID)
	}
	return ids
}

// readZipFile читает файл из xlsx-архива.
func readZipFile(zf *zip.File) ([]byte, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

// printArea — область печати листа (определённое имя _xlnm.Print_Area).
func printArea(t *testing.T, f *excelize.File) string {
	t.Helper()
	for _, dn := range f.GetDefinedName() {
		if dn.Name == "_xlnm.Print_Area" {
			return dn.RefersTo
		}
	}
	t.Fatal("область печати в книге не задана")
	return ""
}

// cellStyle — стиль ячейки (для проверки границ).
func cellStyle(t *testing.T, f *excelize.File, sheet, axis string) *excelize.Style {
	t.Helper()
	id, err := f.GetCellStyle(sheet, axis)
	if err != nil {
		t.Fatalf("GetCellStyle(%s) error: %v", axis, err)
	}
	st, err := f.GetStyle(id)
	if err != nil {
		t.Fatalf("GetStyle(%s) error: %v", axis, err)
	}
	return st
}

// hasRightBorder — задана ли у стиля правая граница (Style > 0 = видимая линия).
func hasRightBorder(borders []excelize.Border) bool {
	for _, b := range borders {
		if b.Type == "right" && b.Style > 0 {
			return true
		}
	}
	return false
}

func TestNewQRLabelsWorkbook_TwoPerPage(t *testing.T) {
	f, _, labels := buildQRWorkbook(t, qrUnits())
	if labels != 2 {
		t.Fatalf("labels = %d, want 2 (кусок без выработки пропущен)", labels)
	}

	// Первая наклейка — столбец B, вторая — C той же пары строк; за ними пусто.
	sheet := f.GetSheetName(0)
	cells := map[string]string{
		"B1": qrCodeSteak,
		"C1": qrCodeMinced,
		"B3": "",
		"C3": "",
	}
	for axis, want := range cells {
		if v, _ := f.GetCellValue(sheet, axis); v != want {
			t.Errorf("%s = %q, want %q", axis, v, want)
		}
	}
}

func TestNewQRLabelsWorkbook_Markup(t *testing.T) {
	f, _, _ := buildQRWorkbook(t, qrUnits())
	sheet := f.GetSheetName(0)

	// В нижней ячейке каждой наклейки пары — картинка QR (63×63 px).
	for _, axis := range []string{"B2", "C2"} {
		pics, err := f.GetPictures(sheet, axis)
		if err != nil {
			t.Fatalf("GetPictures(%s) error: %v", axis, err)
		}
		if len(pics) != 1 {
			t.Fatalf("GetPictures(%s) = %d pictures, want 1", axis, len(pics))
		}
		if pics[0].Extension != ".png" {
			t.Errorf("picture %s extension = %q, want .png", axis, pics[0].Extension)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(pics[0].File))
		if err != nil {
			t.Fatalf("png.DecodeConfig(%s) error: %v", axis, err)
		}
		if cfg.Width != qrImagePx || cfg.Height != qrImagePx {
			t.Errorf("picture %s = %d×%d px, want %d×%d", axis, cfg.Width, cfg.Height, qrImagePx, qrImagePx)
		}
	}

	// Правая граница обеих ячеек левой наклейки — линия разреза при печати.
	for _, axis := range []string{"B1", "B2"} {
		if !hasRightBorder(cellStyle(t, f, sheet, axis).Border) {
			t.Errorf("у ячейки %s нет правой границы", axis)
		}
	}
	// У правой наклейки пары границы нет — она последняя в паре.
	if hasRightBorder(cellStyle(t, f, sheet, "C1").Border) {
		t.Error("у правой наклейки (C1) не должно быть правой границы")
	}
}

func TestNewQRLabelsWorkbook_PrintLayout(t *testing.T) {
	f, raw, _ := buildQRWorkbook(t, qrUnits())
	sheet := f.GetSheetName(0)

	// Высоты строк: верхняя 33 px = 24,75 pt, нижняя 79 px = 59,25 pt.
	heights := map[int]float64{1: qrCodeRowH, 2: qrImgRowH}
	for row, want := range heights {
		got, err := f.GetRowHeight(sheet, row)
		if err != nil {
			t.Fatalf("GetRowHeight(%d) error: %v", row, err)
		}
		if got != want {
			t.Errorf("row %d height = %v, want %v", row, got, want)
		}
	}

	// Ширина обоих столбцов — 15 символов (110 px).
	for _, col := range []string{"B", "C"} {
		width, err := f.GetColWidth(sheet, col)
		if err != nil {
			t.Fatalf("GetColWidth(%s) error: %v", col, err)
		}
		if width != qrColWidth {
			t.Errorf("col %s width = %v, want %v", col, width, qrColWidth)
		}
	}

	// Область печати — оба столбца; две наклейки уложились в одну страницу,
	// то есть отсечек нет.
	if got, want := printArea(t, f), sheet+"!$B$1:$C$2"; got != want {
		t.Errorf("print area = %q, want %q", got, want)
	}
	if ids := rowBreakIDs(t, raw); len(ids) != 0 {
		t.Errorf("отсечки строк = %v, want пусто (одна страница)", ids)
	}
}

func TestNewQRLabelsWorkbook_PageBreakEverySecondRow(t *testing.T) {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	// Пять кусков — три страницы (строки 1-2, 3-4, 5-6), на каждой по две
	// наклейки, кроме последней (пятая наклейка одна — столбец B).
	units := make([]receiving.Unit, 0, 5)
	for i := 1; i <= 5; i++ {
		units = append(units, receiving.Unit{
			InternalCode: fmt.Sprintf("0021000%d", i),
			ProductName:  "Стейк Рибай",
			Weighted:     true,
			WeightG:      1250,
			ProducedOn:   &prod,
			BestBefore:   exp,
		})
	}

	f, raw, labels := buildQRWorkbook(t, units)
	if labels != 5 {
		t.Fatalf("labels = %d, want 5", labels)
	}
	if v, _ := f.GetCellValue(f.GetSheetName(0), "B5"); v != "00210005012502908202629092026" {
		t.Errorf("B5 = %q, want код пятого куска", v)
	}

	sheet := f.GetSheetName(0)
	cells := map[string]string{"B7": "", "C6": ""}
	for axis, want := range cells {
		if v, _ := f.GetCellValue(sheet, axis); v != want {
			t.Errorf("%s = %q, want %q (за последней наклейкой)", axis, v, want)
		}
	}
	if got, want := printArea(t, f), sheet+"!$B$1:$C$6"; got != want {
		t.Errorf("print area = %q, want %q", got, want)
	}

	// Отсечка после каждой второй строки: страницы начинаются с 3-й и 5-й.
	if ids := rowBreakIDs(t, raw); len(ids) != 2 || ids[0] != 2 || ids[1] != 4 {
		t.Errorf("отсечки строк = %v, want [2 4] (перед строками 3 и 5)", ids)
	}
}

func TestNewQRLabelsWorkbook_PieceGoodsWeightSentinel(t *testing.T) {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	// Штучный товар принимается без веса (WeightG 0): в код уходит sentinel-вес
	// 1 г (общий с этикетками) — иначе EncodeItem отвергнет код и кусок остался
	// бы без наклейки.
	f, _, labels := buildQRWorkbook(t, []receiving.Unit{
		{
			InternalCode: "00210010",
			ProductName:  "Хлеб Бородинский",
			Weighted:     false,
			WeightG:      0,
			ProducedOn:   &prod,
			BestBefore:   exp,
		},
	})
	if labels != 1 {
		t.Fatalf("labels = %d, want 1", labels)
	}
	if v, _ := f.GetCellValue(f.GetSheetName(0), "B1"); v != "00210010000012908202629092026" {
		t.Errorf("B1 = %q, want код со sentinel-весом 1 г", v)
	}
}

func TestNewQRLabelsWorkbook_AllSkipped(t *testing.T) {
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	f, labels, err := newQRLabelsWorkbook([]receiving.Unit{
		{InternalCode: "00210003", ProductName: "Стейк Рибай", Weighted: true, WeightG: 1250, ProducedOn: nil, BestBefore: exp},
	})
	if err != nil {
		t.Fatalf("newQRLabelsWorkbook error: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 0 {
		t.Errorf("labels = %d, want 0 (ни одного куска с выработкой)", labels)
	}
}

package usecase

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"image/png"
	"io"
	"testing"
	"time"

	"warehouseHelper/internal/receiving"

	"github.com/xuri/excelize/v2"
)

// buildQRWorkbook собирает книгу QR-наклеек и открывает её обратно — так же, как
// это делает Excel: проверяются сохранённые значения, картинки и параметры
// печати, а не объекты в памяти. Рядом возвращаются сырые байты xlsx: отсечки
// страниц в excelize v2.10.1 читаются только из XML листа.
func buildQRWorkbook(t *testing.T, units []receiving.Unit) (*excelize.File, []byte, int) {
	t.Helper()
	f, labels, err := newQRLabelsWorkbook(units)
	if err != nil {
		t.Fatalf("newQRLabelsWorkbook error: %v", err)
	}
	defer func() { _ = f.Close() }()

	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer error: %v", err)
	}
	raw := buf.Bytes()
	got, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("OpenReader error: %v", err)
	}
	t.Cleanup(func() { _ = got.Close() })
	return got, raw, labels
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
		rc, err := zf.Open()
		if err != nil {
			t.Fatalf("open %s: %v", zf.Name, err)
		}
		sheetXML, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", zf.Name, err)
		}
	}
	if sheetXML == nil {
		t.Fatal("xl/worksheets/sheet1.xml не найден в файле")
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
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	f, raw, labels := buildQRWorkbook(t, []receiving.Unit{
		{
			InternalCode: "00210003",
			ProductName:  "Стейк Рибай",
			Weighted:     true,
			WeightG:      1250,
			ProducedOn:   &prod,
			BestBefore:   exp,
		},
		{
			// Куска без даты выработки быть не должно (при приёмке данные
			// достраиваются вручную), но QR-наклейку для него не собрать —
			// пропускаем, не ломая раскладку соседних.
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
	})
	if labels != 2 {
		t.Fatalf("labels = %d, want 2", labels)
	}

	sheet := f.GetSheetName(0)

	// Верхние ячейки пары — внутренний код товара (текстовой информации о
	// товаре в наклейке нет).
	if v, _ := f.GetCellValue(sheet, "B1"); v != "00210003012502908202629092026" {
		t.Errorf("B1 = %q, want внутренний код", v)
	}
	if v, _ := f.GetCellValue(sheet, "C1"); v != "10210003000012908202629092026" {
		t.Errorf("C1 = %q, want внутренний код второго куска", v)
	}

	// Нижние ячейки — картинка QR-кода (по одной на наклейку).
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
		// 21 модуль QR × целый масштаб 3 = 63 px (ячейка 110 × 79 px).
		if cfg.Width != 63 || cfg.Height != 63 {
			t.Errorf("picture %s = %d×%d px, want 63×63", axis, cfg.Width, cfg.Height)
		}
	}

	// Правая граница у левой наклейки: обе её ячейки отделены от правой
	// наклейки пары (по этой линии режут лист при печати).
	for _, axis := range []string{"B1", "B2"} {
		id, err := f.GetCellStyle(sheet, axis)
		if err != nil {
			t.Fatalf("GetCellStyle(%s) error: %v", axis, err)
		}
		st, err := f.GetStyle(id)
		if err != nil {
			t.Fatalf("GetStyle(%s) error: %v", axis, err)
		}
		if !hasRightBorder(st.Border) {
			t.Errorf("у ячейки %s нет правой границы", axis)
		}
	}
	// У правой наклейки границы нет — она последняя в паре.
	id, err := f.GetCellStyle(sheet, "C1")
	if err != nil {
		t.Fatalf("GetCellStyle(C1) error: %v", err)
	}
	st, err := f.GetStyle(id)
	if err != nil {
		t.Fatalf("GetStyle(C1) error: %v", err)
	}
	if hasRightBorder(st.Border) {
		t.Error("у правой наклейки (C1) не должно быть правой границы")
	}

	// Высоты строк: верхняя 33 px = 24,75 pt, нижняя 79 px = 59,25 pt.
	for _, want := range []struct {
		row  int
		high float64
	}{{1, qrCodeRowH}, {2, qrImgRowH}} {
		got, err := f.GetRowHeight(sheet, want.row)
		if err != nil {
			t.Fatalf("GetRowHeight(%d) error: %v", want.row, err)
		}
		if got != want.high {
			t.Errorf("row %d height = %v, want %v", want.row, got, want.high)
		}
	}

	// Ширина обоих столбцов наклеек.
	for _, col := range []string{"B", "C"} {
		w, err := f.GetColWidth(sheet, col)
		if err != nil {
			t.Fatalf("GetColWidth(%s) error: %v", col, err)
		}
		if w != qrColWidth {
			t.Errorf("col %s width = %v, want %v", col, w, qrColWidth)
		}
	}

	// Область печати — оба столбца пары строк.
	var printArea string
	for _, dn := range f.GetDefinedName() {
		if dn.Name == "_xlnm.Print_Area" {
			printArea = dn.RefersTo
		}
	}
	if printArea != sheet+"!$B$1:$C$2" {
		t.Errorf("print area = %q, want %q", printArea, sheet+"!$B$1:$C$2")
	}

	// Одна пара строк — одна страница: отсечек больше не нужно.
	if ids := rowBreakIDs(t, raw); len(ids) != 0 {
		t.Errorf("отсечки строк = %v, want пусто (одна страница)", ids)
	}
}

func TestNewQRLabelsWorkbook_PageBreakEverySecondRow(t *testing.T) {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	units := make([]receiving.Unit, 0, 6)
	for _, code := range []string{"00210001", "00210002", "00210003", "00210004", "00210005", "00210006"} {
		units = append(units, receiving.Unit{
			InternalCode: code,
			ProductName:  "Стейк Рибай",
			Weighted:     true,
			WeightG:      1250,
			ProducedOn:   &prod,
			BestBefore:   exp,
		})
	}

	f, raw, labels := buildQRWorkbook(t, units)
	if labels != 6 {
		t.Fatalf("labels = %d, want 6", labels)
	}
	sheet := f.GetSheetName(0)

	// Третья пара наклеек — строки 5-6: раскладка B/C повторяется на каждой паре.
	if v, _ := f.GetCellValue(sheet, "B5"); v != "00210005012502908202629092026" {
		t.Errorf("B5 = %q, want код пятого куска", v)
	}
	if v, _ := f.GetCellValue(sheet, "C6"); v != "" {
		t.Errorf("C6 = %q, want пусто (в нижней ячейке картинка)", v)
	}
	if v, _ := f.GetCellValue(sheet, "B7"); v != "" {
		t.Errorf("B7 = %q, want пусто (за последней наклейкой)", v)
	}

	var printArea string
	for _, dn := range f.GetDefinedName() {
		if dn.Name == "_xlnm.Print_Area" {
			printArea = dn.RefersTo
		}
	}
	if printArea != sheet+"!$B$1:$C$6" {
		t.Errorf("print area = %q, want %q", printArea, sheet+"!$B$1:$C$6")
	}

	// Отсечка после каждой второй строки: страницы — строки 1-2, 3-4, 5-6.
	if ids := rowBreakIDs(t, raw); len(ids) != 2 || ids[0] != 2 || ids[1] != 4 {
		t.Errorf("отсечки строк = %v, want [2 4] (перед строками 3 и 5)", ids)
	}
}

func TestNewQRLabelsWorkbook_PieceGoodsWeightSentinel(t *testing.T) {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	// Штучный товар принимается без веса (WeightG 0): в QR уходит sentinel-вес
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
		t.Errorf("B1 = %q, want внутренний код штучного товара", v)
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
		t.Errorf("labels = %d, want 0", labels)
	}
}

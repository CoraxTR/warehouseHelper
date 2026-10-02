package usecase

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"warehouseHelper/internal/receiving"

	"github.com/xuri/excelize/v2"
)

// Ожидаемые 29-значные коды фикстуры (код + вес + выработка + срок):
// стейк весом 1250 г и фарш весом 1 г — тот же код, что даёт sentinel-вес
// штучного товара. labelsQRImagePx — сторона картинки QR: 21 модуль ×
// масштаб 4 (ячейка этикетки 100×99 px, у QR-наклейки 110×79 → 63 px).
const (
	labelsCodeSteak  = "00210003012502908202629092026"
	labelsCodeMinced = "10210003000012908202629092026"
	labelsQRImagePx  = 84
)

// labelsUnits — фикстура тестов: два куска с полными данными и один без даты
// выработки (этикетку из него не собрать, раскладку соседних он не сдвигает).
func labelsUnits() []receiving.Unit {
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

// buildLabelsWorkbook собирает книгу этикеток и открывает её обратно — так же,
// как это делает Excel: проверяются сохранённые значения, картинки и параметры
// печати, а не объекты в памяти. Рядом возвращаются сырые байты xlsx: отсечки
// страниц в excelize v2.10.1 читаются только из XML листа.
func buildLabelsWorkbook(t *testing.T, units []receiving.Unit) (f *excelize.File, raw []byte, labels int) {
	t.Helper()
	book, count, err := newLabelsWorkbook(units)
	if err != nil {
		t.Fatalf("newLabelsWorkbook error: %v", err)
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

func TestNewLabelsWorkbook_Markup(t *testing.T) {
	f, _, labels := buildLabelsWorkbook(t, labelsUnits())
	if labels != 2 {
		t.Fatalf("labels = %d, want 2 (кусок без выработки пропущен)", labels)
	}
	sheet := f.GetSheetName(0)

	checkLabelCells(t, f, sheet)
	checkQRPictures(t, f, sheet)
	checkLabelMerges(t, f, sheet)
	checkLabelAlignment(t, f, sheet)
}

// checkLabelCells — текстовые ячейки: B1 — код, B2 — подпись; этикетка n
// занимает строки 1+2n и 2+2n; за последней этикеткой пусто.
func checkLabelCells(t *testing.T, f *excelize.File, sheet string) {
	t.Helper()
	cells := map[string]string{
		"B1": labelsCodeSteak,
		"B2": "Стейк Рибай до 29.09.2026 вес: 1250",
		"B3": labelsCodeMinced,
		"B4": "Фарш до 29.09.2026 вес: 1",
		"B5": "",
	}
	for axis, want := range cells {
		if v, _ := f.GetCellValue(sheet, axis); v != want {
			t.Errorf("%s = %q, want %q", axis, v, want)
		}
	}
}

// checkQRPictures — картинка лежит только в объединённой C-ячейке этикетки:
// PNG 84×84 px (21 модуль × 4).
func checkQRPictures(t *testing.T, f *excelize.File, sheet string) {
	t.Helper()
	for _, axis := range []string{"C1", "C3"} {
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
		if cfg.Width != labelsQRImagePx || cfg.Height != labelsQRImagePx {
			t.Errorf("picture %s = %d×%d px, want %d×%d",
				axis, cfg.Width, cfg.Height, labelsQRImagePx, labelsQRImagePx)
		}
	}

	// В текстовых ячейках картинок нет — штрих-код с этикетки убран.
	for _, axis := range []string{"B1", "B2", "B3", "B4"} {
		pics, err := f.GetPictures(sheet, axis)
		if err != nil {
			t.Fatalf("GetPictures(%s) error: %v", axis, err)
		}
		if len(pics) != 0 {
			t.Errorf("GetPictures(%s) = %d pictures, want 0", axis, len(pics))
		}
	}
}

// checkLabelMerges — объединены ровно пары C1:C2 и C3:C4.
func checkLabelMerges(t *testing.T, f *excelize.File, sheet string) {
	t.Helper()
	merges, err := f.GetMergeCells(sheet)
	if err != nil {
		t.Fatalf("GetMergeCells error: %v", err)
	}
	got := make(map[string]bool, len(merges))
	for _, mc := range merges {
		got[mc.GetStartAxis()+":"+mc.GetEndAxis()] = true
	}
	for _, want := range []string{"C1:C2", "C3:C4"} {
		if !got[want] {
			t.Errorf("нет объединения %s (есть %v)", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("объединений %d, want 2 (%v)", len(got), got)
	}
}

// checkLabelAlignment — B1: по центру с переносом (29 цифр в 115 px в одну
// строку не влезают); B2: перенос и прижата к низу ячейки.
func checkLabelAlignment(t *testing.T, f *excelize.File, sheet string) {
	t.Helper()
	codeAlign := cellStyle(t, f, sheet, "B1").Alignment
	if codeAlign == nil || !codeAlign.WrapText || codeAlign.Horizontal != "center" {
		t.Errorf("выравнивание B1 = %+v, want перенос и по центру", codeAlign)
	}
	// B2 — подпись с переносом и прижата к низу ячейки.
	capAlign := cellStyle(t, f, sheet, "B2").Alignment
	if capAlign == nil {
		t.Fatal("у B2 нет выравнивания")
	}
	if !capAlign.WrapText {
		t.Error("у B2 не включён перенос текста")
	}
	if capAlign.Vertical != "bottom" {
		t.Errorf("вертикальное выравнивание B2 = %q, want bottom", capAlign.Vertical)
	}
}

func TestNewLabelsWorkbook_PrintLayout(t *testing.T) {
	f, raw, _ := buildLabelsWorkbook(t, labelsUnits())
	sheet := f.GetSheetName(0)

	// Высоты строк: верхняя 33 px = 24,75 pt, нижняя 66 px = 49,50 pt.
	heights := map[int]float64{1: labelsCodeRowH, 2: labelsTextRowH, 3: labelsCodeRowH, 4: labelsTextRowH}
	for row, want := range heights {
		got, err := f.GetRowHeight(sheet, row)
		if err != nil {
			t.Fatalf("GetRowHeight(%d) error: %v", row, err)
		}
		if got != want {
			t.Errorf("row %d height = %v, want %v", row, got, want)
		}
	}

	// Ширины столбцов: B 15,71 (115 px), C 13,57 (100 px).
	widths := map[string]float64{"B": labelsColBWidth, "C": labelsColCWidth}
	for col, want := range widths {
		got, err := f.GetColWidth(sheet, col)
		if err != nil {
			t.Fatalf("GetColWidth(%s) error: %v", col, err)
		}
		if got != want {
			t.Errorf("col %s width = %v, want %v", col, got, want)
		}
	}

	if got, want := printArea(t, f), sheet+"!$B$1:$C$4"; got != want {
		t.Errorf("print area = %q, want %q", got, want)
	}

	// Одна этикетка — одна страница: отсечка перед третьей строкой и никаких
	// отсечек по столбцам (их excelize заводит «в довесок» к ссылке в столбце B).
	rows, cols := pageBreakIDs(t, raw)
	if len(rows) != 1 || rows[0] != 2 {
		t.Errorf("отсечки строк = %v, want [2] (перед строкой 3)", rows)
	}
	if len(cols) != 0 {
		t.Errorf("отсечки столбцов = %v, want пусто", cols)
	}
}

func TestNewLabelsWorkbook_OneLabelPerPage(t *testing.T) {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	// Три куска — три страницы: строки 1-2, 3-4, 5-6.
	units := make([]receiving.Unit, 0, 3)
	for i := 1; i <= 3; i++ {
		units = append(units, receiving.Unit{
			InternalCode: "0021000" + string(rune('0'+i)),
			ProductName:  "Стейк Рибай",
			Weighted:     true,
			WeightG:      1250,
			ProducedOn:   &prod,
			BestBefore:   exp,
		})
	}

	f, raw, labels := buildLabelsWorkbook(t, units)
	if labels != 3 {
		t.Fatalf("labels = %d, want 3", labels)
	}
	sheet := f.GetSheetName(0)
	if v, _ := f.GetCellValue(sheet, "B5"); v == "" {
		t.Error("B5 пуст, want код третьего куска")
	}
	if v, _ := f.GetCellValue(sheet, "B7"); v != "" {
		t.Errorf("B7 = %q, want пусто (за последней этикеткой)", v)
	}
	if got, want := printArea(t, f), sheet+"!$B$1:$C$6"; got != want {
		t.Errorf("print area = %q, want %q", got, want)
	}
	rows, _ := pageBreakIDs(t, raw)
	if len(rows) != 2 || rows[0] != 2 || rows[1] != 4 {
		t.Errorf("отсечки строк = %v, want [2 4] (перед строками 3 и 5)", rows)
	}
}

func TestNewLabelsWorkbook_AllSkipped(t *testing.T) {
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	f, labels, err := newLabelsWorkbook([]receiving.Unit{
		{InternalCode: "00210003", ProductName: "Стейк Рибай", Weighted: true, WeightG: 1250, ProducedOn: nil, BestBefore: exp},
	})
	if err != nil {
		t.Fatalf("newLabelsWorkbook error: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 0 {
		t.Errorf("labels = %d, want 0 (ни одного куска с выработкой)", labels)
	}
}

func TestNewLabelsWorkbook_PieceGoodsWeightSentinel(t *testing.T) {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	// Штучный товар принимается без веса (WeightG 0): в код этикетки уходит
	// sentinelWeightG (1 г) — иначе EncodeItem отвергнет код и кусок остался бы
	// без этикетки. В подписи веса у штучного нет.
	f, _, labels := buildLabelsWorkbook(t, []receiving.Unit{
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
	sheet := f.GetSheetName(0)
	if v, _ := f.GetCellValue(sheet, "B1"); v != "00210010000012908202629092026" {
		t.Errorf("B1 = %q, want код с sentinel-весом 1 г", v)
	}
	if v, _ := f.GetCellValue(sheet, "B2"); v != "Хлеб Бородинский до 29.09.2026" {
		t.Errorf("B2 = %q, want подпись без веса", v)
	}
}

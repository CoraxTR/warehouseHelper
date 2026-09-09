package usecase

import (
	"bytes"
	"testing"
	"time"

	"warehouseHelper/internal/receiving"

	"github.com/xuri/excelize/v2"
)

func TestNewLabelsWorkbook(t *testing.T) {
	prod := time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	f, labels, err := newLabelsWorkbook([]receiving.Unit{
		{
			InternalCode: "00210003",
			ProductName:  "Стейк Рибай",
			WeightG:      1250,
			ProducedOn:   &prod,
			BestBefore:   exp,
		},
		{
			// Куска без даты выработки быть не должно (при приёмке данные
			// достраиваются вручную), но этикетку для него не собрать —
			// пропускаем, не ломая блоки соседних.
			InternalCode: "00210003",
			ProductName:  "Стейк Рибай",
			WeightG:      980,
			ProducedOn:   nil,
			BestBefore:   exp,
		},
		{
			InternalCode: "10210003",
			ProductName:  "Фарш",
			WeightG:      1,
			ProducedOn:   &prod,
			BestBefore:   exp,
		},
	})
	if err != nil {
		t.Fatalf("newLabelsWorkbook error: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 2 {
		t.Errorf("labels = %d, want 2", labels)
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer error: %v", err)
	}
	got, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("OpenReader error: %v", err)
	}
	defer func() { _ = got.Close() }()

	sheet := got.GetSheetName(0)

	// Блок первой этикетки: строки 1-3.
	if v, _ := got.GetCellValue(sheet, "B1"); v != "00210003012502908202629092026" {
		t.Errorf("B1 = %q, want 29-значный код куска", v)
	}
	pics, err := got.GetPictures(sheet, "B2")
	if err != nil {
		t.Fatalf("GetPictures(B2) error: %v", err)
	}
	if len(pics) != 1 {
		t.Fatalf("GetPictures(B2) = %d pictures, want 1", len(pics))
	}
	if pics[0].Extension != ".png" {
		t.Errorf("picture extension = %q, want .png", pics[0].Extension)
	}
	if v, _ := got.GetCellValue(sheet, "B3"); v != "Стейк Рибай до 29.09.2026 вес: 1250" {
		t.Errorf("B3 = %q, want подпись этикетки", v)
	}

	// Пропущенный кусок не сдвинул блоки: вторая этикетка — строки 4-6.
	if v, _ := got.GetCellValue(sheet, "B4"); v != "10210003000012908202629092026" {
		t.Errorf("B4 = %q, want код второго куска", v)
	}
	if v, _ := got.GetCellValue(sheet, "B6"); v != "Фарш до 29.09.2026 вес: 1" {
		t.Errorf("B6 = %q, want подпись второй этикетки", v)
	}

	// За последней этикеткой пусто.
	if v, _ := got.GetCellValue(sheet, "B7"); v != "" {
		t.Errorf("B7 = %q, want пусто", v)
	}

	// Параметры печати: ширина колонки B и зона печати.
	width, err := got.GetColWidth(sheet, "B")
	if err != nil {
		t.Fatalf("GetColWidth error: %v", err)
	}
	if width != labelsColWidth {
		t.Errorf("col width = %v, want %v", width, labelsColWidth)
	}
	var printArea string
	for _, dn := range got.GetDefinedName() {
		if dn.Name == "_xlnm.Print_Area" {
			printArea = dn.RefersTo
		}
	}
	if printArea != sheet+"!$B$1:$B$6" {
		t.Errorf("print area = %q, want %q", printArea, sheet+"!$B$1:$B$6")
	}
}

func TestNewLabelsWorkbook_AllSkipped(t *testing.T) {
	exp := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)

	f, labels, err := newLabelsWorkbook([]receiving.Unit{
		{InternalCode: "00210003", ProductName: "Стейк Рибай", WeightG: 1250, ProducedOn: nil, BestBefore: exp},
	})
	if err != nil {
		t.Fatalf("newLabelsWorkbook error: %v", err)
	}
	defer func() { _ = f.Close() }()
	if labels != 0 {
		t.Errorf("labels = %d, want 0", labels)
	}
}

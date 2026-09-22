package excel

import (
	"bytes"
	"image"
	"testing"

	"github.com/xuri/excelize/v2"
)

// Формат наклейки РефГо (одна наклейка = блок из 8 строк = одна страница):
// штрих-код 265 × 28 px = 7,00 × 0,74 см, отступ слева 50 px — рисунок по центру
// печатной области колонок A:B (365 px), первая строка 41,25 pt = 55 px.
func TestLabelBarcodePlacement(t *testing.T) {
	f := excelize.NewFile()

	if err := insertBarcodeIntoCell(f, "Sheet1", "774512301", 1); err != nil {
		t.Fatalf("insertBarcodeIntoCell: %v", err)
	}

	pics, err := f.GetPictures("Sheet1", "A1")
	if err != nil {
		t.Fatalf("GetPictures: %v", err)
	}

	if len(pics) != 1 {
		t.Fatalf("рисунков в A1: %d, ждём 1", len(pics))
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(pics[0].File))
	if err != nil {
		t.Fatalf("не разобрать PNG штрих-кода: %v", err)
	}

	if cfg.Width != 265 || cfg.Height != 28 {
		t.Errorf("размер штрих-кода %d×%d px, ждём 265×28 px (7,00 × 0,74 см)", cfg.Width, cfg.Height)
	}

	if pics[0].Format == nil {
		t.Fatal("у рисунка нет настроек размещения")
	}

	if pics[0].Format.OffsetX != 50 {
		t.Errorf("отступ рисунка слева %v, ждём 50 px (центр печатной области A:B)", pics[0].Format.OffsetX)
	}
}

// Высоты строк блока наклейки: первая строка укорочена под штрих-код (41,25 pt =
// 55 px), строки 2-8 — боевые значения.
func TestLabelRowHeights(t *testing.T) {
	f := excelize.NewFile()

	style, err := f.NewStyle(&excelize.Style{})
	if err != nil {
		t.Fatalf("NewStyle: %v", err)
	}

	if err := setCellsStyle(f, "Sheet1", 1, style, style, style); err != nil {
		t.Fatalf("setCellsStyle: %v", err)
	}

	// Строка 7 высоты не задаёт (Excel берёт свою — там переносится адрес).
	want := map[int]float64{1: 41.25, 2: 20, 3: 12, 4: 12, 5: 12, 6: 12, 8: 12}

	for row, wantHeight := range want {
		got, err := f.GetRowHeight("Sheet1", row)
		if err != nil {
			t.Fatalf("GetRowHeight(%d): %v", row, err)
		}

		if got != wantHeight {
			t.Errorf("высота строки %d = %g pt, ждём %g pt", row, got, wantHeight)
		}
	}
}

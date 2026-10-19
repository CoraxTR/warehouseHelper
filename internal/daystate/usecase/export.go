package usecase

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
	"unicode/utf8"

	"warehouseHelper/internal/daystate"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/tempdir"

	"github.com/xuri/excelize/v2"
)

// Цвета ячеек xlsx — те же, что в вебе (raven-guard.css).
const (
	xlsxFillWhite     = "#FFFFFF"
	xlsxFillYellow    = "#FFEB3B"
	xlsxFillRed       = "#F1948A"
	xlsxFillGray      = "#000000"
	xlsxFontGray      = "#C8C6BE"
	xlsxFontRed       = "#C00000"
	xlsxFontDefault   = "#000000"
	xlsxColGroupWidth = 30.0
	xlsxColNameWidth  = 40.0
	xlsxColDayPad     = 2.0 // поля клетки, символы
	xlsxColDayMin     = 6.0 // пустой день не сжимается в нитку
)

// ExportStockReport выгружает «Отчёт по наличию» за месяц в xlsx
// (tempdir, имя stock_report_YYYY-MM.xlsx) и возвращает путь к файлу.
// Заливка и шрифты ячеек — по правилам владельца (daystate.CellFor),
// как в веб-версии отчёта.
func (uc *UseCase) ExportStockReport(ctx context.Context, month time.Time) (string, error) {
	done := metrics.Track(trackPkg, "ExportStockReport")
	defer done()

	report, err := uc.StockReport(ctx, month)
	if err != nil {
		return "", err
	}

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)

	styles, err := reportStyles(f)
	if err != nil {
		return "", err
	}

	// Шапка: группа, товар, дни месяца.
	_ = f.SetCellValue(sheet, "A1", "Группа")
	_ = f.SetCellValue(sheet, "B1", "Товар")
	for i := 1; i <= report.Days; i++ {
		axis, _ := excelize.CoordinatesToCellName(i+2, 1) // колонка C — день 1
		_ = f.SetCellValue(sheet, axis, i)
	}

	for ri, p := range report.Products {
		row := ri + 2
		groupAxis, _ := excelize.CoordinatesToCellName(1, row)
		nameAxis, _ := excelize.CoordinatesToCellName(2, row)
		_ = f.SetCellValue(sheet, groupAxis, p.GroupName)
		_ = f.SetCellValue(sheet, nameAxis, p.Name)
		for di, cell := range p.Cells {
			axis, _ := excelize.CoordinatesToCellName(di+3, row)
			if cell.Text != "" {
				_ = f.SetCellValue(sheet, axis, cell.Text)
			}
			if s, ok := styles[cell.Kind]; ok {
				_ = f.SetCellStyle(sheet, axis, axis, s)
			}
		}
	}

	_ = f.SetColWidth(sheet, "A", "A", xlsxColGroupWidth)
	_ = f.SetColWidth(sheet, "B", "B", xlsxColNameWidth)
	for di, width := range dayColWidths(report) {
		col, _ := excelize.ColumnNumberToName(di + 3) // C — день 1
		_ = f.SetColWidth(sheet, col, col, width)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, XSplit: 2, YSplit: 1})

	name := fmt.Sprintf("stock_report_%s.xlsx", report.Month.Format("2006-01"))
	path := filepath.Join(tempdir.Dir, name)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("сохранить xlsx: %w", err)
	}
	return path, nil
}

// dayColWidths — ширина колонки каждого дня по самой длинной клетке столбца:
// в xlsx столбцы должны выглядеть как на странице (владелец, 19.10.2026 —
// «в xlsx надо сделать так же, как в отображении на странице»). Единица
// SetColWidth — символы шрифта книги, поэтому длина текста плюс поля; пустой
// столбец не сжимается меньше xlsxColDayMin.
func dayColWidths(report *StockReportPage) []float64 {
	widths := make([]float64, report.Days)
	for i := range widths {
		longest := 0
		for _, p := range report.Products {
			if i >= len(p.Cells) {
				continue
			}
			if n := utf8.RuneCountInString(p.Cells[i].Text); n > longest {
				longest = n
			}
		}
		w := float64(longest) + xlsxColDayPad
		if w < xlsxColDayMin {
			w = xlsxColDayMin
		}
		widths[i] = w
	}
	return widths
}

// reportStyles — стили заливки по видам ячеек (пустая — без стиля).
func reportStyles(f *excelize.File) (map[daystate.CellKind]int, error) {
	newStyle := func(fill, font string) (int, error) {
		return f.NewStyle(&excelize.Style{
			Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{fill}},
			Font: &excelize.Font{Color: font},
		})
	}
	yellow, err := newStyle(xlsxFillYellow, xlsxFontDefault)
	if err != nil {
		return nil, fmt.Errorf("стиль yellow: %w", err)
	}
	red, err := newStyle(xlsxFillRed, xlsxFontDefault)
	if err != nil {
		return nil, fmt.Errorf("стиль red: %w", err)
	}
	yellowRed, err := newStyle(xlsxFillYellow, xlsxFontRed)
	if err != nil {
		return nil, fmt.Errorf("стиль yellow-red: %w", err)
	}
	gray, err := newStyle(xlsxFillGray, xlsxFontGray)
	if err != nil {
		return nil, fmt.Errorf("стиль gray: %w", err)
	}
	// CellPlain в карту не входит: клетка без скидки остаётся без заливки
	// (решение владельца 18.10.2026) — xlsx и страница совпадают по смыслу.
	return map[daystate.CellKind]int{
		daystate.CellYellow:    yellow,
		daystate.CellRed:       red,
		daystate.CellYellowRed: yellowRed,
		daystate.CellGray:      gray,
	}, nil
}

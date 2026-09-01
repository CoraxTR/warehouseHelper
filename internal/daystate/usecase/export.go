package usecase

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

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
	xlsxFillGray      = "#CCCCCC"
	xlsxFontRed       = "#C00000"
	xlsxFontDefault   = "#000000"
	xlsxColGroupWidth = 30.0
	xlsxColNameWidth  = 40.0
	xlsxColDayWidth   = 7.0
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
	lastCol, _ := excelize.ColumnNumberToName(report.Days + 2)
	_ = f.SetColWidth(sheet, "C", lastCol, xlsxColDayWidth)
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, XSplit: 2, YSplit: 1})

	name := fmt.Sprintf("stock_report_%s.xlsx", report.Month.Format("2006-01"))
	path := filepath.Join(tempdir.Dir, name)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("сохранить xlsx: %w", err)
	}
	return path, nil
}

// reportStyles — стили заливки по видам ячеек (пустая — без стиля).
func reportStyles(f *excelize.File) (map[daystate.CellKind]int, error) {
	newStyle := func(fill, font string) (int, error) {
		return f.NewStyle(&excelize.Style{
			Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{fill}},
			Font: &excelize.Font{Color: font},
		})
	}
	plain, err := newStyle(xlsxFillWhite, xlsxFontDefault)
	if err != nil {
		return nil, fmt.Errorf("стиль plain: %w", err)
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
	gray, err := newStyle(xlsxFillGray, xlsxFontDefault)
	if err != nil {
		return nil, fmt.Errorf("стиль gray: %w", err)
	}
	return map[daystate.CellKind]int{
		daystate.CellPlain:     plain,
		daystate.CellYellow:    yellow,
		daystate.CellRed:       red,
		daystate.CellYellowRed: yellowRed,
		daystate.CellGray:      gray,
	}, nil
}

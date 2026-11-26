package usecase

import (
	"fmt"
	"path/filepath"
	"time"

	"warehouseHelper/internal/tempdir"

	"github.com/xuri/excelize/v2"
)

// Выгрузка найденных удалений в xlsx (excelize, как остальные отчёты
// проекта). Файл — в tempdir, страница отдаёт его по /download или
// напрямую через /ms/audit-search/export.xlsx.

const (
	xlsxSheetName = "Удаления"
	xlsxColMoment = 20.0
	xlsxColOrder  = 16.0
	xlsxColID     = 38.0
	xlsxColName   = 52.0
	xlsxColQty    = 12.0
	xlsxColUOM    = 8.0
	xlsxColUID    = 28.0
	xlsxColAudit  = 38.0
)

// Export — xlsx по всем найденным строкам задачи: шапка с товаром и окном,
// затем таблица удалений. Возвращает путь к файлу в tempdir.
func (uc *UseCase) Export(jobID string) (string, error) {
	uc.mu.Lock()

	j := uc.jobs[jobID]
	if j == nil {
		uc.mu.Unlock()

		return "", ErrJobNotFound
	}

	productName := j.productName
	productID := j.productID
	from, to := j.from, j.to
	results := make([]Result, len(j.results))
	copy(results, j.results)
	uc.mu.Unlock()

	if productName == "" {
		productName = productID
	}

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	if err := f.SetSheetName(f.GetSheetName(0), xlsxSheetName); err != nil {
		return "", fmt.Errorf("лист xlsx: %w", err)
	}

	_ = f.SetCellValue(xlsxSheetName, "A1", "Товар: "+productName)
	_ = f.SetCellValue(xlsxSheetName, "A2", fmt.Sprintf(
		"Окно: %s — %s (МСК), найдено: %d",
		from.Format("02.01.2006"), to.Format("02.01.2006"), len(results)))

	header := []string{"Момент (МСК)", "Заказ", "ID заказа", "Товар", "Кол-во", "Ед.", "Автор (uid)", "Событие аудита"}
	for i, title := range header {
		axis, err := excelize.CoordinatesToCellName(i+1, 4)
		if err != nil {
			return "", fmt.Errorf("шапка xlsx: %w", err)
		}

		_ = f.SetCellValue(xlsxSheetName, axis, title)
	}

	for ri, r := range results {
		row := ri + 5

		moment := r.MomentText()

		values := []any{
			moment,
			r.OrderName,
			r.OrderID,
			r.ProductName,
			r.Quantity,
			r.UOM,
			r.UID,
			r.AuditID,
		}
		for ci, v := range values {
			axis, err := excelize.CoordinatesToCellName(ci+1, row)
			if err != nil {
				return "", fmt.Errorf("строка xlsx %d: %w", row, err)
			}

			_ = f.SetCellValue(xlsxSheetName, axis, v)
		}
	}

	widths := []float64{xlsxColMoment, xlsxColOrder, xlsxColID, xlsxColName, xlsxColQty, xlsxColUOM, xlsxColUID, xlsxColAudit}
	for i, width := range widths {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return "", fmt.Errorf("ширина колонки xlsx: %w", err)
		}

		_ = f.SetColWidth(xlsxSheetName, col, col, width)
	}

	name := fmt.Sprintf("audit_deletions_%s.xlsx", uc.now().Format("20060102_150405"))
	path := filepath.Join(uc.tempDir(), name)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("сохранить xlsx: %w", err)
	}

	return path, nil
}

// tempDir — путь временной директории экспорта; в тестах подменяется
// (писать в temp/ проекта из тестов не нужно и негде).
func (uc *UseCase) tempDir() string {
	if uc.temp != "" {
		return uc.temp
	}

	return tempdir.Dir
}

// momentText — момент для выгрузки/страницы (МСК), пусто для нулевого.
func momentText(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.In(mskLoc).Format("02.01.2006 15:04")
}

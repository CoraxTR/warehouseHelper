package usecase

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"path/filepath"
	"strconv"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/tempdir"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/xuri/excelize/v2"
)

// Параметры листа этикеток — перенесены из рабочего прототипа печати наклеек
// (формат файла для сканера этикеток): колонка B, шрифт 9 с переносом,
// штрих-код 211×40 px со смещением внутри ячейки, строка картинки высотой 35,
// ширина колонки 30. На каждую этикетку — блок из трёх строк: данные кода,
// картинка Code128, подпись «название до срок вес: N»; страница рвётся после
// каждого блока.
const (
	labelsFontSize = 9
	labelsColWidth = 30.0
	labelsImgRowH  = 35.0
	// Ширина штрих-кода: barcode.Scale не ужимает Code128 ниже естественной
	// ширины. Для 29-значного кода (Code128C) она 211 px — в прототипе было
	// 189, но тот кодировал короткий код конкретного поставщика.
	labelsBarcodeW   = 211
	labelsBarcodeH   = 40
	labelsImgOffsetX = 5
	labelsImgOffsetY = 3
)

// errNoLabels — нет ни одного куска, из которого можно собрать этикетку.
var errNoLabels = errors.New("ни у одного куска нет полных данных для этикетки (нужна дата выработки)")

// ExportLabels формирует xlsx-файл этикеток принятых кусков в tempdir и
// возвращает путь к нему. В штрих-код этикетки кодируется полный внутренний
// код куска (innercode.EncodeItem), чтобы этикетка сканировалась как обычный
// кусок в заказы и расформирования. Куски без полных данных (нет даты
// выработки, нулевой вес) пропускаются.
func (uc *ReceivingUseCase) ExportLabels(units []receiving.Unit) (string, error) {
	done := metrics.Track(trackPkg, "ExportLabels")
	defer done()

	f, labels, err := newLabelsWorkbook(units)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if labels == 0 {
		return "", errNoLabels
	}

	name := fmt.Sprintf("receive_labels_%s.xlsx", time.Now().Format("20060102_150405"))
	path := filepath.Join(tempdir.Dir, name)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("сохранить файл этикеток: %w", err)
	}
	return path, nil
}

// newLabelsWorkbook собирает книгу этикеток (в памяти) и возвращает число
// сформированных этикеток.
func newLabelsWorkbook(units []receiving.Unit) (*excelize.File, int, error) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)

	style, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: labelsFontSize},
		Alignment: &excelize.Alignment{WrapText: true},
	})
	if err != nil {
		return nil, 0, err
	}

	row := 1
	labels := 0
	for _, u := range units {
		var produced time.Time
		if u.ProducedOn != nil {
			produced = *u.ProducedOn
		}
		code, err := innercode.EncodeItem(u.InternalCode, u.WeightG, produced, u.BestBefore)
		if err != nil {
			continue // полного внутреннего кода нет — этикетку не собрать
		}

		pngBytes, err := generateBarcodePNG(code, labelsBarcodeW, labelsBarcodeH)
		if err != nil {
			return nil, 0, fmt.Errorf("штрих-код %s: %w", code, err)
		}

		// Строка 1: данные кода (визуальный контроль при наклейке).
		axis := fmt.Sprintf("B%d", row)
		_ = f.SetCellValue(sheet, axis, code)
		_ = f.SetCellStyle(sheet, axis, axis, style)
		row++

		// Строка 2: картинка штрих-кода.
		axis = fmt.Sprintf("B%d", row)
		_ = f.AddPictureFromBytes(sheet, axis, &excelize.Picture{
			Extension: ".png",
			File:      pngBytes,
			Format: &excelize.GraphicOptions{
				ScaleX:      1.0,
				ScaleY:      1.0,
				OffsetX:     labelsImgOffsetX,
				OffsetY:     labelsImgOffsetY,
				Positioning: "oneCell",
			},
		})
		_ = f.SetRowHeight(sheet, row, labelsImgRowH)
		row++

		// Строка 3: подпись «название до срок вес: N».
		axis = fmt.Sprintf("B%d", row)
		_ = f.SetCellValue(sheet, axis, labelCaption(u))
		_ = f.SetCellStyle(sheet, axis, axis, style)
		row++

		labels++
	}

	_ = f.SetColWidth(sheet, "B", "B", labelsColWidth)
	if labels > 0 {
		printArea := fmt.Sprintf("%s!$B$1:$B$%d", sheet, row-1)
		_ = f.SetDefinedName(&excelize.DefinedName{
			Name:     "_xlnm.Print_Area",
			RefersTo: printArea,
			Scope:    sheet,
		})
		// Разрыв страницы после каждого блока этикетки (строки 1-3, 4-6, ...).
		for r := 4; r <= row; r += 3 {
			_ = f.InsertPageBreak(sheet, fmt.Sprintf("B%d", r))
		}
	}
	return f, labels, nil
}

// labelCaption — подпись под штрих-кодом (как в прототипе печати наклеек).
func labelCaption(u receiving.Unit) string {
	return u.ProductName + " до " + u.BestBefore.Format("02.01.2006") +
		" вес: " + strconv.FormatInt(u.WeightG, 10)
}

// generateBarcodePNG создаёт PNG-байты штрих-кода Code128.
func generateBarcodePNG(data string, width, height int) ([]byte, error) {
	bc, err := code128.Encode(data)
	if err != nil {
		return nil, err
	}
	scaled, err := barcode.Scale(bc, width, height)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

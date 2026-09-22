// Package boxlabel печатает наклейки коробок (xlsx): 33-значный внутренний код
// коробки в Code128, ниже его цифры, наименование товара, вес и число вложений.
// Лист 75 × 120 мм, одна наклейка = одна страница. Пакет — нижний слой: его
// вызывают и приёмка (наклейки принятых коробок), и страница «Создать коробку»
// в «Продукции» — геометрия печати живёт в одном месте.
package boxlabel

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/tempdir"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/xuri/excelize/v2"
)

// Геометрия наклейки: лист 75 мм шириной и 120 мм высотой, одна наклейка на
// страницу. Блок занимает лист целиком: отступ, штрих-код, цифры кода,
// наименование, вес с числом вложений, нижний отступ.
//
// Переводы (единственная арифметика): 1 мм = 2,8346 pt для высот строк, для
// картинок px = мм × 3,7795 (96 dpi), ширина колонки в «символах» ≈ (px − 5) / 7
// (метрика рендера Excel, не excelize-конвертер).
const (
	// Ширина колонки B: 75 мм ≈ 283 px ≈ 39,79 символа.
	colWidthChars = 39.79

	// Штрих-код: 33 цифры Code128 дают минимум 233 px, растягиваем до 72 мм —
	// модуль шире, читаемость лучше. 272 px < 283 px колонки.
	barcodeW = 272
	barcodeH = 113

	// Шрифты: 33 цифры при 10 pt ≈ 64,7 мм — влезают в 75 мм без обрезки.
	fontDigits = 10
	fontName   = 14
	fontAmount = 16

	// Смещение картинки внутри ячейки (px): по центру колонки и от верха.
	imgOffsetX = 5
	imgOffsetY = 3

	// sentinelWeightG — заглушка веса для штучного товара: у штучных веса нет,
	// но поле веса в 33-значном коде не может быть пустым (в коде учитывается
	// только количество). В данные приёмки и отчёты заглушка не попадает.
	sentinelWeightG int64 = 1
)

// rowHeightMM — высоты строк одной наклейки: отступ, штрих-код, цифры,
// наименование, вес с вложениями, даты, нижний отступ. Сумма — ровно высота
// листа (120 мм), поэтому наклейка печатается 1:1 без масштабирования.
var rowHeightMM = []float64{6, 32, 10, 22, 18, 17, 15}

// Box — данные наклейки коробки. Достаточно для 33-значного кода и подписей:
// товар, вес (у штучных — ноль), число вложений и обе даты (без них код не
// собрать, EncodeBox отвергает нулевые даты).
type Box struct {
	InternalCode string
	ProductName  string
	Weighted     bool
	WeightG      int64
	Qty          int
	ProducedOn   time.Time
	BestBefore   time.Time
}

// errNoLabels — ни одной коробки с полными данными для наклейки.
var errNoLabels = errors.New("ни у одной коробки нет полных данных для наклейки (нужна дата выработки)")

// Export формирует xlsx-файл наклеек коробок в tempdir и возвращает путь к нему.
// В штрих-код наклейки кодируется полный внутренний код коробки
// (innercode.EncodeBox), чтобы наклейка распознавалась при последующем
// сканировании. Коробки без полных данных (нет даты выработки или срока,
// вес или число вложений не влезают в формат) пропускаются молча.
func Export(boxes []Box) (string, error) {
	f, labels, err := newWorkbook(boxes)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if labels == 0 {
		return "", errNoLabels
	}

	name := fmt.Sprintf("box_labels_%s.xlsx", time.Now().Format("20060102_150405"))
	path := filepath.Join(tempdir.Dir, name)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("сохранить файл наклеек коробок: %w", err)
	}
	return path, nil
}

// newWorkbook собирает книгу наклеек (в памяти) и возвращает число
// сформированных наклеек.
func newWorkbook(boxes []Box) (*excelize.File, int, error) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)

	styles, err := newStyles(f)
	if err != nil {
		return nil, 0, err
	}

	row := 1
	labels := 0
	for _, b := range boxes {
		weight := b.WeightG
		if !b.Weighted || weight <= 0 {
			weight = sentinelWeightG
		}
		code, err := innercode.EncodeBox(b.InternalCode, weight, b.Qty, b.ProducedOn, b.BestBefore)
		if err != nil {
			continue // наклейку из этого кода не собрать — коробка пропускается
		}

		pngBytes, err := generateBarcodePNG(code, barcodeW, barcodeH)
		if err != nil {
			return nil, 0, fmt.Errorf("штрих-код %s: %w", code, err)
		}

		// Строка 1 блока — отступ, чтобы наклейка не прилипала к краю листа.
		setRow(f, sheet, styles.plain, row, "")
		row++

		// Строка 2: штрих-код.
		axis := fmt.Sprintf("B%d", row)
		_ = f.AddPictureFromBytes(sheet, axis, &excelize.Picture{
			Extension: ".png",
			File:      pngBytes,
			Format: &excelize.GraphicOptions{
				ScaleX:      1.0,
				ScaleY:      1.0,
				OffsetX:     imgOffsetX,
				OffsetY:     imgOffsetY,
				Positioning: "oneCell",
			},
		})
		setRow(f, sheet, styles.plain, row, "")
		row++

		// Строка 3: цифры кода (ручной ввод, если сканер не читает).
		setRow(f, sheet, styles.digits, row, code)
		row++

		// Строка 4: наименование товара.
		setRow(f, sheet, styles.name, row, b.ProductName)
		row++

		// Строка 5: вес и число вложений.
		setRow(f, sheet, styles.amount, row, caption(b))
		row++

		// Строка 6: даты — выработка и срок, по строке на дату (две даты одной
		// строкой в 75 мм не влезают при шрифте 14).
		setRow(f, sheet, styles.name, row, dateCaption(b))
		row++

		// Строка 7: нижний отступ.
		setRow(f, sheet, styles.plain, row, "")
		row++

		labels++
	}

	_ = f.SetColWidth(sheet, "B", "B", colWidthChars)
	if labels > 0 {
		printArea := fmt.Sprintf("%s!$B$1:$B$%d", sheet, row-1)
		_ = f.SetDefinedName(&excelize.DefinedName{
			Name:     "_xlnm.Print_Area",
			RefersTo: printArea,
			Scope:    sheet,
		})
		// Разрыв страницы после каждой наклейки: блоки по 6 строк.
		for r := len(rowHeightMM) + 1; r <= row; r += len(rowHeightMM) {
			_ = f.InsertPageBreak(sheet, fmt.Sprintf("B%d", r))
		}
	}
	return f, labels, nil
}

// labelStyles — стили строк наклейки (все — с выравниванием по центру).
type labelStyles struct {
	plain  int
	digits int
	name   int
	amount int
}

// newStyles заводит стили наклейки: общий (отступы), цифры кода, наименование
// (с переносом длинных названий) и строка веса с числом вложений.
func newStyles(f *excelize.File) (labelStyles, error) {
	newStyle := func(font float64, wrap bool) (int, error) {
		return f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Size: font},
			Alignment: &excelize.Alignment{
				Horizontal: "center",
				Vertical:   "center",
				WrapText:   wrap,
			},
		})
	}
	var s labelStyles
	var err error
	if s.plain, err = newStyle(fontName, false); err != nil {
		return labelStyles{}, err
	}
	if s.digits, err = newStyle(fontDigits, false); err != nil {
		return labelStyles{}, err
	}
	if s.name, err = newStyle(fontName, true); err != nil {
		return labelStyles{}, err
	}
	if s.amount, err = newStyle(fontAmount, false); err != nil {
		return labelStyles{}, err
	}
	return s, nil
}

// setRow пишет значение в колонку B заданной строки, ставит стиль и высоту
// строки по лейауту (heightIdx — индекс строки внутри блока наклейки).
// setRow пишет значение в колонку B заданной строки, ставит стиль и высоту
// строки по раскладке наклейки (rowHeightMM).
func setRow(f *excelize.File, sheet string, style, row int, value string) {
	axis := fmt.Sprintf("B%d", row)
	_ = f.SetCellValue(sheet, axis, value)
	_ = f.SetCellStyle(sheet, axis, axis, style)
	_ = f.SetRowHeight(sheet, row, rowHeightMM[(row-1)%len(rowHeightMM)]*mmToPt)
}

// caption — строка под штрих-кодом: вес и число вложений. У штучного товара
// веса нет (в коде стоит заглушка 1 г) — печатается только число вложений.
func caption(b Box) string {
	qty := "вложений: " + strconv.Itoa(b.Qty)
	if !b.Weighted || b.WeightG <= 0 {
		return qty
	}
	return "вес: " + formatKg(b.WeightG) + " кг   " + qty
}

// dateCaption — даты коробки: выработка и срок годности, каждая с новой строки.
func dateCaption(b Box) string {
	return "выработка " + b.ProducedOn.Format("02.01.2006") +
		"\nсрок " + b.BestBefore.Format("02.01.2006")
}

// formatKg переводит граммы в килограммы с запятой и без хвостовых нулей:
// 2500 → «2,5», 500 → «0,5», 2550 → «2,55».
func formatKg(weightG int64) string {
	kg := strconv.FormatFloat(float64(weightG)/1000, 'f', 3, 64)
	kg = strings.TrimRight(kg, "0")
	kg = strings.TrimRight(kg, ".")
	return strings.Replace(kg, ".", ",", 1)
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

// mmToPt — миллиметры в пункты: высоты строк в xlsx задаются в pt.
const mmToPt = 2.834646

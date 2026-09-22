// Package boxlabel печатает наклейки коробок (xlsx): 33-значный внутренний код
// коробки в Code128, ниже его цифры, наименование товара, вес с числом вложений
// и даты — выработка и срок. Лист 75 мм шириной, одна наклейка = одна страница.
// Пакет — нижний слой: его вызывают и приёмка (наклейки принятых коробок), и
// страница «Создать коробку» в «Продукции» — геометрия печати живёт в одном
// месте.
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

// Геометрия наклейки — числа образца, принятого владельцем (22.09.2026):
// колонка B шириной 75 мм, блок из шести строк (штрих-код 48,75 pt и пять строк
// подписей по 15,75 pt — высота строки шрифта 12), картинка штрих-кода
// 272 × 113 px (72 × 30 мм) со смещением 5 × 3 px внутри ячейки, поля листа
// нулевые. Ширина колонки в «символах» ≈ (px − 5) / 7 — метрика рендера Excel,
// не конвертер excelize: 75 мм ≈ 283 px ≈ 39,855 символа.
const (
	colWidthChars = 39.85546875

	// Штрих-код: 33 цифры Code128 дают минимум 233 px, растягиваем до 72 × 15 мм —
	// модуль шире, читаемость лучше. 272 px < 283 px колонки.
	barcodeW = 272
	barcodeH = 57

	// fontSize — шрифт строк подписей: цифры кода, наименование, вес с числом
	// вложений, даты.
	fontSize = 12

	// Смещение картинки внутри ячейки (px): по центру колонки и от верха.
	imgOffsetX = 5
	imgOffsetY = 3

	// sentinelWeightG — заглушка веса для штучного товара: у штучных веса нет,
	// но поле веса в 33-значном коде не может быть пустым (в коде учитывается
	// только количество). В данные приёмки и отчёты заглушка не попадает.
	sentinelWeightG int64 = 1
)

// rowHeightPt — высоты строк одной наклейки: штрих-код, цифры кода,
// наименование, вес с числом вложений, выработка, срок. 48,75 pt = 65 px —
// строка под картинку штрих-кода (15 мм + смещение 3 px), 15,75 pt — строка
// шрифта 12.
var rowHeightPt = []float64{48.75, 15.75, 15.75, 15.75, 15.75, 15.75}

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

		// Строка 1: штрих-код.
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

		// Строка 2: цифры кода (ручной ввод, если сканер не читает).
		setRow(f, sheet, styles.digits, row, code)
		row++

		// Строка 3: наименование товара.
		setRow(f, sheet, styles.text, row, b.ProductName)
		row++

		// Строка 4: вес и число вложений.
		setRow(f, sheet, styles.amount, row, caption(b))
		row++

		// Строки 5-6: даты — выработка («от») и срок годности («до»).
		setRow(f, sheet, styles.text, row, producedCaption(b))
		row++
		setRow(f, sheet, styles.text, row, bestBeforeCaption(b))
		row++

		labels++
	}

	_ = f.SetColWidth(sheet, "B", "B", colWidthChars)
	// Поля листа — нулевые (числа образца): наклейка печатается от края листа.
	_ = f.SetPageMargins(sheet, zeroMargins())
	if labels > 0 {
		printArea := fmt.Sprintf("%s!$B$1:$B$%d", sheet, row-1)
		_ = f.SetDefinedName(&excelize.DefinedName{
			Name:     "_xlnm.Print_Area",
			RefersTo: printArea,
			Scope:    sheet,
		})
		// Разрыв страницы после каждой наклейки: блоки по шесть строк.
		for r := len(rowHeightPt) + 1; r <= row; r += len(rowHeightPt) {
			_ = f.InsertPageBreak(sheet, fmt.Sprintf("B%d", r))
		}
	}
	return f, labels, nil
}

// labelStyles — стили строк наклейки (все — с выравниванием по центру).
type labelStyles struct {
	plain  int // строки без значения: под картинку штрих-кода
	digits int // цифры кода: без переноса, чтобы код читался одной строкой
	text   int // наименование и даты: с переносом длинных значений
	amount int // вес с числом вложений: без переноса
}

// newStyles заводит стили наклейки: один шрифт, различие — перенос значений.
func newStyles(f *excelize.File) (labelStyles, error) {
	newStyle := func(wrap bool) (int, error) {
		return f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Size: fontSize},
			Alignment: &excelize.Alignment{
				Horizontal: "center",
				Vertical:   "center",
				WrapText:   wrap,
			},
		})
	}
	var s labelStyles
	var err error
	if s.plain, err = newStyle(false); err != nil {
		return labelStyles{}, err
	}
	if s.digits, err = newStyle(false); err != nil {
		return labelStyles{}, err
	}
	if s.text, err = newStyle(true); err != nil {
		return labelStyles{}, err
	}
	if s.amount, err = newStyle(false); err != nil {
		return labelStyles{}, err
	}
	return s, nil
}

// setRow пишет значение в колонку B заданной строки, ставит стиль и высоту
// строки по раскладке наклейки (rowHeightPt).
func setRow(f *excelize.File, sheet string, style, row int, value string) {
	axis := fmt.Sprintf("B%d", row)
	_ = f.SetCellValue(sheet, axis, value)
	_ = f.SetCellStyle(sheet, axis, axis, style)
	_ = f.SetRowHeight(sheet, row, rowHeightPt[(row-1)%len(rowHeightPt)])
}

// caption — строка под наименованием: вес и число вложений. У штучного товара
// веса нет (в коде стоит заглушка 1 г) — печатается только число вложений.
func caption(b Box) string {
	qty := "вложений: " + strconv.Itoa(b.Qty)
	if !b.Weighted || b.WeightG <= 0 {
		return qty
	}
	return "вес: " + formatKg(b.WeightG) + " кг   " + qty
}

// producedCaption — дата выработки коробки строкой «от ДД.ММ.ГГГГ».
func producedCaption(b Box) string {
	return "от " + b.ProducedOn.Format("02.01.2006")
}

// bestBeforeCaption — срок годности коробки строкой «до ДД.ММ.ГГГГ».
func bestBeforeCaption(b Box) string {
	return "до " + b.BestBefore.Format("02.01.2006")
}

// formatKg переводит граммы в килограммы с запятой и без хвостовых нулей:
// 2500 → «2,5», 500 → «0,5», 2550 → «2,55».
func formatKg(weightG int64) string {
	kg := strconv.FormatFloat(float64(weightG)/1000, 'f', 3, 64)
	kg = strings.TrimRight(kg, "0")
	kg = strings.TrimRight(kg, ".")
	return strings.Replace(kg, ".", ",", 1)
}

// Спец-коды приёмки: их перехватывает страница (JS) до резолва, поэтому на
// наклейке достаточно штрих-кода Code128 с цифрами — операция выполняется
// сканом вместо нажатия кнопки.
const (
	breakCode   = "666" // закрытие коробки
	openBoxCode = "555" // открытие карточки коробки (кнопка «+ Коробка»)
)

// ExportBreakMarker формирует xlsx с наклейкой спец-кода закрытия коробки
// (666). Штрих-код — той же геометрией, что наклейка коробки (72 × 15 мм),
// ниже цифры кода; печатают на лист 75 мм и клеят у сканера.
func ExportBreakMarker() (string, error) {
	return exportMarker(breakCode)
}

// ExportOpenBoxMarker формирует xlsx с наклейкой спец-кода открытия коробки
// (555): скан равен нажатию «+ Коробка» на странице приёмки.
func ExportOpenBoxMarker() (string, error) {
	return exportMarker(openBoxCode)
}

// exportMarker собирает файл наклейки спец-кода (одна наклейка = страница).
func exportMarker(code string) (string, error) {
	f, err := newMarkerWorkbook(code)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	name := fmt.Sprintf("box_cmd_%s_%s.xlsx", code, time.Now().Format("20060102_150405"))
	path := filepath.Join(tempdir.Dir, name)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("сохранить файл наклейки %s: %w", code, err)
	}
	return path, nil
}

// newMarkerWorkbook собирает книгу наклейки спец-кода: строка штрих-кода и
// строка цифр — высоты берутся из раскладки наклейки коробки (rowHeightPt).
func newMarkerWorkbook(code string) (*excelize.File, error) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)

	styles, err := newStyles(f)
	if err != nil {
		return nil, err
	}
	pngBytes, err := generateBarcodePNG(code, barcodeW, barcodeH)
	if err != nil {
		return nil, fmt.Errorf("штрих-код %s: %w", code, err)
	}

	_ = f.AddPictureFromBytes(sheet, "B1", &excelize.Picture{
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
	setRow(f, sheet, styles.plain, 1, "")
	setRow(f, sheet, styles.digits, 2, code)

	_ = f.SetColWidth(sheet, "B", "B", colWidthChars)
	_ = f.SetPageMargins(sheet, zeroMargins())
	_ = f.SetDefinedName(&excelize.DefinedName{
		Name:     "_xlnm.Print_Area",
		RefersTo: sheet + "!$B$1:$B$2",
		Scope:    sheet,
	})
	// Разрыв после наклейки: следующая начинается с новой страницы.
	_ = f.InsertPageBreak(sheet, "B3")
	return f, nil
}

// zeroMargins — нулевые поля страницы: образец владельца печатается без
// отступов от края листа (умолчания Excel — 0,75″/0,7″ — сдвигали бы наклейку).
func zeroMargins() *excelize.PageLayoutMarginsOptions {
	zero := func() *float64 { v := 0.0; return &v }
	return &excelize.PageLayoutMarginsOptions{
		Bottom: zero(), Footer: zero(), Header: zero(), Left: zero(), Right: zero(), Top: zero(),
	}
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

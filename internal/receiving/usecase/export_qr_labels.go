package usecase

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"path/filepath"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/tempdir"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/xuri/excelize/v2"
)

// Геометрия QR-наклейки — числа образца, принятого владельцем (02.10.2026):
// наклейка = две ячейки одного столбца (верхняя — код текстом, нижняя —
// картинка QR), ширина ячейки 15 симв. (110 px), верхняя строка 33 px
// (24,75 pt), нижняя 79 px (59,25 pt). На страницу печати идут ДВЕ наклейки —
// столбцы B и C одной пары строк, — поэтому в область печати входят оба
// столбца, а отсечка страницы ставится после каждой второй строки.
const (
	qrColWidth = 15.0
	// Высоты строк Excel задаются в пунктах, а размеры образца — в пикселях
	// (1 px = 0,75 pt при 96 dpi).
	qrCodeRowH = 24.75
	qrImgRowH  = 59.25
	// Размер ячейки в пикселях — для центрирования картинки QR внутри нижней
	// ячейки: 15 симв. ширины дают 110 px, высота нижней строки 79 px.
	qrCellW = 110
	qrCellH = 79
	// Шрифт верхней ячейки — 9 с переносом, как на этикетке кусков: в ней
	// 29-значный код целиком, а в одну строку при ширине 110 px он не влезает
	// (29 цифр шрифтом 12 — около 220 px). Шрифт 9 даёт ~19 цифр в строке, то
	// есть две строки в ячейке высотой 24,75 pt.
	qrFontSize = 9
	// Товарная текстовая информация (наименование, вес, даты) в наклейку не
	// входит — по требованию владельца её убрали.
)

// errNoQRLabels — нет ни одного куска, из которого можно собрать QR-наклейку.
var errNoQRLabels = errors.New("ни у одного куска нет полных данных для QR-наклейки (нужна дата выработки)")

// ExportQRLabels формирует xlsx-файл QR-наклеек принятых кусков в tempdir и
// возвращает путь к нему. QR-наклейка — замена Code128-этикетки: в QR и в
// надпись над ним идёт один и тот же полный внутренний код куска
// (innercode.EncodeItem), чтобы скан шёл в заказы и расформирования так же, как
// со штрих-кода. Куски без полных данных (нет даты выработки, нулевой вес)
// пропускаются.
func (uc *ReceivingUseCase) ExportQRLabels(units []receiving.Unit) (string, error) {
	done := metrics.Track(trackPkg, "ExportQRLabels")
	defer done()

	f, labels, err := newQRLabelsWorkbook(units)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if labels == 0 {
		return "", errNoQRLabels
	}

	name := fmt.Sprintf("receive_qr_labels_%s.xlsx", time.Now().Format("20060102_150405"))
	path := filepath.Join(tempdir.Dir, name)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("сохранить файл QR-наклеек: %w", err)
	}
	return path, nil
}

// qrStyles — стили ячеек наклейки. У ЛЕВОЙ наклейки (столбец B) обе ячейки
// имеют обычную правую границу — при печати она отделяет левую наклейку от
// правой, по ней и режут лист. У правой наклейки границы нет.
type qrStyles struct {
	codeLeft  int // верхняя ячейка левой наклейки: код + правая граница
	codeRight int // верхняя ячейка правой наклейки: только код
	imgLeft   int // нижняя ячейка левой наклейки: пусто + правая граница
}

// newQRStyles заводит стили наклейки: код — по центру ячейки с переносом
// длинного значения на две строки, граница — тонкая чёрная справа
// (Style 1 = thin).
func newQRStyles(f *excelize.File) (qrStyles, error) {
	newStyle := func(style *excelize.Style) (int, error) {
		return f.NewStyle(style)
	}
	align := &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true}
	rightBorder := []excelize.Border{{Type: "right", Color: "000000", Style: 1}}

	var styles qrStyles
	var err error
	styles.codeLeft, err = newStyle(&excelize.Style{
		Font:      &excelize.Font{Size: qrFontSize},
		Alignment: align,
		Border:    rightBorder,
	})
	if err != nil {
		return qrStyles{}, err
	}
	styles.codeRight, err = newStyle(&excelize.Style{
		Font:      &excelize.Font{Size: qrFontSize},
		Alignment: align,
	})
	if err != nil {
		return qrStyles{}, err
	}
	styles.imgLeft, err = newStyle(&excelize.Style{Border: rightBorder})
	if err != nil {
		return qrStyles{}, err
	}
	return styles, nil
}

// newQRLabelsWorkbook собирает книгу QR-наклеек (в памяти) и возвращает число
// сформированных наклеек. Раскладка: наклейки идут парами — чётная в столбец B,
// нечётная в столбец C, — пара занимает две строки (верхняя — код, нижняя —
// картинка) и печатается на отдельной странице.
func newQRLabelsWorkbook(units []receiving.Unit) (*excelize.File, int, error) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)

	styles, err := newQRStyles(f)
	if err != nil {
		return nil, 0, err
	}

	topRow := 1 // верхняя строка текущей пары
	lastRow := 1
	labels := 0
	for _, u := range units {
		var produced time.Time
		if u.ProducedOn != nil {
			produced = *u.ProducedOn
		}
		weight := u.WeightG
		if !u.Weighted || weight <= 0 {
			weight = sentinelWeightG
		}
		code, err := innercode.EncodeItem(u.InternalCode, weight, produced, u.BestBefore)
		if err != nil {
			continue // полного внутреннего кода нет — наклейку не собрать
		}

		// Наклейки идут парами: чётная — левая (столбец B), нечётная — правая (C).
		col := "B"
		if labels%2 == 1 {
			col = "C"
		}
		topAxis := fmt.Sprintf("%s%d", col, topRow)
		imgAxis := fmt.Sprintf("%s%d", col, topRow+1)

		// Верхняя ячейка: 29-значный код куска текстом — тот же, что и в QR
		// (как на этикетке: визуальный контроль и ручной ввод, если сканер не
		// читает картинку).
		_ = f.SetCellValue(sheet, topAxis, code)
		// Нижняя ячейка: картинка QR-кода.
		pngBytes, size, err := generateQRPNG(code, qrCellW, qrCellH)
		if err != nil {
			return nil, 0, fmt.Errorf("QR-код %s: %w", code, err)
		}
		_ = f.AddPictureFromBytes(sheet, imgAxis, &excelize.Picture{
			Extension: ".png",
			File:      pngBytes,
			Format: &excelize.GraphicOptions{
				ScaleX:  1.0,
				ScaleY:  1.0,
				OffsetX: (qrCellW - size) / 2,
				OffsetY: (qrCellH - size) / 2,
				// Positioning: "oneCell" — картинка не растягивается вместе с
				// ячейкой; размер задан пикселями PNG.
				Positioning: "oneCell",
			},
		})

		if col == "B" {
			_ = f.SetCellStyle(sheet, topAxis, topAxis, styles.codeLeft)
			_ = f.SetCellStyle(sheet, imgAxis, imgAxis, styles.imgLeft)
		} else {
			_ = f.SetCellStyle(sheet, topAxis, topAxis, styles.codeRight)
		}

		lastRow = topRow + 1
		labels++
		if labels%2 == 0 {
			topRow += 2 // пара строк заполнена — следующая страница
		}
	}

	// Высоты строк — по образцу: верхняя 24,75 pt (33 px), нижняя 59,25 pt (79 px).
	for r := 1; r <= lastRow; r += 2 {
		_ = f.SetRowHeight(sheet, r, qrCodeRowH)
		_ = f.SetRowHeight(sheet, r+1, qrImgRowH)
	}
	_ = f.SetColWidth(sheet, "B", "C", qrColWidth)

	if labels > 0 {
		// В область печати входят оба столбца наклеек.
		printArea := fmt.Sprintf("%s!$B$1:$C$%d", sheet, lastRow)
		_ = f.SetDefinedName(&excelize.DefinedName{
			Name:     "_xlnm.Print_Area",
			RefersTo: printArea,
			Scope:    sheet,
		})
		// Отсечка страницы после каждой второй строки: страница = пара наклеек
		// (строки 1-2, 3-4, ...).
		for r := 3; r <= lastRow; r += 2 {
			_ = f.InsertPageBreak(sheet, fmt.Sprintf("B%d", r))
		}
	}
	return f, labels, nil
}

// generateQRPNG создаёт PNG-байты QR-кода и возвращает его размер в пикселях.
// QR — матричный код, поэтому масштаб берётся ЦЕЛЫМ числом (модуль кода должен
// оставаться квадратным, иначе сканер не прочитает картинку): factor —
// максимум, при котором код влезает в ячейку. Не целой остаётся величина
// отступа — центрирование делает вызывающий (OffsetX/OffsetY картинки).
func generateQRPNG(data string, cellW, cellH int) ([]byte, int, error) {
	code, err := qr.Encode(data, qr.M, qr.Auto)
	if err != nil {
		return nil, 0, err
	}
	bounds := code.Bounds()
	modules := bounds.Dx()
	factor := min(cellW/modules, cellH/modules)
	if factor < 1 {
		return nil, 0, fmt.Errorf("код %d×%d модулей не влезает в ячейку %d×%d px", modules, modules, cellW, cellH)
	}
	size := modules * factor

	scaled, err := barcode.Scale(code, size, size)
	if err != nil {
		return nil, 0, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), size, nil
}

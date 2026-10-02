package usecase

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"path/filepath"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/tempdir"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/xuri/excelize/v2"
)

// Параметры листа QR-наклеек — числа владельца (02.10.2026): на страницу печати
// две наклейки (столбцы B и C одной пары строк), наклейка — две ячейки: верхняя
// с кодом куска, нижняя с картинкой QR. Ширина ячейки 15 символов (110 px),
// верхняя строка 33 px, нижняя 79 px, отсечка страницы после каждой второй
// строки. У левой наклейки пары правая граница — по этой линии лист режут.
const (
	qrColWidth = 15.0 // ширина столбца в «символах» Excel: 15 → 110 px
	// Высоты строк в pt: Excel хранит высоту в пунктах, не в пикселях
	// (33 px = 24,75 pt, 79 px = 59,25 pt).
	qrCodeRowH = 24.75
	qrImgRowH  = 59.25
	// Шрифт надписи 9 с переносом: 29 цифр кода в 110 px в одну строку не
	// влезают — при шрифте 9 это ≈18 цифр на строку, то есть две строки.
	qrFontSize = 9
	// Стороны ячейки в пикселях: нужны для целого масштаба модуля QR и его
	// центрирования в нижней ячейке.
	qrCellW = 110
	qrCellH = 79
)

// errNoQRLabels — ни одного куска, из которого можно собрать QR-наклейку.
var errNoQRLabels = errors.New("ни у одного куска нет полных данных для QR-наклейки (нужна дата выработки)")

// ExportQRLabels формирует xlsx-файл QR-наклеек принятых кусков в tempdir и
// возвращает путь к нему. В QR и в надпись над ним идёт один и тот же полный
// внутренний код куска (innercode.EncodeItem) — как на этикетке с Code128, чтобы
// скан шёл в заказы и расформирования. Куски без полных данных (нет даты
// выработки) пропускаются.
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

// newQRLabelsWorkbook собирает книгу QR-наклеек (в памяти) и возвращает число
// сформированных наклеек. Раскладка: наклейка n (с нуля) — столбец B при чётном
// n и C при нечётном, строка пары — 1 + 2*(n/2).
func newQRLabelsWorkbook(units []receiving.Unit) (*excelize.File, int, error) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)

	styles, err := newQRStyles(f)
	if err != nil {
		return nil, 0, err
	}

	labels := 0
	lastRow := 0
	for _, u := range units {
		code, ok := labelUnitCode(u)
		if !ok {
			continue // полного внутреннего кода нет — наклейку не собрать
		}

		col := "B"
		if labels%2 == 1 {
			col = "C"
		}
		topAxis := fmt.Sprintf("%s%d", col, 1+2*(labels/2))
		imgAxis := fmt.Sprintf("%s%d", col, 2+2*(labels/2))

		// Верхняя ячейка: код куска текстом (ручной ввод, если сканер не читает).
		codeStyle, imgStyle := styles.codeC, 0
		if col == "B" {
			codeStyle, imgStyle = styles.codeB, styles.imgB
		}
		_ = f.SetCellValue(sheet, topAxis, code)
		_ = f.SetCellStyle(sheet, topAxis, topAxis, codeStyle)
		// Нижняя ячейка: пусто под картинку; у левой наклейки — с границей.
		_ = f.SetCellStyle(sheet, imgAxis, imgAxis, imgStyle)

		img, err := generateQRPNG(code, qrCellW, qrCellH)
		if err != nil {
			return nil, 0, fmt.Errorf("QR-код %s: %w", code, err)
		}
		_ = f.AddPictureFromBytes(sheet, imgAxis, &excelize.Picture{
			Extension: ".png",
			File:      img.PNG,
			Format: &excelize.GraphicOptions{
				ScaleX:      1.0,
				ScaleY:      1.0,
				OffsetX:     (qrCellW - img.Size) / 2,
				OffsetY:     (qrCellH - img.Size) / 2,
				Positioning: "oneCell",
			},
		})

		labels++
		// lastRow — строка картинки последней наклейки: ряд пары 1 + 2*(n/2).
		lastRow = 2 + 2*((labels-1)/2)
	}

	_ = f.SetColWidth(sheet, "B", "C", qrColWidth)
	for row := 1; row <= lastRow; row += 2 {
		_ = f.SetRowHeight(sheet, row, qrCodeRowH)
		_ = f.SetRowHeight(sheet, row+1, qrImgRowH)
	}
	if labels > 0 {
		printArea := fmt.Sprintf("%s!$B$1:$C$%d", sheet, lastRow)
		_ = f.SetDefinedName(&excelize.DefinedName{
			Name:     "_xlnm.Print_Area",
			RefersTo: printArea,
			Scope:    sheet,
		})
		// Отсечка страницы после каждой второй строки (страницы — строки 1-2,
		// 3-4, ...): новая страница начинается с третьей строки и далее через две.
		for row := 3; row <= lastRow; row += 2 {
			_ = f.InsertPageBreak(sheet, fmt.Sprintf("B%d", row))
		}
	}
	return f, labels, nil
}

// qrStyles — стили ячеек наклейки. У левой наклейки пары (столбец B) правая
// граница отделяет её от правой наклейки — по этой линии лист режут при печати.
type qrStyles struct {
	codeB int // верхняя ячейка левой наклейки: код и правая граница.
	codeC int // верхняя ячейка правой наклейки: код без границы.
	imgB  int // нижняя ячейка левой наклейки: пусто и правая граница.
}

// newQRStyles заводит стили ячеек наклейки: шрифт с переносом для 29 цифр кода и
// правую границу у левой наклейки (Style 1 — обычная тонкая линия).
func newQRStyles(f *excelize.File) (qrStyles, error) {
	font := &excelize.Font{Size: qrFontSize}
	align := &excelize.Alignment{Horizontal: alignCenter, Vertical: alignCenter, WrapText: true}
	rightBorder := []excelize.Border{{Type: "right", Color: "000000", Style: 1}}

	codeB, err := f.NewStyle(&excelize.Style{Font: font, Alignment: align, Border: rightBorder})
	if err != nil {
		return qrStyles{}, err
	}
	codeC, err := f.NewStyle(&excelize.Style{Font: font, Alignment: align})
	if err != nil {
		return qrStyles{}, err
	}
	imgB, err := f.NewStyle(&excelize.Style{Border: rightBorder})
	if err != nil {
		return qrStyles{}, err
	}
	return qrStyles{codeB: codeB, codeC: codeC, imgB: imgB}, nil
}

// qrImage — готовая картинка QR-кода: PNG-байты и сторона квадрата в пикселях
// (нужна вызывающему, чтобы отцентрировать картинку в ячейке).
type qrImage struct {
	PNG  []byte
	Size int
}

// generateQRPNG создаёт PNG-байты QR-кода и его размер в пикселях. QR — матричный
// код, поэтому масштаб берётся ЦЕЛЫМ числом модулей (при нецелом модуль
// перестаёт быть квадратным и сканер картинку не прочитает): factor — максимум,
// при котором код влезает в ячейку cellW×cellH. Не целым остаётся только отступ —
// центрирование делает вызывающий.
func generateQRPNG(data string, cellW, cellH int) (qrImage, error) {
	code, err := qr.Encode(data, qr.M, qr.Auto)
	if err != nil {
		return qrImage{}, err
	}

	modules := code.Bounds().Dx()
	factor := min(cellW/modules, cellH/modules)
	if factor < 1 {
		return qrImage{}, fmt.Errorf("код %d×%d модулей не влезает в ячейку %d×%d px",
			modules, modules, cellW, cellH)
	}
	size := modules * factor

	scaled, err := barcode.Scale(code, size, size)
	if err != nil {
		return qrImage{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return qrImage{}, err
	}
	return qrImage{PNG: buf.Bytes(), Size: size}, nil
}

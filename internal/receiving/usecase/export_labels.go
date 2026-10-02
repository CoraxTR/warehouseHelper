package usecase

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
	"warehouseHelper/internal/tempdir"

	"github.com/xuri/excelize/v2"
)

// Параметры листа этикеток — числа владельца (02.10.2026). Этикетка занимает
// три ячейки и ровно одну страницу печати:
//
//	B1 — 29-значный внутренний код куска текстом (ручной ввод, если сканер
//	     не читает QR); высота 24,75 pt (33 px), ширина 20 символов (115 px);
//	B2 — подпись «наименование … до ДД.ММ.ГГГГ вес: N» с переносом и прижатая
//	     к низу ячейки; высота 49,50 pt (66 px);
//	C1:C2 — объединённая ячейка 100×99 px с QR-кодом того же кода.
//
// Ширина столбцов: B 15,71 (115 px), C 13,57 (100 px). Отсечка страницы — после
// каждой второй строки, то есть одна этикетка на страницу.
const (
	labelsFontSize  = 9
	labelsColBWidth = 15.71 // ширина столбца в «символах» Excel: 7 px на символ + 5 px
	labelsColCWidth = 13.57
	// Высоты строк в pt: Excel хранит высоту в пунктах, не в пикселях
	// (33 px = 24,75 pt, 66 px = 49,50 pt).
	labelsCodeRowH = 24.75
	labelsTextRowH = 49.5
	// Стороны объединённой ячейки C1:C2 в пикселях: нужны для целого масштаба
	// модуля QR и его центрирования (33 px + 66 px = 99 px высоты).
	labelsQRCellW = 100
	labelsQRCellH = 99
)

// errNoLabels — нет ни одного куска, из которого можно собрать этикетку.
var errNoLabels = errors.New("ни у одного куска нет полных данных для этикетки (нужна дата выработки)")

// sentinelWeightG — вес штучного товара в этикетке: 1 г. У штучных веса нет
// (в данные идёт 0), но поле веса 29-значного кода не может быть пустым —
// EncodeItem отвергает 0, и кусок остался бы без этикетки. Печатается только
// в код: в данные приёмки, отчёт и статистику весов 1 г не попадает.
const sentinelWeightG int64 = 1

// ExportLabels формирует xlsx-файл этикеток принятых кусков в tempdir и
// возвращает путь к нему. В QR-код этикетки кодируется полный внутренний код
// куска (innercode.EncodeItem) — как раньше в штрих-код, чтобы этикетка
// сканировалась как обычный кусок в заказы и расформирования. Куски без полных
// данных (нет даты выработки) пропускаются.
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
// сформированных этикеток. Этикетка n (с нуля) занимает строки 1+2n и 2+2n:
// в верхней — код, в нижней — подпись, в объединённой C-ячейке той же пары
// строк — QR.
func newLabelsWorkbook(units []receiving.Unit) (*excelize.File, int, error) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)

	styles, err := newLabelsStyles(f)
	if err != nil {
		return nil, 0, err
	}

	labels := 0
	for _, u := range units {
		code, ok := labelUnitCode(u)
		if !ok {
			continue // полного внутреннего кода нет — этикетку не собрать
		}
		img, err := generateQRPNG(code, labelsQRCellW, labelsQRCellH)
		if err != nil {
			return nil, 0, fmt.Errorf("QR-код %s: %w", code, err)
		}

		topRow, bottomRow := 1+2*labels, 2+2*labels
		codeAxis := fmt.Sprintf("B%d", topRow)
		textAxis := fmt.Sprintf("B%d", bottomRow)
		qrAxis := fmt.Sprintf("C%d", topRow)

		_ = f.SetCellValue(sheet, codeAxis, code)
		_ = f.SetCellStyle(sheet, codeAxis, codeAxis, styles.code)
		_ = f.SetCellValue(sheet, textAxis, labelCaption(u))
		_ = f.SetCellStyle(sheet, textAxis, textAxis, styles.caption)

		// Картинка кладётся после объединения: Excel якорит её по левой
		// верхней ячейке объединённого диапазона.
		_ = f.MergeCell(sheet, qrAxis, fmt.Sprintf("C%d", bottomRow))
		_ = f.AddPictureFromBytes(sheet, qrAxis, &excelize.Picture{
			Extension: ".png",
			File:      img.PNG,
			Format: &excelize.GraphicOptions{
				ScaleX:      1.0,
				ScaleY:      1.0,
				OffsetX:     (labelsQRCellW - img.Size) / 2,
				OffsetY:     (labelsQRCellH - img.Size) / 2,
				Positioning: "oneCell",
			},
		})

		labels++
	}

	_ = f.SetColWidth(sheet, "B", "B", labelsColBWidth)
	_ = f.SetColWidth(sheet, "C", "C", labelsColCWidth)
	for n := 0; n < labels; n++ {
		_ = f.SetRowHeight(sheet, 1+2*n, labelsCodeRowH)
		_ = f.SetRowHeight(sheet, 2+2*n, labelsTextRowH)
	}

	if labels > 0 {
		lastRow := 2 * labels
		printArea := fmt.Sprintf("%s!$B$1:$C$%d", sheet, lastRow)
		_ = f.SetDefinedName(&excelize.DefinedName{
			Name:     "_xlnm.Print_Area",
			RefersTo: printArea,
			Scope:    sheet,
		})
		// Отсечка страницы после каждой второй строки (страницы — строки 1-2,
		// 3-4, ...). Ссылка на столбец A — не «B»: excelize по ссылке заводит
		// ещё и отсечку по столбцу, а она тут лишняя.
		for row := 3; row <= lastRow; row += 2 {
			_ = f.InsertPageBreak(sheet, fmt.Sprintf("A%d", row))
		}
	}
	return f, labels, nil
}

// Выравнивание в стилях ячеек excelize — общее для обоих билдеров наклеек:
// строки держим константами (goconst), иначе «center» повторяется литералом.
const (
	alignCenter = "center"
	alignLeft   = "left"
	alignBottom = "bottom"
)

// labelsStyles — стили ячеек этикетки.
type labelsStyles struct {
	code    int // B1: код по центру, с переносом (29 цифр в 115 px — две строки)
	caption int // B2: подпись с переносом, прижата к низу ячейки
}

// newLabelsStyles заводит стили этикетки: шрифт 9 и перенос по словам у обоих
// текстовых полей.
func newLabelsStyles(f *excelize.File) (labelsStyles, error) {
	font := &excelize.Font{Size: labelsFontSize}

	code, err := f.NewStyle(&excelize.Style{
		Font:      font,
		Alignment: &excelize.Alignment{Horizontal: alignCenter, Vertical: alignCenter, WrapText: true},
	})
	if err != nil {
		return labelsStyles{}, err
	}
	caption, err := f.NewStyle(&excelize.Style{
		Font:      font,
		Alignment: &excelize.Alignment{Horizontal: alignLeft, Vertical: alignBottom, WrapText: true},
	})
	if err != nil {
		return labelsStyles{}, err
	}
	return labelsStyles{code: code, caption: caption}, nil
}

// labelUnitCode собирает полный внутренний код куска для этикетки. У штучного
// товара веса нет — в код уходит sentinelWeightG, иначе EncodeItem отвергнет
// нулевой вес и кусок остался бы без этикетки. Второй результат — false, когда
// полного кода нет (нет даты выработки): этикетку из такого куска не собрать.
func labelUnitCode(u receiving.Unit) (string, bool) {
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
		return "", false
	}
	return code, true
}

// labelCaption — подпись под кодом: наименование, срок и вес (у штучного веса
// нет — как и раньше на этикетке со штрих-кодом).
func labelCaption(u receiving.Unit) string {
	caption := u.ProductName + " до " + u.BestBefore.Format("02.01.2006")
	if u.Weighted {
		caption += " вес: " + strconv.FormatInt(u.WeightG, 10)
	}
	return caption
}

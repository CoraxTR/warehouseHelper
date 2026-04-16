package excel

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"log"
	"strconv"
	"time"
	"warehouseHelper/internal/domain"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/xuri/excelize/v2"
)

const cashPaymentMethod = "Наличные"
const cardPaymentMethod = "Терминал"
const wirePaymentMethod = "расч. счет"

type ExcelExporter struct{}

func NewExcelExporter() *ExcelExporter {
	return &ExcelExporter{}
}

func incrementStringCounterByInt(counter string, increment int) (string, error) {
	counterInt, err := strconv.Atoi(counter)
	if err != nil {
		return "", err
	}

	counterInt += increment

	return strconv.Itoa(counterInt), nil
}

func setPaymentInformation(f *excelize.File, sheet, paymentMethod, row string, sum float64) error {
	result := make([]error, 0)

	switch paymentMethod {
	case cashPaymentMethod, cardPaymentMethod:
		err := f.SetCellFloat(sheet, "M"+row, sum, -1, 64)
		if err != nil {
			result = append(result, err)
		}

		err = f.SetCellFloat(sheet, "N"+row, sum, -1, 64)
		if err != nil {
			result = append(result, err)
		}

		err = f.SetCellValue(sheet, "Q"+row, "Нет")
		if err != nil {
			result = append(result, err)
		}
	case wirePaymentMethod:
		err := f.SetCellFloat(sheet, "M"+row, 0, -1, 64)
		if err != nil {
			result = append(result, err)
		}

		err = f.SetCellFloat(sheet, "N"+row, 0, -1, 64)
		if err != nil {
			result = append(result, err)
		}

		err = f.SetCellValue(sheet, "Q"+row, "Да")
		if err != nil {
			result = append(result, err)
		}
	default:
		err := f.SetCellFloat(sheet, "M"+row, 0, -1, 64)
		if err != nil {
			result = append(result, err)
		}

		err = f.SetCellFloat(sheet, "N"+row, 0, -1, 64)
		if err != nil {
			result = append(result, err)
		}

		err = f.SetCellValue(sheet, "Q"+row, "Нет")
		if err != nil {
			result = append(result, err)
		}
	}

	return errors.Join(result...)
}

func setOrderMainInformation(f *excelize.File, sheet, row string, refGoNumber int, o *domain.InternalOrder) {
	err := f.SetCellValue(sheet, "A"+row, refGoNumber)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "B"+row, refGoNumber)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "C"+row, o.GetRecieverName())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "D"+row, o.GetRecieverPhoneNumber())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "E"+row, o.GetShipmentAddress())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "F"+row, o.GetDeliveryPlannedDate())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "G"+row, o.GetDeliveryIntervalFrom())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "H"+row, o.GetDeliveryIntervalUntil())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}

	err = f.SetCellValue(sheet, "I"+row, o.GetDescription())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", refGoNumber, err)
	}
}

func setChilledAsMainLine(f *excelize.File, sheet, row string, info *repeatableOrderInfo, o *domain.InternalOrder) {
	err := f.SetCellValue(sheet, "J"+row, "Охлаждённая продукция")
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "K"+row, info.refgonumber)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "L"+row, info.chilledBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = setPaymentInformation(f, sheet, info.paymentMethod, row, info.sum)
	if err != nil {
		log.Printf("Failed to set payment information for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "O"+row, o.GetChilledWeight())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "P"+row, "Средние температуры (+2+6)")
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "R"+row, info.region)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "S"+row, info.chilledBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}
}

func setFrozenAsSecondaryLine(f *excelize.File, sheet, row string, info *repeatableOrderInfo, o *domain.InternalOrder) {
	err := f.SetCellValue(sheet, "J"+row, "Замороженная продукция")
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "K"+row, info.refgonumber)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "L"+row, info.frozenBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellFloat(sheet, "M"+row, 0, -1, 64)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellFloat(sheet, "N"+row, 0, -1, 64)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "O"+row, o.GetFrozenWeight())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "P"+row, "Низкие температуры (-18)")
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	if info.paymentMethod == wirePaymentMethod {
		err = f.SetCellValue(sheet, "Q"+row, "Да")
		if err != nil {
			log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
		}
	} else {
		err = f.SetCellValue(sheet, "Q"+row, "Нет")
		if err != nil {
			log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
		}
	}

	err = f.SetCellValue(sheet, "R"+row, info.region)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "S"+row, info.frozenBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}
}

func setOnlyLine(f *excelize.File, sheet, row string, info *repeatableOrderInfo, o *domain.InternalOrder) {
	err := f.SetCellValue(sheet, "K"+row, info.refgonumber)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "L"+row, info.chilledBoxes+info.frozenBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = setPaymentInformation(f, sheet, info.paymentMethod, row, info.sum)
	if err != nil {
		log.Printf("Failed to set payment information for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "O"+row, o.GetChilledWeight()+o.GetFrozenWeight())
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "R"+row, info.region)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "S"+row, info.chilledBoxes+info.frozenBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}
}

func addOrderToSummary(f *excelize.File, sheet, row string, info *repeatableOrderInfo) {
	err := f.SetCellValue(sheet, "L"+row, info.refgonumber)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "M"+row, info.chilledBoxes+info.frozenBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}

	err = f.SetCellValue(sheet, "N"+row, info.sum)
	if err != nil {
		log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
	}
}

func fillSummarySheet(f *excelize.File, sheet string, ordersCount int, boxesCount uint64) {
	err := f.SetCellValue(sheet, "C15", ordersCount)
	if err != nil {
		log.Printf("Failed to set cell value for total orders: %v", err)
	}

	err = f.SetCellValue(sheet, "F15", boxesCount)
	if err != nil {
		log.Printf("Failed to set cell value for overall boxes: %v", err)
	}
}

func setOrderBoxesInformation(f *excelize.File, sheet, row string, info *repeatableOrderInfo, order *domain.InternalOrder) int {
	incrementCounter := 0

	switch {
	case info.chilledBoxes > 0 && info.frozenBoxes > 0:
		setChilledAsMainLine(f, sheet, row, info, order)

		incrementCounter++

		setFrozenAsSecondaryLine(f, sheet, row, info, order)

		incrementCounter++

	case info.chilledBoxes > 0 && info.frozenBoxes == 0:
		err := f.SetCellValue(sheet, "J"+row, "Охлаждённая продукция")
		if err != nil {
			log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
		}

		setOnlyLine(f, sheet, row, info, order)

		err = f.SetCellValue(sheet, "P"+row, "Средние температуры (+2+6)")
		if err != nil {
			log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
		}

		incrementCounter++
	case info.chilledBoxes == 0 && info.frozenBoxes > 0:
		err := f.SetCellValue(sheet, "J"+row, "Замороженная продукция")
		if err != nil {
			log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
		}

		setOnlyLine(f, sheet, row, info, order)

		err = f.SetCellValue(sheet, "P"+row, "Низкие температуры (-18)")
		if err != nil {
			log.Printf("Failed to set cell value for order %d: %v", info.refgonumber, err)
		}

		incrementCounter++
	default:
		log.Printf("Order %d has no boxes, skipping import", info.refgonumber)
	}

	return incrementCounter
}

type repeatableOrderInfo struct {
	refgonumber   int
	chilledBoxes  uint64
	frozenBoxes   uint64
	paymentMethod string
	region        string
	sum           float64
}

func (e *ExcelExporter) ExportOrdersToExcel(orders []*domain.InternalOrder) (savepath string, err error) {
	var importRowNumber, summaryRowNumber string

	var importSheet = "Импорт"

	var summarySheet = "Расписка"

	var overallBoxes uint64

	temptoday := time.Now()
	today := temptoday.Format("02.01.2006")
	savepath = "../" + today + ".xlsx"

	uploadFile, err := excelize.OpenFile("../blankimport.xlsx")
	if err != nil {
		panic(err)
	}

	importRowNumber = "2"
	summaryRowNumber = "7"

	for _, order := range orders {
		refgonumber, err := strconv.Atoi(order.GetRefGoNumber())
		if err != nil {
			log.Printf("Failed to convert RefGoNumber for order %d: %v", refgonumber, err)

			continue
		}

		info := &repeatableOrderInfo{
			refgonumber:   refgonumber,
			chilledBoxes:  order.GetChilledBoxes(),
			frozenBoxes:   order.GetFrozenBoxes(),
			paymentMethod: order.GetPaymentMethod(),
			region:        order.GetDeliveryRegion(),
			sum:           order.GetSum(),
		}

		setOrderMainInformation(uploadFile, importSheet, importRowNumber, info.refgonumber, order)
		incrementCounter := setOrderBoxesInformation(uploadFile, importSheet, importRowNumber, info, order)
		addOrderToSummary(uploadFile, summarySheet, summaryRowNumber, info)

		importRowNumber, err = incrementStringCounterByInt(importRowNumber, incrementCounter)
		if err != nil {
			log.Printf("Failed to increment import row number: %v", err)
		}

		summaryRowNumber, err = incrementStringCounterByInt(summaryRowNumber, 1)
		if err != nil {
			log.Printf("Failed to increment summary row number: %v", err)
		}

		overallBoxes += order.GetChilledBoxes() + order.GetFrozenBoxes()
	}

	fillSummarySheet(uploadFile, summarySheet, len(orders), overallBoxes)

	// TODO: Перекинуть сохранение в темп файл для скачивания
	err = uploadFile.SaveAs(savepath)
	if err != nil {
		log.Printf("Failed to save Excel file: %v", err)
	}

	return today + ".xlsx", nil
}

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

	err = png.Encode(&buf, scaled)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func setCellsStyle(f *excelize.File, sheet string, startRow, header, regular, toTheRight int) error {
	innerCounter := startRow

	err := f.SetRowHeight(sheet, innerCounter, 62)
	if err != nil {
		return err
	}

	err = f.MergeCell(sheet, fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter))
	if err != nil {
		return err
	}

	err = f.SetCellStyle(sheet, fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), header)
	if err != nil {
		return err
	}

	innerCounter++

	err = f.SetRowHeight(sheet, innerCounter, 20)
	if err != nil {
		return err
	}

	err = f.MergeCell(sheet, fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter))
	if err != nil {
		return err
	}

	err = f.SetCellStyle(sheet, fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), header)
	if err != nil {
		return err
	}

	innerCounter++

	for i := 0; i < 4; i++ {
		err = f.SetRowHeight(sheet, innerCounter+i, 12)
		if err != nil {
			return err
		}

		err = f.SetCellStyle(sheet, fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), regular)
		if err != nil {
			return err
		}

		innerCounter++
	}

	err = f.SetCellStyle(sheet, fmt.Sprintf("B%d", innerCounter), fmt.Sprintf("B%d", innerCounter), toTheRight)
	if err != nil {
		return err
	}

	innerCounter++

	err = f.SetRowHeight(sheet, innerCounter, -1)
	if err != nil {
		return err
	}

	err = f.SetCellStyle(sheet, fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), regular)
	if err != nil {
		return err
	}

	innerCounter++

	err = f.SetRowHeight(sheet, innerCounter, 12)
	if err != nil {
		return err
	}

	err = f.SetCellStyle(sheet, fmt.Sprintf("B%d", innerCounter), fmt.Sprintf("B%d", innerCounter), toTheRight)
	if err != nil {
		return err
	}

	return nil
}

func insertBarcodeIntoCell(f *excelize.File, sheet, refGoNumber string, cellNumber int) error {
	pngBytes, err := generateBarcodePNG(refGoNumber, 140, 55)
	if err != nil {
		log.Printf("Ошибка генерации: %v\n", err)
	}

	err = f.AddPictureFromBytes(sheet,
		fmt.Sprintf("A%d", cellNumber),
		&excelize.Picture{
			Extension: ".png",
			File:      pngBytes,
			Format: &excelize.GraphicOptions{
				ScaleX:      1.0,
				ScaleY:      1.0,
				OffsetY:     3,
				OffsetX:     100,
				Positioning: "oneCell",
			},
		})
	if err != nil {
		return err
	}

	return nil
}

func fillStaticCells(f *excelize.File, sheet string, rowNumber int) {
	innerCounter := rowNumber

	err := f.SetCellValue(sheet, fmt.Sprintf("A%d", innerCounter), "Наименование:")
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetCellValue(sheet, fmt.Sprintf("A%d", innerCounter), "Заказчик:")
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue(sheet, fmt.Sprintf("B%d", innerCounter), "STEAK HOME")
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetCellValue(sheet, fmt.Sprintf("A%d", innerCounter), "Получатель:")
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetCellValue(sheet, fmt.Sprintf("A%d", innerCounter), "Вх. накладная:")
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetCellValue(sheet, fmt.Sprintf("A%d", innerCounter), "Адрес доставки:")
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}
}

func fillSingularBarcode(f *excelize.File, o *domain.InternalOrder, boxState string, boxnumber int, totalboxes uint64, rowNumber, header, regular, toTheRight int) {
	innerCounter := rowNumber
	workSheet := "Sheet1"
	refGoNumber := o.GetRefGoNumber()

	err := f.SetCellValue(workSheet, fmt.Sprintf("A%d", innerCounter), refGoNumber)
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	err = insertBarcodeIntoCell(f, workSheet, refGoNumber, innerCounter)
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel while inserting PNG", err)
	}

	innerCounter++

	if boxState == "Охл" {
		err = f.SetCellValue(workSheet, fmt.Sprintf("A%d", innerCounter), "Среднетемпературный режим (+2+6)")
		if err != nil {
			log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
		}

		err = f.SetCellValue(workSheet, fmt.Sprintf("B%d", innerCounter+1), "Охлаждённая продукция")
		if err != nil {
			log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
		}
	} else {
		err = f.SetCellValue(workSheet, fmt.Sprintf("A%d", innerCounter), "Низкотемпературный (-18)")
		if err != nil {
			log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
		}

		err = f.SetCellValue(workSheet, fmt.Sprintf("B%d", innerCounter+1), "Замороженная продукция")
		if err != nil {
			log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
		}
	}

	innerCounter++

	fillStaticCells(f, workSheet, innerCounter)

	innerCounter += 2

	err = f.SetCellValue(workSheet, fmt.Sprintf("B%d", innerCounter), o.GetRecieverName())
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetCellValue(workSheet, fmt.Sprintf("B%d", innerCounter), refGoNumber)
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetCellValue(workSheet, fmt.Sprintf("B%d", innerCounter), o.GetShipmentAddress())
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetCellValue(workSheet, fmt.Sprintf("B%d", innerCounter), fmt.Sprintf("(%d из %d)", boxnumber, totalboxes))
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	err = setCellsStyle(f, workSheet, rowNumber, header, regular, toTheRight)
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel while setting cell styles", err)
	}
}

func createxlsxStyles(f *excelize.File) (regular, right, header int) {
	regular, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Size:   8,
			Bold:   true,
			Family: "Arial",
		},
		Alignment: &excelize.Alignment{
			WrapText: true,
		},
	})
	if err != nil {
		log.Printf("err: %s occurred creating regular style", err)
	}

	right, err = f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Size:   8,
			Bold:   true,
			Family: "Arial",
		},
		Alignment: &excelize.Alignment{
			WrapText:   true,
			Horizontal: "right",
		},
	})
	if err != nil {
		log.Printf("err: %s occurred creating rightCell style", err)
	}

	header, err = f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Size:   14,
			Bold:   true,
			Family: "Arial",
		},
		Alignment: &excelize.Alignment{
			Horizontal: "center",
			Vertical:   "bottom",
			WrapText:   true,
		},
	})
	if err != nil {
		log.Printf("err: %s occurred creating header style", err)
	}

	return regular, right, header
}

func (e *ExcelExporter) ExportOrdersBarcodesToExcel(orders []*domain.InternalOrder) (savepath string, err error) {
	f := excelize.NewFile()

	regular, right, header := createxlsxStyles(f)

	err = f.SetColWidth("Sheet1", "A", "A", 14)
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetColWidth("Sheet1", "B", "B", 36.7)
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel", err)
	}

	counter := 1

	for _, o := range orders {
		totalboxes := o.GetChilledBoxes() + o.GetFrozenBoxes()
		totalcount := 1

		var i uint64

		if o.GetChilledBoxes() > 0 {
			for i = 1; i <= o.GetChilledBoxes(); i++ {
				fillSingularBarcode(f, o, "Охл", totalcount, totalboxes, counter, header, regular, right)

				totalcount++
				counter += 8
			}
		}

		if o.GetFrozenBoxes() > 0 {
			for i = 1; i <= o.GetFrozenBoxes(); i++ {
				fillSingularBarcode(f, o, "Зам", totalcount, totalboxes, counter, header, regular, right)

				totalcount++
				counter += 8
			}
		}
	}

	printArea := fmt.Sprintf("Sheet1!$A$1:$B$%d", counter-1)

	err = f.SetDefinedName(&excelize.DefinedName{
		Name:     "_xlnm.Print_Area",
		RefersTo: printArea,
		Scope:    "Sheet1",
	})
	if err != nil {
		panic(err)
	}

	for row := 9; row <= counter; row += 8 {
		cell := fmt.Sprintf("A%d", row)

		err := f.InsertPageBreak("Sheet1", cell)
		if err != nil {
			log.Printf("%s occurred in ExportOrdersBarcodesToExcel while inserting page break", err)
		}
	}

	temptoday := time.Now()
	today := temptoday.Format("02.01.2006")

	savepath = "../" + today + ".xlsx"

	err = f.SaveAs(savepath)
	if err != nil {
		log.Printf("%s occurred in ExportOrdersBarcodesToExcel while saving file", err)

		return "", err
	}

	return savepath, nil
}

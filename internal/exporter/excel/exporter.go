package excel

import (
	"bytes"
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

type ExcelExporter struct{}

func NewExcelExporter() *ExcelExporter {
	return &ExcelExporter{}
}

func IncrementStringCounter(counter string) (string, error) {
	counterInt, err := strconv.Atoi(counter)
	if err != nil {
		return "", err
	}

	counterInt++

	return strconv.Itoa(counterInt), nil
}

func SetIntValueofString(file *excelize.File, sheet, cell, value string) error {
	intValue, err := strconv.Atoi(value)
	if err != nil {
		return err
	}

	err = file.SetCellInt(sheet, cell, int64(intValue))
	if err != nil {
		return err
	}

	return nil
}

func SetFloatValueofString(file *excelize.File, sheet, cell, value string) error {
	floatValue, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return err
	}

	err = file.SetCellFloat(sheet, cell, floatValue, -1, 64)
	if err != nil {
		return err
	}

	return nil
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
		err = SetIntValueofString(uploadFile, importSheet, "A"+importRowNumber, order.GetRefGoNumber())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = SetIntValueofString(uploadFile, importSheet, "B"+importRowNumber, order.GetRefGoNumber())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(importSheet, "C"+importRowNumber, order.GetRecieverName())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(importSheet, "D"+importRowNumber, order.GetRecieverPhoneNumber())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(importSheet, "E"+importRowNumber, order.GetShipmentAddress())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(importSheet, "F"+importRowNumber, order.GetDeliveryPlannedDate())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(importSheet, "G"+importRowNumber, order.GetDeliveryIntervalFrom())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(importSheet, "H"+importRowNumber, order.GetDeliveryIntervalUntil())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(importSheet, "I"+importRowNumber, order.GetDescription())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		chilledBoxes := order.GetChilledBoxes()

		frozenBoxes := order.GetFrozenBoxes()

		switch {
		case chilledBoxes > 0 && frozenBoxes > 0:
			err = uploadFile.SetCellValue(importSheet, "J"+importRowNumber, "Охлаждённая продукция")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = SetIntValueofString(uploadFile, importSheet, "K"+importRowNumber, order.GetRefGoNumber())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "L"+importRowNumber, order.GetChilledBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			if order.GetPaymentMethod() == "Наличные" || order.GetPaymentMethod() == "Терминал" {
				err = uploadFile.SetCellFloat(importSheet, "M"+importRowNumber, order.GetSum(), -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}

				err = uploadFile.SetCellFloat(importSheet, "N"+importRowNumber, order.GetSum(), -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			} else {
				err = uploadFile.SetCellFloat(importSheet, "M"+importRowNumber, 0, -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}

				err = uploadFile.SetCellFloat(importSheet, "N"+importRowNumber, 0, -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			}

			err = uploadFile.SetCellValue(importSheet, "O"+importRowNumber, order.GetChilledWeight())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "P"+importRowNumber, "Средние температуры (+2+6)")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			if order.GetPaymentMethod() == "расч. счет" {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Да")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			} else {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Нет")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			}

			err = uploadFile.SetCellValue(importSheet, "R"+importRowNumber, order.GetDeliveryRegion())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "S"+importRowNumber, order.GetChilledBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			importRowNumber, err = IncrementStringCounter(importRowNumber)
			if err != nil {
				log.Printf("Failed to increment summary row number: %v", err)
			}

			err = uploadFile.SetCellValue(importSheet, "J"+importRowNumber, "Замороженная продукция")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = SetIntValueofString(uploadFile, importSheet, "K"+importRowNumber, order.GetRefGoNumber())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "L"+importRowNumber, order.GetFrozenBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellFloat(importSheet, "M"+importRowNumber, 0, -1, 64)
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellFloat(importSheet, "N"+importRowNumber, 0, -1, 64)
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "O"+importRowNumber, order.GetFrozenWeight())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "P"+importRowNumber, "Низкие температуры (-18)")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			if order.GetPaymentMethod() == "расч. счет" {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Да")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			} else {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Нет")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			}

			err = uploadFile.SetCellValue(importSheet, "R"+importRowNumber, order.GetDeliveryRegion())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "S"+importRowNumber, order.GetFrozenBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			importRowNumber, err = IncrementStringCounter(importRowNumber)
			if err != nil {
				log.Printf("Failed to increment summary row number: %v", err)
			}
		case chilledBoxes > 0 && frozenBoxes == 0:
			err = uploadFile.SetCellValue(importSheet, "J"+importRowNumber, "Охлаждённая продукция")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = SetIntValueofString(uploadFile, importSheet, "K"+importRowNumber, order.GetRefGoNumber())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "L"+importRowNumber, order.GetChilledBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			if order.GetPaymentMethod() == "Наличные" || order.GetPaymentMethod() == "Терминал" {
				err = uploadFile.SetCellFloat(importSheet, "M"+importRowNumber, order.GetSum(), -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}

				err = uploadFile.SetCellFloat(importSheet, "N"+importRowNumber, order.GetSum(), -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			} else {
				err = uploadFile.SetCellFloat(importSheet, "M"+importRowNumber, 0, -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}

				err = uploadFile.SetCellFloat(importSheet, "N"+importRowNumber, 0, -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			}

			err = uploadFile.SetCellValue(importSheet, "O"+importRowNumber, order.GetChilledWeight()+order.GetFrozenWeight())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "P"+importRowNumber, "Средние температуры (+2+6)")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			if order.GetPaymentMethod() == "расч. счет" {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Да")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			} else {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Нет")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			}

			err = uploadFile.SetCellValue(importSheet, "R"+importRowNumber, order.GetDeliveryRegion())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "S"+importRowNumber, order.GetChilledBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			importRowNumber, err = IncrementStringCounter(importRowNumber)
			if err != nil {
				log.Printf("Failed to increment summary row number: %v", err)
			}
		case chilledBoxes == 0 && frozenBoxes > 0:
			err = uploadFile.SetCellValue(importSheet, "J"+importRowNumber, "Замороженная продукция")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = SetIntValueofString(uploadFile, importSheet, "K"+importRowNumber, order.GetRefGoNumber())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "L"+importRowNumber, order.GetFrozenBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			if order.GetPaymentMethod() == "Наличные" || order.GetPaymentMethod() == "Терминал" {
				err = uploadFile.SetCellFloat(importSheet, "M"+importRowNumber, order.GetSum(), -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}

				err = uploadFile.SetCellFloat(importSheet, "N"+importRowNumber, order.GetSum(), -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			} else {
				err = uploadFile.SetCellFloat(importSheet, "M"+importRowNumber, 0, -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}

				err = uploadFile.SetCellFloat(importSheet, "N"+importRowNumber, 0, -1, 64)
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			}

			err = uploadFile.SetCellValue(importSheet, "O"+importRowNumber, order.GetChilledWeight()+order.GetFrozenWeight())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "P"+importRowNumber, "Низкие температуры (-18)")
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			if order.GetPaymentMethod() == "расч. счет" {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Да")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			} else {
				err = uploadFile.SetCellValue(importSheet, "Q"+importRowNumber, "Нет")
				if err != nil {
					log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
				}
			}

			err = uploadFile.SetCellValue(importSheet, "R"+importRowNumber, order.GetDeliveryRegion())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			err = uploadFile.SetCellValue(importSheet, "S"+importRowNumber, order.GetFrozenBoxes())
			if err != nil {
				log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
			}

			importRowNumber, err = IncrementStringCounter(importRowNumber)
			if err != nil {
				log.Printf("Failed to increment import row number: %v", err)
			}
		default:
			log.Printf("Order %s has no boxes, skipping import", order.GetRefGoNumber())
		}

		err = SetIntValueofString(uploadFile, summarySheet, "L"+summaryRowNumber, order.GetRefGoNumber())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(summarySheet, "M"+summaryRowNumber, order.GetFrozenBoxes()+order.GetChilledBoxes())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		err = uploadFile.SetCellValue(summarySheet, "N"+summaryRowNumber, order.GetSum())
		if err != nil {
			log.Printf("Failed to set cell value for order %s: %v", order.GetRefGoNumber(), err)
		}

		summaryRowNumber, err = IncrementStringCounter(summaryRowNumber)
		if err != nil {
			log.Printf("Failed to increment summary row number: %v", err)
		}

		overallBoxes += order.GetChilledBoxes() + order.GetFrozenBoxes()
	}

	err = uploadFile.SetCellValue(summarySheet, "C15", len(orders))
	if err != nil {
		log.Printf("Failed to set cell value for total orders: %v", err)
	}

	err = uploadFile.SetCellValue(summarySheet, "F15", overallBoxes)
	if err != nil {
		log.Printf("Failed to set cell value for overall boxes: %v", err)
	}
	// На данный момент сохраняем в корень, позже переделаем в отдельную папку
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
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fillSingularBarcode(f *excelize.File, o *domain.InternalOrder, boxState string, boxnumber int, totalboxes uint64, rowNumber int, header int, regular int, toTheRight int) error {
	innerCounter := rowNumber
	err := f.SetRowHeight("Sheet1", innerCounter, 62)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), o.GetRefGoNumber())
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.MergeCell("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter))
	if err != nil {
		log.Printf("failed to merge cells")
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), header)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	pngBytes, err := generateBarcodePNG(o.GetRefGoNumber(), 140, 55)
	if err != nil {
		log.Printf("Ошибка генерации: %v\n", err)
	}

	err = f.AddPictureFromBytes("Sheet1",
		fmt.Sprintf("A%d", innerCounter),
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
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}
	innerCounter++

	err = f.SetRowHeight("Sheet1", innerCounter, 20)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	if boxState == "Охл" {
		err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), "Среднетемпературный режим (+2+6)")
		if err != nil {
			log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
		}
	} else {
		err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), "Низкотемпературный (-18)")
		if err != nil {
			log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
		}
	}

	err = f.MergeCell("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter))
	if err != nil {
		log.Printf("failed to merge cells")
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), header)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetRowHeight("Sheet1", innerCounter, 12)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), "Наименование:")
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	if boxState == "Охл" {
		err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", innerCounter), "Охлаждённая продукция")
		if err != nil {
			log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
		}
	} else {
		err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", innerCounter), "Замороженная продукция")
		if err != nil {
			log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
		}
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), regular)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetRowHeight("Sheet1", innerCounter, 12)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), "Заказчик:")
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", innerCounter), "STEAK HOME")
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), regular)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetRowHeight("Sheet1", innerCounter, 12)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), "Получатель:")
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", innerCounter), o.GetRecieverName())
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), regular)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetRowHeight("Sheet1", innerCounter, 12)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), "Вх. накладная:")
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", innerCounter), o.GetRefGoNumber())
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("A%d", innerCounter), regular)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("B%d", innerCounter), fmt.Sprintf("B%d", innerCounter), toTheRight)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetRowHeight("Sheet1", innerCounter, -1)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", innerCounter), "Адрес доставки:")
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", innerCounter), o.GetShipmentAddress())
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("A%d", innerCounter), fmt.Sprintf("B%d", innerCounter), regular)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	innerCounter++

	err = f.SetRowHeight("Sheet1", innerCounter, 12)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", innerCounter), fmt.Sprintf("(%d из %d)", boxnumber, totalboxes))
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	err = f.SetCellStyle("Sheet1", fmt.Sprintf("B%d", innerCounter), fmt.Sprintf("B%d", innerCounter), toTheRight)
	if err != nil {
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	return nil
}

func (e *ExcelExporter) ExportOrdersBarcodesToExcel(orders []*domain.InternalOrder) (savepath string, err error) {
	f := excelize.NewFile()
	regularCellWrapStyle, err := f.NewStyle(&excelize.Style{
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
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	regularToTheRightCellWrapStyle, err := f.NewStyle(&excelize.Style{
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
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	headerCellWrapStyle, err := f.NewStyle(&excelize.Style{
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
		log.Printf("%s occured in ExportOrdersBarcodesToExcel", err)
	}

	f.SetColWidth("Sheet1", "A", "A", 14)
	f.SetColWidth("Sheet1", "B", "B", 36.7)

	counter := 1

	for _, o := range orders {
		totalboxes := o.GetChilledBoxes() + o.GetFrozenBoxes()
		totalcount := 1
		if o.GetChilledBoxes() > 0 {
			for i := 1; i <= int(o.GetChilledBoxes()); i++ {
				fillSingularBarcode(f, o, "Охл", totalcount, totalboxes, counter, headerCellWrapStyle, regularCellWrapStyle, regularToTheRightCellWrapStyle)
				totalcount++
				counter += 8
			}
		}
		if o.GetFrozenBoxes() > 0 {
			for i := 1; i <= int(o.GetChilledBoxes()); i++ {
				fillSingularBarcode(f, o, "Зам", totalcount, totalboxes, counter, headerCellWrapStyle, regularCellWrapStyle, regularToTheRightCellWrapStyle)
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
		if err := f.InsertPageBreak("Sheet1", cell); err != nil {
			panic(err)
		}
	}

	temptoday := time.Now()
	today := temptoday.Format("02.01.2006")
	savepath = "../" + today + ".xlsx"
	if err = f.SaveAs(savepath); err != nil {
		fmt.Println("Ошибка сохранения:", err)
		return
	}

	return savepath, nil
}

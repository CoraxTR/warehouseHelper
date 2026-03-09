package excel

import (
	"log"
	"strconv"
	"time"
	"warehouseHelper/internal/domain"

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

	err = uploadFile.SaveAs(savepath)
	if err != nil {
		log.Printf("Failed to save Excel file: %v", err)
	}

	return today + ".xlsx", nil
}

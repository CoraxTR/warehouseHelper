package msapiclient

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"
	"warehouseHelper/internal/domain"
)

const (
	gramsInKG      = 1000
	copecksInRuble = 100
)

type MoySkladConverter struct{}

type BoxWeightInfo struct {
	chilledBoxes  uint64
	frozenBoxes   uint64
	chilledWeight float64
	frozenWeight  float64
}

func processPhoneNumber(phonestr string) (uint64, error) {
	var digits strings.Builder

	for _, ch := range phonestr {
		if ch >= '0' && ch <= '9' {
			digits.WriteRune(ch)
		}
	}

	digitStr := digits.String()

	if len(digitStr) < 10 {
		return 0, fmt.Errorf("недостаточно цифр в номере (найдено %d): %s", len(digitStr), phonestr)
	}

	last10 := digitStr[len(digitStr)-10:]

	resultStr := "8" + last10

	result, err := strconv.ParseUint(resultStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ошибка преобразования в число: %w", err)
	}

	return result, nil
}

func processDeliveryDate(date string) string {
	datelayout := "02.01.2006"

	tempdate, err := time.Parse("2006-01-02 15:04:05.000", date)
	if err != nil {
		log.Printf("Couldn't parse date: %s", date)
	}

	deliverydate := tempdate.Format(datelayout)

	return deliverydate
}

func processDeliveryInterval(msOrder *MSOrder) (from, until string) {
	tempinterval, ok := msOrder.AttributesMap["Интервал доставки"].(string)
	if !ok {
		return "", ""
	}

	parts := strings.Split(tempinterval, "-")
	from = parts[0]
	until = parts[1]

	return from, until
}

func processDeliveryRegion(msOrder *MSOrder) string {
	region, ok := msOrder.AttributesMap["Регион доставки"].(string)
	if !ok {
		region = "МСК"
	}

	return region
}

func processPaymentMethod(msOrder *MSOrder) string {
	paymentMethod, ok := msOrder.AttributesMap["Способ оплаты"].(string)
	if !ok {
		return ""
	}

	return paymentMethod
}

func processWeights(msOrder *MSOrder) (chilledWeight, frozenWeight, anyWeight float64) {
	for _, position := range msOrder.PositionsWInfo {
		if position.PositionCode == "" {
			anyWeight += (position.Quantity * position.PositionWeight * gramsInKG)
			log.Printf("Позиция %s пропущена из-за отсутствия кода", position.Meta.HREF)
			continue
		}

		runedCode := []rune(position.PositionCode)
		switch runedCode[1] {
		case '0':
			frozenWeight += (position.Quantity * position.PositionWeight * gramsInKG)
		case '1':
			chilledWeight += (position.Quantity * position.PositionWeight * gramsInKG)
		case '2':
			anyWeight += (position.Quantity * position.PositionWeight * gramsInKG)
		default:
			continue
		}
	}

	return chilledWeight, frozenWeight, anyWeight
}

func processChilledBoxesCount(msOrder *MSOrder) uint64 {
	chilledBoxes, ok := msOrder.AttributesMap["Кол-во коробок охл."]
	if !ok {
		return 0
	}

	chilledBoxesStr, ok := chilledBoxes.(string)
	if !ok {
		log.Printf("ошибка преобразования количества коробок охл.: значение не строка: %v", chilledBoxes)

		return 0
	}

	chilledBoxesCount, err := strconv.Atoi(chilledBoxesStr)
	if err != nil {
		log.Printf("ошибка преобразования количества коробок охл.: %v", err)

		return 0
	}

	if chilledBoxesCount < 0 {
		log.Printf("ошибка преобразования количества коробок охл.: отрицательное значение: %d", chilledBoxesCount)

		return 0
	}

	chilledBoxesCountUint := uint64(chilledBoxesCount)

	return chilledBoxesCountUint
}

func processFrozenBoxesCount(msOrder *MSOrder) uint64 {
	frozenBoxes, ok := msOrder.AttributesMap["Кол-во коробок зам."]
	if !ok {
		return 0
	}

	frozenBoxesStr, ok := frozenBoxes.(string)
	if !ok {
		log.Printf("ошибка преобразования количества коробок зам.: значение не строка: %v", frozenBoxes)

		return 0
	}

	frozenBoxesCount, err := strconv.Atoi(frozenBoxesStr)
	if err != nil {
		log.Printf("ошибка преобразования количества коробок зам.: %v", err)

		return 0
	}

	if frozenBoxesCount < 0 {
		log.Printf("ошибка преобразования количества коробок зам.: отрицательное значение: %d", frozenBoxesCount)

		return 0
	}

	frozenBoxesCountUint := uint64(frozenBoxesCount)

	return frozenBoxesCountUint
}

func weightRoundUp(weight float64) float64 {
	rounded := math.Ceil(weight/500) * 500

	roundedKG := rounded / gramsInKG
	if roundedKG < 0.5 {
		return 0.5
	} else {
		return roundedKG
	}
}

func processBoxesAndWeights(msOrder *MSOrder) BoxWeightInfo {
	cB := processChilledBoxesCount(msOrder)
	fB := processFrozenBoxesCount(msOrder)
	cW, fW, aW := processWeights(msOrder)

	switch {
	case cB != 0 && fB == 0:
		return BoxWeightInfo{
			chilledBoxes:  cB,
			chilledWeight: weightRoundUp(cW + fW + aW),
		}
	case cB == 0 && fB != 0:
		return BoxWeightInfo{
			frozenBoxes:  fB,
			frozenWeight: weightRoundUp(cW + fW + aW),
		}
	default:
		return BoxWeightInfo{
			chilledBoxes:  cB,
			frozenBoxes:   fB,
			chilledWeight: weightRoundUp(cW + aW),
			frozenWeight:  weightRoundUp(fW),
		}
	}
}

func (c *MoySkladConverter) ToDomain(msOrder *MSOrder) *domain.InternalOrder {
	o := new(domain.InternalOrder)
	o.SetHREF(msOrder.Meta.HREF)
	o.SetName(msOrder.Name)

	if name, ok := msOrder.AttributesMap["Имя получателя"]; ok {
		if tempname, sure := name.(string); sure {
			o.SetRecieverName(tempname)
		} else {
			log.Println("TODO")
		}
	} else {
		o.SetRecieverName(msOrder.AgentName)
	}

	if phone, ok := msOrder.AttributesMap["Телефон получателя"]; ok {
		if assertedphone, sure := phone.(string); sure {
			tempPhone, err := processPhoneNumber(assertedphone)
			if err != nil {
				log.Println(err)
			}

			o.SetRecieverPhoneNumber(tempPhone)
		} else {
			log.Println("TODO")
		}
	} else {
		tempPhone, err := processPhoneNumber(msOrder.AgentPhone)
		if err != nil {
			log.Println(err)
		}

		o.SetRecieverPhoneNumber(tempPhone)
	}

	o.SetDescription(msOrder.Description)
	o.SetDeliveryPlannedDate(processDeliveryDate(msOrder.DeliveryPlannedMoment))
	o.SetShipmentAddress(msOrder.ShipmentAddress)
	from, until := processDeliveryInterval(msOrder)
	o.SetDeliveryIntervalFrom(from)
	o.SetDeliveryIntervalUntil(until)
	o.SetDeliveryRegion(processDeliveryRegion(msOrder))
	o.SetPaymentMethod(processPaymentMethod(msOrder))
	if refGoNumber, ok := msOrder.AttributesMap["Номер в РЕФ"].(string); ok {
		o.SetRefGoNumber(refGoNumber)
	}
	o.SetSum(msOrder.Sum / copecksInRuble)
	boxWeightInfo := processBoxesAndWeights(msOrder)
	o.SetChilledBoxes(boxWeightInfo.chilledBoxes)
	o.SetFrozenBoxes(boxWeightInfo.frozenBoxes)
	o.SetChilledWeight(boxWeightInfo.chilledWeight)
	o.SetFrozenWeight(boxWeightInfo.frozenWeight)

	return o
}

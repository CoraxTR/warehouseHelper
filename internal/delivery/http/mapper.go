package http

import (
	"warehouseHelper/internal/domain"
)

func ToDomainOrder(dto *UpdateOrderRequest) *domain.InternalOrder {
	order := &domain.InternalOrder{}

	order.SetHREF(dto.HREF)
	order.SetName(dto.Name)
	order.SetRecieverName(dto.ReceiverName)
	order.SetRecieverPhoneNumber(dto.ReceiverPhoneNumber)
	order.SetDescription(dto.Description)
	order.SetDeliveryPlannedDate(dto.DeliveryPlannedDate)
	order.SetShipmentAddress(dto.ShipmentAddress)
	order.SetDeliveryIntervalFrom(dto.DeliveryIntervalFrom)
	order.SetDeliveryIntervalUntil(dto.DeliveryIntervalUntil)
	order.SetDeliveryRegion(dto.DeliveryRegion)
	order.SetPaymentMethod(dto.PaymentMethod)
	order.SetRefGoNumber(dto.RefGoNumber)
	order.SetSum(dto.Sum)
	order.SetChilledWeight(dto.ChilledWeight)
	order.SetFrozenWeight(dto.FrozenWeight)
	order.SetFrozenBoxes(dto.FrozenBoxes)
	order.SetChilledBoxes(dto.ChilledBoxes)

	return order
}

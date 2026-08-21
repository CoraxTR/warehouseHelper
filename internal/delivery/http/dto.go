package http

type UpdateOrderRequest struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	ReceiverName          string  `json:"receiverName"`
	ReceiverPhoneNumber   uint64  `json:"receiverPhoneNumber"`
	Description           string  `json:"description"`
	DeliveryPlannedDate   string  `json:"deliveryPlannedDate"`
	ShipmentAddress       string  `json:"shipmentAddress"`
	DeliveryIntervalFrom  string  `json:"deliveryIntervalFrom"`
	DeliveryIntervalUntil string  `json:"deliveryIntervalUntil"`
	DeliveryRegion        string  `json:"deliveryRegion"`
	PaymentMethod         string  `json:"paymentMethod"`
	RefGoNumber           string  `json:"refGoNumber"`
	RefGoZone             string  `json:"refGoZone"`
	Sum                   float64 `json:"sum"`
	ChilledWeight         float64 `json:"chilledWeight"`
	FrozenWeight          float64 `json:"frozenWeight"`
	FrozenBoxes           uint64  `json:"frozenBoxes"`
	ChilledBoxes          uint64  `json:"chilledBoxes"`
}

type UpdateFromMSRequest struct {
	ID string `json:"id"`
}

type PrintMultipleRequest struct {
	IDs []string `json:"ids"`
}

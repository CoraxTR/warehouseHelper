package domain

type InternalOrder struct {
	href                  string
	name                  string
	receiverName          string
	receiverPhoneNumber   uint64
	description           string
	deliveryPlannedDate   string
	shipmentAddress       string
	deliveryIntervalFrom  string
	deliveryIntervalUntil string
	deliveryRegion        string
	paymentMethod         string
	refGoNumber           string
	sum                   float64
	chilledWeight         float64
	frozenWeight          float64
	frozenBoxes           uint64
	chilledBoxes          uint64
	errors                map[string]string
}

func (o *InternalOrder) SetHREF(s string) {
	o.href = s
}

func (o *InternalOrder) SetName(s string) {
	o.name = s
}

func (o *InternalOrder) SetRecieverName(s string) {
	o.receiverName = s
}

func (o *InternalOrder) SetRecieverPhoneNumber(u uint64) {
	o.receiverPhoneNumber = u
}

func (o *InternalOrder) SetDescription(s string) {
	o.description = s
}

func (o *InternalOrder) SetDeliveryPlannedDate(s string) {
	o.deliveryPlannedDate = s
}

func (o *InternalOrder) SetShipmentAddress(s string) {
	o.shipmentAddress = s
}

func (o *InternalOrder) SetDeliveryIntervalFrom(s string) {
	o.deliveryIntervalFrom = s
}

func (o *InternalOrder) SetDeliveryIntervalUntil(s string) {
	o.deliveryIntervalUntil = s
}

func (o *InternalOrder) SetDeliveryRegion(s string) {
	o.deliveryRegion = s
}

func (o *InternalOrder) SetPaymentMethod(s string) {
	o.paymentMethod = s
}

func (o *InternalOrder) SetRefGoNumber(s string) {
	o.refGoNumber = s
}

func (o *InternalOrder) SetSum(f float64) {
	o.sum = f
}

func (o *InternalOrder) SetChilledWeight(f float64) {
	o.chilledWeight = f
}

func (o *InternalOrder) SetFrozenWeight(f float64) {
	o.frozenWeight = f
}

func (o *InternalOrder) SetFrozenBoxes(f uint64) {
	o.frozenBoxes = f
}

func (o *InternalOrder) SetChilledBoxes(f uint64) {
	o.chilledBoxes = f
}

func (o *InternalOrder) SetErrors(errs map[string]string) {
	o.errors = errs
}

func (o *InternalOrder) GetHREF() string {
	return o.href
}

func (o *InternalOrder) GetName() string {
	return o.name
}

func (o *InternalOrder) GetRecieverName() string {
	return o.receiverName
}

func (o *InternalOrder) GetRecieverPhoneNumber() uint64 {
	return o.receiverPhoneNumber
}

func (o *InternalOrder) GetDescription() string {
	return o.description
}

func (o *InternalOrder) GetDeliveryPlannedDate() string {
	return o.deliveryPlannedDate
}

func (o *InternalOrder) GetShipmentAddress() string {
	return o.shipmentAddress
}

func (o *InternalOrder) GetDeliveryIntervalFrom() string {
	return o.deliveryIntervalFrom
}

func (o *InternalOrder) GetDeliveryIntervalUntil() string {
	return o.deliveryIntervalUntil
}

func (o *InternalOrder) GetDeliveryRegion() string {
	return o.deliveryRegion
}

func (o *InternalOrder) GetPaymentMethod() string {
	return o.paymentMethod
}

func (o *InternalOrder) GetRefGoNumber() string {
	return o.refGoNumber
}

func (o *InternalOrder) GetSum() float64 {
	return o.sum
}

func (o *InternalOrder) GetChilledWeight() float64 {
	return o.chilledWeight
}

func (o *InternalOrder) GetFrozenWeight() float64 {
	return o.frozenWeight
}

func (o *InternalOrder) GetFrozenBoxes() uint64 {
	return o.frozenBoxes
}

func (o *InternalOrder) GetChilledBoxes() uint64 {
	return o.chilledBoxes
}

func (o *InternalOrder) GetErrors() map[string]string {
	return o.errors
}

func (o *InternalOrder) Validate() {
	o.errors = make(map[string]string)
	if o.receiverName == "" {
		o.errors["recieverName"] = "Не указано имя получателя"
	}

	if o.receiverPhoneNumber < 1000000000 {
		o.errors["recieverPhoneNumber"] = "Не указан телефон получателя"
	}

	if o.shipmentAddress == "" {
		o.errors["shipmentAdress"] = "Не указан адрес доставки"
	}

	if o.deliveryPlannedDate == "" {
		o.errors["deliveryPlannedDate"] = "Не указана дата доставки"
	}

	if o.deliveryIntervalFrom == "" {
		o.errors["deliveryIntervalFrom"] = "Не указан интервал доставки"
	}

	if o.deliveryIntervalUntil == "" {
		o.errors["deliveryIntervalUntil"] = "Не указан интервал доставки"
	}

	if o.chilledBoxes != 0 && o.chilledWeight == 0 {
		o.errors["chilledWeight"] = "Нулевой вес охлаждённой продукции"
	}

	if o.frozenBoxes != 0 && o.frozenWeight == 0 {
		o.errors["frozenWeight"] = "Нулевой вес замороженной продукции"
	}

	if o.frozenBoxes == 0 && o.chilledBoxes == 0 {
		o.errors["frozenBoxes"] = "В заказе нет коробок"
		o.errors["chilledBoxes"] = "В заказе нет коробок"
	}
}

type OrdersUsefulInfo struct {
	TotalOrders     int
	MoscowPayByCard []string
	MoscowComments  map[string]string
	SPBOrders       []string
	SPBOrdersByCard []string
	SPBComments     map[string]string
}

func CollectOrdersInfo(orders []*InternalOrder) *OrdersUsefulInfo {
	totalOrders := len(orders)
	moscowPayByCard := make([]string, 0)
	comments := make(map[string]string)
	spbOrders := make([]string, 0)
	spbOrdersByCard := make([]string, 0)
	spbComments := make(map[string]string)

	for _, order := range orders {
		switch order.GetDeliveryRegion() {
		case "МСК":
			if order.GetPaymentMethod() == "Терминал" {
				moscowPayByCard = append(moscowPayByCard, order.GetRefGoNumber())
			}

			if order.GetDescription() != "" {
				comments[order.GetRefGoNumber()] = order.GetDescription()
			}
		case "СПБ":
			spbOrders = append(spbOrders, order.GetRefGoNumber())
			if order.GetPaymentMethod() == "Терминал" {
				spbOrdersByCard = append(spbOrdersByCard, order.GetRefGoNumber())
			}

			if order.GetDescription() != "" {
				spbComments[order.GetRefGoNumber()] = order.GetDescription()
			}
		default:
			continue
		}
	}

	return &OrdersUsefulInfo{
		TotalOrders:     totalOrders,
		MoscowPayByCard: moscowPayByCard,
		MoscowComments:  comments,
		SPBOrders:       spbOrders,
		SPBOrdersByCard: spbOrdersByCard,
		SPBComments:     spbComments,
	}
}

type InternalPosition struct {
	state       string
	quantity    float64
	weight      float64
	totalweight float64
}

func (p *InternalPosition) SetState(s string) {
	p.state = s
}

func (p *InternalPosition) SetQuantity(f float64) {
	p.quantity = f
}

func (p *InternalPosition) SetWeight(f float64) {
	p.weight = f
}

func (p *InternalPosition) SetTotalWeight(f float64) {
	p.totalweight = f
}

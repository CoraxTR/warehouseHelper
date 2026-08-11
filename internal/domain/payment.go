package domain

// Способы оплаты заказа — значения атрибута «Способ оплаты» в МойСклад.
const (
	PaymentMethodCash = "Наличные"
	PaymentMethodCard = "Терминал"
	PaymentMethodWire = "расч. счет"
)

// IsShippablePayment — отгружается ли заказ с данным способом оплаты
// (наличные и терминал; «расч. счет» не отгружается).
func IsShippablePayment(paymentMethod string) bool {
	return paymentMethod == PaymentMethodCash || paymentMethod == PaymentMethodCard
}

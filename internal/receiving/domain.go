// Пакет receiving — модуль приёмки.
//
// Владеет таблицами product_received_weights (веса принятых единиц, граммы)
// и product_supplier_barcodes (связка «внешний код поставщика → товар»,
// нужна только приёмке для резолва внешних штрих-кодов). Клиент модуля
// сроков (AcceptStock) — шов пишется вместе с этим модулем.
package receiving

// BarcodeRef — связка «внешний код поставщика → товар каталога».
type BarcodeRef struct {
	ExternalCode string // базовая часть штрих-кода, вычитываемая decode_rule
	ProductID    string // товар каталога (products.id)
	ProductName  string // имя товара (для отображения)
}

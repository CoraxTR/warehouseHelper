// Пакет receiving — модуль приёмки.
//
// Владеет таблицами product_received_weights (веса принятых единиц, граммы)
// и product_supplier_barcodes (связка «внешний код поставщика → товар»,
// нужна только приёмке для резолва внешних штрих-кодов). Клиент модуля
// сроков (AcceptStock) — шов пишется вместе с этим модулем.
package receiving

import (
	"time"
)

// BarcodeRef — связка «внешний код поставщика → товар каталога».
type BarcodeRef struct {
	ExternalCode string // базовая часть штрих-кода, вычитываемая decode_rule
	ProductID    string // товар каталога (products.id)
	ProductName  string // имя товара (для отображения)
	InternalCode string // внутренний код товара (products.internal_code)
	Weighted     bool   // весовой товар (uom: кг/г/т)
}

// ProductRef — товар каталога для кеша приёмки (по internal_code).
type ProductRef struct {
	ProductID    string
	InternalCode string
	Name         string
	Weighted     bool
}

// Cache — кеш приёмки поставщика: правила вычитки, маппинг внешних кодов,
// товары каталога по внутренним кодам, позиции поставщика для ручного выбора.
type Cache struct {
	ItemRules  []DecodeRule          // правила единичных товаров
	BoxRules   []DecodeRule          // правила коробок
	ByExternal map[string]BarcodeRef // external_code → товар
	ByCode     map[string]ProductRef // internal_code → товар (внутренние штрих-коды)
	Products   []ProductRef          // позиции поставщика (уникальные товары из связок)
}

// DecodeRule — распарсенное правило вычитки поставщика (decoderules.Rule
// не экспортируем наружу: пакет decoderules — деталь реализации).
type DecodeRule struct {
	Length int
	Fields []RuleField
}

// RuleField — поле правила: позиция (1-based) и длина; Pos == 0 — поле не задано.
type RuleField struct {
	Pos int
	Len int
}

// ScanKind — тип распознанного скана.
type ScanKind string

const (
	KindItem ScanKind = "item" // единичный товар (кусок)
	KindBox  ScanKind = "box"  // коробка
)

// DecodedScan — результат резолва штрих-кода.
type DecodedScan struct {
	Kind            ScanKind
	Raw             string
	IsInternal      bool // внутренний формат 29/33
	ProductID       string
	InternalCode    string
	ProductName     string
	WeightG         *int64 // граммы; nil — не резолвнуто (для весового — обязательно)
	Qty             int64  // кусок = 1, коробка = кол-во вложений (из кода; 0 — не резолвнуто)
	ProducedOn      *time.Time
	BestBefore      *time.Time
	DeclaredQty     *int64 // коробка: заявленное кол-во вложений из кода
	DeclaredWeightG *int64 // коробка: заявленный общий вес из кода
	// Фактические значения коробки (Σ детей) — заполняет Save:
	ActualQty     int64
	ActualWeightG int64
	Mismatch      bool // факт != заявленное (не блокирует, подсвечивается)
}

// ScanEntry — вход Save: сырой штрих-код + ручные дополнения.
type ScanEntry struct {
	Raw              string
	ManualProductID  string      // ручной выбор товара (код не резолвнулся)
	ManualWeightG    *int64      // ручной вес (не вычитывается из кода)
	ManualProducedOn *time.Time  // ручная выработка
	ManualBestBefore *time.Time  // ручной срок
	Children         []ScanEntry // сканы внутри коробки
}

// SaveRequest — сохранить приёмку поставщика.
type SaveRequest struct {
	SupplierID string
	Scans      []ScanEntry
}

// SaveResult — результат приёмки: отчёт, куски и коробки (для будущей печати).
type SaveResult struct {
	Rows  []ReportRow // отчёт: товар — срок — принято (кг/шт)
	Units []Unit      // индивидуальные куски
	Boxes []Box       // собранные коробки
}

// ReportRow — строка отчёта приёмки.
type ReportRow struct {
	ProductName string
	BestBefore  string // YYYY-MM-DD
	Weighted    bool   // весовой: QtyKg в кг; иначе Qty штук
	QtyKg       float64
	Qty         int64
}

// Unit — принятый индивидуальный кусок.
type Unit struct {
	ProductID    string
	InternalCode string
	ProductName  string
	WeightG      int64
	ProducedOn   *time.Time
	BestBefore   time.Time
	InBox        bool // лежал в коробке (в подсписке)
	BoxMismatch  bool // коробка-родитель с расхождением
}

// Box — принятая коробка (для печати): факт по кускам.
type Box struct {
	ProductID       string
	InternalCode    string
	ProductName     string
	WeightG         int64 // Σ весов кусков
	Qty             int64
	ProducedOn      *time.Time
	BestBefore      *time.Time // nil — срок не вычитывается из кода коробки
	DeclaredQty     *int64     // заявленное из кода (nil — не резолвнуто)
	DeclaredWeightG *int64
	Mismatch        bool
}

// WeightRow — вес принятой единицы (граммы) для product_received_weights.
type WeightRow struct {
	ProductID string
	WeightG   int64
}

// Ошибки домена.
var (
	ErrScanUnknown = errScan("штрих-код не распознан: нет правила поставщика и это не внутренний формат")
)

type errScan string

func (e errScan) Error() string { return string(e) }

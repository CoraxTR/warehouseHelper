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
	ExternalCode string `json:"-"` // ключ карты ByExternal, внутри не нужен
	ProductID    string `json:"product_id"`
	ProductName  string `json:"name"`
	InternalCode string `json:"internal_code"`
	Weighted     bool   `json:"weighted"`
}

// ProductRef — товар каталога для кеша приёмки (по internal_code).
type ProductRef struct {
	ProductID    string `json:"product_id"`
	InternalCode string `json:"internal_code"`
	Name         string `json:"name"`
	Weighted     bool   `json:"weighted"`
}

// Cache — кеш приёмки поставщика: правила вычитки, маппинг внешних кодов,
// товары каталога по внутренним кодам, позиции поставщика для ручного выбора.
// Сериализуется целиком на страницу приёмки — JS резолвит сканы локально,
// сервер перевалидирует при сохранении.
type Cache struct {
	ItemRules  []DecodeRule          `json:"item_rules"`
	BoxRules   []DecodeRule          `json:"box_rules"`
	ByExternal map[string]BarcodeRef `json:"by_external"`
	ByCode     map[string]ProductRef `json:"by_code"`
	Products   []ProductRef          `json:"products"`
}

// DecodeRule — распарсенное правило вычитки поставщика (decoderules.Rule
// не экспортируем наружу: пакет decoderules — деталь реализации).
type DecodeRule struct {
	Length int        `json:"length"`
	Fields []RuleField `json:"fields"`
}

// RuleField — поле правила: позиция (1-based) и длина; Pos == 0 — поле не задано.
type RuleField struct {
	Pos int `json:"pos"`
	Len int `json:"len"`
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
	Raw              string      `json:"raw"`
	ManualProductID  string      `json:"manual_product_id"`  // ручной выбор товара (код не резолвнулся)
	ManualWeightG    *int64      `json:"manual_weight_g"`    // ручной вес (не вычитывается из кода)
	ManualProducedOn *time.Time  `json:"manual_produced_on"` // ручная выработка
	ManualBestBefore *time.Time  `json:"manual_best_before"` // ручной срок
	Children         []ScanEntry `json:"children"`           // сканы внутри коробки
}

// SaveRequest — сохранить приёмку поставщика.
type SaveRequest struct {
	SupplierID string      `json:"supplier_id"`
	Scans      []ScanEntry `json:"scans"`
}

// SaveResult — результат приёмки: отчёт, куски и коробки (для будущей печати).
type SaveResult struct {
	Rows  []ReportRow `json:"rows"`  // отчёт: товар — срок — принято (кг/шт)
	Units []Unit      `json:"units"` // индивидуальные куски
	Boxes []Box       `json:"boxes"` // собранные коробки
}

// ReportRow — строка отчёта приёмки.
type ReportRow struct {
	ProductName string  `json:"name"`
	BestBefore  string  `json:"best_before"` // YYYY-MM-DD
	Weighted    bool    `json:"weighted"`    // весовой: QtyKg в кг; иначе Qty штук
	QtyKg       float64 `json:"qty_kg"`
	Qty         int64   `json:"qty"`
}

// Unit — принятый индивидуальный кусок.
type Unit struct {
	ProductID    string     `json:"product_id"`
	InternalCode string     `json:"internal_code"`
	ProductName  string     `json:"name"`
	WeightG      int64      `json:"weight_g"`
	ProducedOn   *time.Time `json:"produced_on"`
	BestBefore   time.Time  `json:"best_before"`
	InBox        bool       `json:"in_box"`       // лежал в коробке (в подсписке)
	BoxMismatch  bool       `json:"box_mismatch"` // коробка-родитель с расхождением
}

// Box — принятая коробка (для печати): факт по кускам.
type Box struct {
	ProductID       string     `json:"product_id"`
	InternalCode    string     `json:"internal_code"`
	ProductName     string     `json:"name"`
	WeightG         int64      `json:"weight_g"` // Σ весов кусков
	Qty             int64      `json:"qty"`
	ProducedOn      *time.Time `json:"produced_on"`
	BestBefore      *time.Time `json:"best_before"` // nil — срок не вычитывается из кода коробки
	DeclaredQty     *int64     `json:"declared_qty"`
	DeclaredWeightG *int64     `json:"declared_weight_g"`
	Mismatch        bool       `json:"mismatch"`
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

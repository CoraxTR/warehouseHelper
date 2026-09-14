package usecase

import (
	"time"

	"warehouseHelper/internal/returns"
	"warehouseHelper/internal/scanmatch"
	"warehouseHelper/internal/stock"
)

// Обёртки страницы возврата над общим ядром сверки сканов
// (internal/scanmatch): правила перенесены туда, а здесь остаются прежние
// внутрипакетные имена — страница и её тесты работают с ними как раньше.

// scannedUnit — разобранный скан куска: какую строку отчёта он закрыл и что из
// этикетки идёт в остатки.
type scannedUnit struct {
	row     returns.Expected // строка отчёта, которую закрыл скан
	weightG int64            // вес из этикетки, г (у штучного — вес-заглушка)
	expDate time.Time        // срок годности из этикетки
}

// matchScans — сверка сканов с ожиданиями (scanmatch.Match): строку отчёта
// гасит свой скан — весовую закрывает ровно один скан с тем же весом (строго,
// без допуска: «бип и отказ»), штучную — ExpectedQty сканов. Любое
// несоответствие — ValidationError: событие остаётся открытым, выход — ручное
// закрытие.
func matchScans(scans []string, expected []returns.Expected) ([]scannedUnit, error) {
	units, err := scanmatch.Match(scans, expected)
	if err != nil {
		return nil, err
	}

	out := make([]scannedUnit, 0, len(units))
	for _, u := range units {
		out = append(out, scannedUnit{row: u.Row, weightG: u.WeightG, expDate: u.ExpDate})
	}
	return out, nil
}

// aggregateLots — группировка принятых сканов по (товар, срок): каждый скан —
// одна единица остатка (scanmatch.AggregateLots).
func aggregateLots(units []scannedUnit) []stock.LotIn {
	rows := make([]scanmatch.ScannedUnit, 0, len(units))
	for _, u := range units {
		rows = append(rows, scanmatch.ScannedUnit{Row: u.row, WeightG: u.weightG, ExpDate: u.expDate})
	}
	return scanmatch.AggregateLots(rows)
}

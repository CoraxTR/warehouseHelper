package scanmatch

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/stock"
)

// ValidationError — сканы не сошлись с ожиданиями: вызывающий код отдаёт это
// как отказ (HTTP 400 на страницах) и оставляет операцию незавершённой.
type ValidationError struct{ Reason string }

// Error возвращает текст отказа для оператора.
func (e *ValidationError) Error() string { return e.Reason }

// ErrBox — скан этикетки коробки (33 цифр): в сверку идут только куски.
var ErrBox = errors.New("коробка (33): возвращаются только куски")

// Scan — разобранный скан этикетки куска: код склада и вес, которыми скан
// участвует в сверке, и срок годности, который уходит в остатки.
type Scan struct {
	InternalCode string    // код склада (8 цифр: режим + группа + номер)
	WeightG      int64     // вес из этикетки, г
	ExpDate      time.Time // срок годности из этикетки
}

// ParseScan разбирает скан этикетки куска (29 цифр: 8-значный internal_code +
// вес + даты). Ошибка — не внутренний штрих-код или коробка (ErrBox):
// сверяются только куски.
func ParseScan(raw string) (Scan, error) {
	code, err := innercode.Parse(raw)
	if err != nil {
		return Scan{}, err
	}
	if code.Kind != innercode.KindItem {
		return Scan{}, ErrBox
	}
	return Scan{
		InternalCode: code.InternalCode,
		WeightG:      int64(code.WeightG),
		ExpDate:      code.ExpDate,
	}, nil
}

// ScannedUnit — разобранный скан куска: какую строку ожидания он закрыл и что
// из этикетки идёт в остатки.
type ScannedUnit struct {
	Row     Expected  // строка ожидания, которую закрыл скан
	WeightG int64     // вес из этикетки, г (у штучного — вес-заглушка)
	ExpDate time.Time // срок годности из этикетки
}

// Match — сверка сканов с ожиданиями ПОСТРОЧНО (решение владельца 10.09):
// строку гасит свой скан — весовую закрывает ровно один скан с тем же весом
// (строго, без допуска: «бип и отказ»), штучную — ExpectedQty сканов. Каждый
// скан — кусок 29 (коробки 33 не участвуют). Любое несоответствие —
// ValidationError: операция не выполняется, выход — ручное закрытие.
func Match(scans []string, expected []Expected) ([]ScannedUnit, error) {
	// Прогресс по строкам (индекс = порядок строки/Idx).
	progress := make([]int64, len(expected))

	units := make([]ScannedUnit, 0, len(scans))
	for _, raw := range scans {
		scan, err := ParseScan(raw)
		if err != nil {
			if errors.Is(err, ErrBox) {
				return nil, &ValidationError{Reason: fmt.Sprintf("штрих-код %q — %v", raw, ErrBox)}
			}
			return nil, &ValidationError{Reason: fmt.Sprintf("неверный штрих-код %q: %v", raw, err)}
		}

		idx := PickRow(expected, progress, scan.InternalCode, scan.WeightG)
		if idx < 0 {
			return nil, ScanReject(expected, scan.InternalCode, scan.WeightG)
		}

		units = append(units, ScannedUnit{Row: expected[idx], WeightG: scan.WeightG, ExpDate: scan.ExpDate})
		if expected[idx].Weighted {
			progress[idx] = expected[idx].ExpectedQty // весовой: строка гасится целиком
			continue
		}
		progress[idx]++ // штучный: этикетка = одна единица (вес-заглушка не участвует)
	}

	for i := range expected {
		if progress[i] != expected[i].ExpectedQty {
			return nil, &ValidationError{Reason: fmt.Sprintf(
				"вес не сходится: %s — отсканировано %s, ожидается %s",
				expected[i].Name, FormatQtyAmt(&expected[i], progress[i]), FormatQtyAmt(&expected[i], expected[i].ExpectedQty))}
		}
	}

	return units, nil
}

// PickRow — индекс свободной строки, которую гасит скан: тот же
// internal_code; весовой — строго тот же вес, и строка ещё не закрыта;
// штучный — первая незакрытая. -1 — подходящей строки нет.
func PickRow(expected []Expected, progress []int64, code string, weightG int64) int {
	for i := range expected {
		if expected[i].InternalCode != code {
			continue
		}
		if expected[i].Weighted {
			if weightG > 0 && expected[i].ExpectedQty == weightG && progress[i] == 0 {
				return i
			}
			continue
		}
		if progress[i] < expected[i].ExpectedQty {
			return i
		}
	}
	return -1
}

// ScanReject — расшифровка отказа скану, под который не нашлось строки: чужая
// позиция, перебор по штучной либо вес, которого нет ни в одной строке (в т.ч.
// «слитая» при подборе позиция). Выход один — ручное закрытие; склад получит
// уведомление о пересчёте сроков.
func ScanReject(expected []Expected, code string, weightG int64) *ValidationError {
	rows := make([]*Expected, 0, len(expected))
	for i := range expected {
		if expected[i].InternalCode == code {
			rows = append(rows, &expected[i])
		}
	}
	if len(rows) == 0 {
		return &ValidationError{Reason: fmt.Sprintf("товар с кодом %s не в списке возврата", code)}
	}
	if !rows[0].Weighted {
		return &ValidationError{Reason: fmt.Sprintf("перебор: все строки %s уже закрыты", rows[0].Name)}
	}

	weights := make([]string, 0, len(rows))
	for _, r := range rows {
		weights = append(weights, FormatQtyAmt(r, r.ExpectedQty))
	}
	return &ValidationError{Reason: fmt.Sprintf(
		"вес %.3f кг не подходит ни одной строке %s: ожидаются %s — закройте событие вручную, склад пересчитает сроки по этим позициям",
		float64(weightG)/1000, rows[0].Name, strings.Join(weights, " / "))}
}

// FormatQtyAmt — количество для текста ошибки/ожидания в единицах строки.
func FormatQtyAmt(e *Expected, qty int64) string {
	if e.Weighted {
		return fmt.Sprintf("%.3f кг", float64(qty)/1000)
	}
	return fmt.Sprintf("%d шт", qty)
}

// AggregateLots — группировка принятых сканов по (товар, срок): каждый скан —
// одна единица остатка (остатки ключуются парой (product_id, best_before),
// qty — штуки; вес в остатках не хранится).
func AggregateLots(units []ScannedUnit) []stock.LotIn {
	type key struct {
		productID  string
		bestBefore time.Time
	}
	counts := make(map[key]int64, len(units))
	for _, u := range units {
		k := key{productID: u.Row.ProductID, bestBefore: u.ExpDate}
		counts[k]++
	}

	lots := make([]stock.LotIn, 0, len(counts))
	for k, qty := range counts {
		lots = append(lots, stock.LotIn{
			ProductID:  k.productID,
			BestBefore: k.bestBefore,
			Qty:        qty,
		})
	}
	return lots
}

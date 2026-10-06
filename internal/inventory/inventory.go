// Пакет inventory содержит правила инвентаризации склада: разбор сканов
// внутренних штрих-кодов и сборка строк документа. Пакет ничего не знает про
// БД, МойСклад и HTTP; форматы штрих-кодов — инвариант internal/innercode.
package inventory

import (
	"errors"
	"fmt"
	"strings"

	"warehouseHelper/internal/innercode"
)

// Product — позиция каталога, нужная инвентаризации (проекция каталога).
type Product struct {
	ID           string // uuid товара в МС
	InternalCode string // код склада (8 цифр); пусто — товар не сканируется
	Name         string
	UOM          string // «кг»/«шт»/… (тип учёта берётся из единицы измерения)
	BuyPriceKop  *int64 // закупочная цена, копейки; nil — не задана (в документ идёт 0)
}

// Line — строка отчёта: одна позиция группы со фактическим количеством.
type Line struct {
	ProductID    string
	InternalCode string
	Name         string
	UOM          string
	Weighted     bool
	Fact         float64 // весовой — килограммы (граммы/1000), штучный — штуки
	Scans        int     // сколько сканов легло в строку
	PriceKop     int64   // закупочная цена, копейки; 0 — не задана
	Scanable     bool    // у товара есть код склада (можно сканировать)
}

// Preview — собранный отчёт по группе: ВСЕ позиции вида инвентаризации.
type Preview struct {
	Lines   []Line
	Total   int // позиций в группе
	Scanned int // позиций, у которых есть хотя бы один скан
	Scans   int // принятых сканов
}

// Position — строка документа МС.
type Position struct {
	ProductID string
	Quantity  float64 // факт: кг для весовых, штуки для штучных
	PriceKop  int64
}

var (
	// ErrScanInvalid — скан не разобран как внутренний штрих-код (чужая длина
	// или невалидные данные) — страница такие сканы отбивает бипом.
	ErrScanInvalid = errors.New("inventory: невалидный внутренний штрих-код")
	// ErrScanNotInGroup — код разобран, но товара с таким кодом нет среди
	// позиций выбранного вида инвентаризации.
	ErrScanNotInGroup = errors.New("inventory: скан не из выбранного вида инвентаризации")
)

// Weighted — весовой ли товар. Тип учёта выводится из единицы измерения:
// килограммы, граммы и тонны — весовые, всё остальное — штучное.
func Weighted(uom string) bool {
	switch strings.ToLower(strings.TrimSpace(uom)) {
	case "кг", "г", "т":
		return true
	default:
		return false
	}
}

// Aggregate собирает отчёт по группе: строки идут в порядке входа products,
// сканы раскладываются по внутреннему коду товара. Весовые сканы суммируются
// в граммах (Fact — килограммы), штучные — в штуках. Первый невалидный скан
// останавливает разбор и возвращается как ошибка.
func Aggregate(products []Product, scans []string) (Preview, error) {
	preview := Preview{
		Lines: make([]Line, 0, len(products)),
		Total: len(products),
	}
	index := make(map[string]int, len(products))
	for _, p := range products {
		line := Line{
			ProductID:    p.ID,
			InternalCode: p.InternalCode,
			Name:         p.Name,
			UOM:          p.UOM,
			Weighted:     Weighted(p.UOM),
			Scanable:     p.InternalCode != "",
		}
		if p.BuyPriceKop != nil {
			line.PriceKop = *p.BuyPriceKop
		}
		preview.Lines = append(preview.Lines, line)
		if line.Scanable {
			index[line.InternalCode] = len(preview.Lines) - 1
		}
	}

	grams := make([]int64, len(products))
	qty := make([]float64, len(products))

	for _, raw := range scans {
		scan := strings.TrimSpace(raw)
		if scan == "" {
			return Preview{}, fmt.Errorf("%w: пустой скан %q", ErrScanInvalid, raw)
		}
		parsed, err := innercode.Parse(scan)
		if err != nil {
			return Preview{}, fmt.Errorf("%w: скан %q: %w", ErrScanInvalid, scan, err)
		}
		i, ok := index[parsed.InternalCode]
		if !ok {
			return Preview{}, fmt.Errorf("%w: код %s, скан %q", ErrScanNotInGroup, parsed.InternalCode, scan)
		}

		line := &preview.Lines[i]
		if line.Weighted {
			grams[i] += int64(parsed.WeightG)
		} else {
			qty[i] += float64(parsed.Qty)
		}
		line.Scans++
		preview.Scans++
		if line.Scans == 1 {
			preview.Scanned++
		}
	}

	for i := range preview.Lines {
		if preview.Lines[i].Weighted {
			// Граммы целые — деление даёт ровно три знака после точки.
			preview.Lines[i].Fact = float64(grams[i]) / 1000
			continue
		}
		preview.Lines[i].Fact = qty[i]
	}

	return preview, nil
}

// Positions возвращает строки документа МС по всем строкам отчёта, включая
// непросканированные (количество 0). Порядок — как в Lines.
func Positions(p Preview) []Position {
	positions := make([]Position, 0, len(p.Lines))
	for _, line := range p.Lines {
		positions = append(positions, Position{
			ProductID: line.ProductID,
			Quantity:  line.Fact,
			PriceKop:  line.PriceKop,
		})
	}
	return positions
}

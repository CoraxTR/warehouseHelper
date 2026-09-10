package usecase

import (
	"context"
	"math"

	"warehouseHelper/internal/returns"
)

// Сборка ожиданий возврата по событию аудита: из каких строк (удалённые
// позиции диффа / позиции живого заказа) и с какими количествами склад
// должен вернуть товары в продажу.

// candidate — строка-кандидат возврата: удалённая позиция из diff события
// (kind=positions_removed) или позиция живого заказа (kind=order_cancelled).
type candidate struct {
	ProductID string  // uuid товара (последний сегмент meta.href)
	Name      string  // название (из диффа / позиции заказа)
	Quantity  float64 // количество строки (кг для весовых, штуки для штучных)
	Reserve   float64 // резерв строки на момент проверки
}

// qtyInt — количество в единицах сверки: весовой товар → граммы
// (round кг×1000 — вес этикетки 29 в граммах), штучный → штуки (round).
// qtyUnit — единица сверки количества строки возврата: весовой товар
// сводится в граммы (кг этикетки/диффа ×1000), штучный — в единицы.
type qtyUnit uint8

const (
	qtyGrams  qtyUnit = iota // весовой: сравнение/накопление в граммах
	qtyPieces                // штучный: по количеству единиц
)

func qtyInt(v float64, u qtyUnit) int64 {
	if u == qtyGrams {
		return int64(math.Round(v * 1000))
	}
	return int64(math.Round(v))
}

// reservedEquals — «товар физически отложен»: quantity == reserve строго,
// без допуска (решение пользователя). Сравнение в единицах сверки (int),
// не float по кг — 0.657 и 0.657 в double равны, но округление защищает
// от хвостов вида 0.6570000000001.
func reservedEquals(q, r float64, u qtyUnit) bool {
	return qtyInt(q, u) == qtyInt(r, u)
}

// candidates — строки-кандидаты по виду события. Источник правды —
// живой МС (снимков в БД нет): для удаления — раскрытие audit/<id>/events,
// для отмены — позиции заказа в текущем состоянии (МС reserve при отмене
// НЕ сбрасывает — проверено пользователем 08.09.2026).
func (uc *UseCase) candidates(ctx context.Context, ev *returns.ReturnEvent) ([]candidate, error) {
	switch ev.Kind {
	case returns.KindRemoved:
		rows, err := uc.audit.FetchAuditDetail(ctx, ev.ID)
		if err != nil {
			return nil, err
		}

		out := parseDetail(rows, uc.cfg.CancelledStateID)
		cands := make([]candidate, 0, len(out.removals))
		for _, r := range out.removals {
			cands = append(cands, candidate{
				ProductID: r.ProductID,
				Name:      r.Name,
				Quantity:  r.Quantity,
				Reserve:   r.Reserve,
			})
		}
		return cands, nil

	case returns.KindCancelled:
		positions, err := uc.audit.FetchOrderPositions(ctx, ev.OrderID)
		if err != nil {
			return nil, err
		}

		cands := make([]candidate, 0, len(positions))
		for _, p := range positions {
			cands = append(cands, candidate{
				ProductID: lastPathSegment(p.Assortment.Meta.HREF),
				Name:      p.Assortment.Name,
				Quantity:  p.Quantity,
				Reserve:   p.Reserve,
			})
		}
		return cands, nil

	default:
		return nil, returns.ErrEventNotFound
	}
}

// buildExpected — ожидания возврата: по строке на КАЖДУЮ прошедшую фильтры
// строку отчёта, без склейки по товару (решение владельца 10.09). Порядок
// строк — порядок отчёта (позиций заказа / диффа аудита), он же Idx.
// Пропускаются: строки без резерва (quantity != reserved — «товар не был
// физически отложен»), без internal_code, неизвестные каталогу и с нулевым
// количеством. Пустой результат — ErrNothingToReturn (возвращать нечего).
func (uc *UseCase) buildExpected(ctx context.Context, ev *returns.ReturnEvent) ([]returns.Expected, error) {
	cands, err := uc.candidates(ctx, ev)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, returns.ErrNothingToReturn
	}

	ids := make([]string, 0, len(cands))
	seen := make(map[string]struct{}, len(cands))
	for _, c := range cands {
		if c.ProductID == "" {
			continue
		}
		if _, ok := seen[c.ProductID]; ok {
			continue
		}
		seen[c.ProductID] = struct{}{}
		ids = append(ids, c.ProductID)
	}

	products, err := uc.catalog.ProductsByMSIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	expected := make([]returns.Expected, 0, len(cands))
	for _, c := range cands {
		p, ok := products[c.ProductID]
		if !ok || p.InternalCode == "" {
			continue // нет в каталоге или без кода склада — не складской товар
		}

		unit := qtyPieces
		if p.Weighted {
			unit = qtyGrams
		}
		if !reservedEquals(c.Quantity, c.Reserve, unit) {
			continue // не отложен физически — возвращать нечего
		}
		qty := qtyInt(c.Quantity, unit)
		if qty <= 0 {
			continue // пустая строка: погасить её сканом нельзя
		}

		expected = append(expected, returns.Expected{
			Idx:          len(expected),
			ProductID:    c.ProductID,
			InternalCode: p.InternalCode,
			Name:         c.Name,
			Weighted:     p.Weighted,
			ExpectedQty:  qty,
		})
	}

	if len(expected) == 0 {
		return nil, returns.ErrNothingToReturn
	}
	return expected, nil
}

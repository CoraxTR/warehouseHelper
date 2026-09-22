package usecase

import (
	"context"

	"warehouseHelper/internal/returns"
	"warehouseHelper/internal/scanmatch"
)

// Сборка ожиданий возврата по событию аудита: из каких строк (удалённые
// позиции диффа / позиции живого заказа) и с какими количествами склад
// должен вернуть товары в продажу. Правила отбора строк (резерв, код склада,
// единицы сверки) живут в общем ядре internal/scanmatch.

// candidates — строки-кандидаты по виду события. Источник правды —
// живой МС (снимков в БД нет): для удаления — раскрытие audit/<id>/events,
// для отмены — позиции заказа в текущем состоянии (МС reserve при отмене
// НЕ сбрасывает — проверено пользователем 08.09.2026).
func (uc *UseCase) candidates(ctx context.Context, ev *returns.ReturnEvent) ([]scanmatch.Candidate, error) {
	switch ev.Kind {
	case returns.KindRemoved:
		rows, err := uc.audit.FetchAuditDetail(ctx, ev.ID)
		if err != nil {
			return nil, err
		}

		out := parseDetail(rows, uc.cfg.CancelledStateID)
		cands := make([]scanmatch.Candidate, 0, len(out.removals))
		for _, r := range out.removals {
			cands = append(cands, scanmatch.Candidate{
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

		cands := make([]scanmatch.Candidate, 0, len(positions))
		for _, p := range positions {
			cands = append(cands, scanmatch.Candidate{
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

// buildExpected — ожидания возврата: состав строк собирает scanmatch по
// строкам-кандидатам и каталогу (по строке на КАЖДУЮ прошедшую фильтры
// строку, без склейки по товару — решение владельца 10.09). Пустой
// результат — ErrNothingToReturn (возвращать нечего).
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

	expected := scanmatch.BuildExpected(cands, scanmatchProducts(products))
	if len(expected) == 0 {
		return nil, returns.ErrNothingToReturn
	}
	return expected, nil
}

// scanmatchProducts — каталог возвратов в форме ядра сверки: BuildExpected
// читает только код склада и тип учёта (название строки приходит из кандидата —
// позиции заказа или диффа), поэтому returns.CatalogProduct с названием для
// подписей наклеек сводится к scanmatch.CatalogProduct.
func scanmatchProducts(products map[string]returns.CatalogProduct) map[string]scanmatch.CatalogProduct {
	out := make(map[string]scanmatch.CatalogProduct, len(products))
	for id, p := range products {
		out[id] = scanmatch.CatalogProduct{
			ProductID:    p.ProductID,
			InternalCode: p.InternalCode,
			Weighted:     p.Weighted,
		}
	}
	return out
}

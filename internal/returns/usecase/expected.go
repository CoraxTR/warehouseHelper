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
// живой МС (снимков в БД нет): для ушедших позиций (удаление строки целиком
// или урезание количества) — раскрытие audit/<id>/events,
// для отмены — позиции заказа в текущем состоянии (МС reserve при отмене
// НЕ сбрасывает — проверено пользователем 08.09.2026).
func (uc *UseCase) candidates(ctx context.Context, ev *returns.ReturnEvent) ([]scanmatch.Candidate, error) {
	switch ev.Kind {
	// KindManualWeightDown — подмножество KindRemoved (урезание весовой строки):
	// источник тот же дифф аудита, поэтому идёт этой же веткой (нужно retryNew,
	// где вид уже сохранён в БД).
	case returns.KindRemoved, returns.KindManualWeightDown:
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
				Released:  r.Released,
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

	products, err := uc.catalog.ProductsByMSIDs(ctx, candidateProductIDs(cands))
	if err != nil {
		return nil, err
	}

	expected := scanmatch.BuildExpected(cands, scanmatchProducts(products))
	if len(expected) == 0 {
		return nil, returns.ErrNothingToReturn
	}
	return expected, nil
}

// candidateProductIDs — uuid товаров-кандидатов без повторов, в порядке
// кандидатов: вход шва каталога (ProductsByMSIDs дедуп не делает, а один товар
// в событии может встречаться несколькими строками).
func candidateProductIDs(cands []scanmatch.Candidate) []string {
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
	return ids
}

// manualWeightDrops — отложенные весовые строки события, вес которых менеджер
// уменьшил вручную (дифф аудита: количество уменьшилось, резерв МС понизил
// вслед за ним, Released > 0). Кандидаты берём тем же путём, что buildExpected
// (события из диффа аудита), но фильтруем по каталогу сами: ядро scanmatch не
// зовём — оно обслуживает ещё и «возврат в сроки при переподборе», его правила
// не трогаем. К событию отмены отношения не имеет (у позиций заказа Released
// не бывает — источник другой), поэтому сразу пусто.
func (uc *UseCase) manualWeightDrops(ctx context.Context, ev *returns.ReturnEvent) ([]returns.ManualWeightDrop, error) {
	if ev.Kind != returns.KindRemoved && ev.Kind != returns.KindManualWeightDown {
		return nil, nil
	}

	cands, err := uc.candidates(ctx, ev)
	if err != nil {
		return nil, err
	}

	products, err := uc.catalog.ProductsByMSIDs(ctx, candidateProductIDs(cands))
	if err != nil {
		return nil, err
	}

	out := make([]returns.ManualWeightDrop, 0, len(cands))
	for _, c := range cands {
		p, ok := products[c.ProductID]
		// Только весовая строка складского товара: штучную урезку ловит
		// возврат в продажу (scanmatch), рост веса и товары без кода склада —
		// не наше событие.
		if !ok || !p.Weighted || p.InternalCode == "" || c.Released <= 0 {
			continue
		}
		out = append(out, returns.ManualWeightDrop{
			ProductID: c.ProductID,
			Name:      c.Name,
			BeforeKg:  c.Quantity,
			AfterKg:   c.Quantity - c.Released,
		})
	}
	return out, nil
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

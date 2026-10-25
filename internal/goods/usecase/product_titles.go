package usecase

import (
	"context"
	"fmt"

	"warehouseHelper/internal/metrics"
)

// ProductTitles — названия товаров по id: шов для чужих модулей (цены
// поставщиков показывают название товара, а таблица products — каталога).
// Неизвестные id в карту не попадают.
func (uc *GoodsUseCase) ProductTitles(ctx context.Context, ids []string) (map[string]string, error) {
	done := metrics.Track(trackPkg, "ProductTitles")
	defer done()

	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	products, err := uc.repo.GetProductsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("названия товаров: %w", err)
	}
	for _, p := range products {
		out[p.ID] = p.Name
	}

	return out, nil
}

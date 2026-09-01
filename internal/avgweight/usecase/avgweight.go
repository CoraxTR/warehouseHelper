// Пакет usecase — сценарии модуля «Средний вес».
package usecase

import (
	"context"
	"fmt"
	"strconv"

	"warehouseHelper/internal/avgweight"
	"warehouseHelper/internal/metrics"
)

const trackPkg = "avgweight"

// Repository — хранилище весов принятых единиц (реализует PGClient).
type Repository interface {
	// InsertReceivedWeights пишет все веса партии одной транзакцией.
	InsertReceivedWeights(ctx context.Context, rows []avgweight.WeightRow) error
	// TrimReceivedWeights удаляет строки товара за пределами последних keep
	// записей (FIFO по возрастанию id).
	TrimReceivedWeights(ctx context.Context, productID string, keep int) error
	// AverageWeightGrams — средний вес товара по оставшимся строкам, граммы.
	AverageWeightGrams(ctx context.Context, productID string) (float64, error)
}

// ProductWeightUpdater — контракт обновления среднего веса в каталоге
// (реализует *gucase.GoodsUseCase; products.average_weight пишет только каталог).
type ProductWeightUpdater interface {
	UpdateAverageWeight(ctx context.Context, productID string, avgKg float64) error
}

// WikiWeightUpdater — контракт обновления среднего веса на странице товара
// (реализует *wucase.WikiUseCase).
type WikiWeightUpdater interface {
	UpdateProductAverageWeight(ctx context.Context, productID, averageWeight string) error
}

// UseCase — сценарии модуля «Средний вес».
type UseCase struct {
	repo     Repository
	products ProductWeightUpdater
	wiki     WikiWeightUpdater
	keep     int // сколько последних весов хранить на товар (FIFO)
}

// NewUseCase создаёт юзкейс: хранилище, адаптеры каталога и вики, лимит
// истории весов на товар (настройка приложения PRODUCT_WEIGHTS_HISTORY).
func NewUseCase(repo Repository, products ProductWeightUpdater, wiki WikiWeightUpdater, keep int) *UseCase {
	return &UseCase{repo: repo, products: products, wiki: wiki, keep: keep}
}

// RecordWeights записывает единичные веса партии поштучно и пересчитывает
// средний вес. Для каждого товара партии: trim до последних keep записей
// (FIFO по id) → среднее по оставшимся → каталог (products.average_weight,
// кг) и вики (страница товара). Ошибка записи/trim/расчёта фатальна — веса
// принятых единиц это ядро модуля, приёмка без них не завершается. Сбой
// синка каталога или вики приёмку не роняет: возвращается списком
// предупреждений (уходят в отчёт).
func (uc *UseCase) RecordWeights(ctx context.Context, rows []avgweight.WeightRow) (warnings []string, err error) {
	defer metrics.Track(trackPkg, "RecordWeights")()
	if len(rows) == 0 {
		return nil, nil
	}

	if err := uc.repo.InsertReceivedWeights(ctx, rows); err != nil {
		return nil, fmt.Errorf("записать веса: %w", err)
	}

	// Уникальные товары партии в порядке появления.
	seen := make(map[string]struct{}, len(rows))
	productIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		if _, ok := seen[r.ProductID]; ok {
			continue
		}
		seen[r.ProductID] = struct{}{}
		productIDs = append(productIDs, r.ProductID)
	}

	for _, productID := range productIDs {
		if err := uc.repo.TrimReceivedWeights(ctx, productID, uc.keep); err != nil {
			return nil, fmt.Errorf("обрезать веса товара %s: %w", productID, err)
		}

		grams, err := uc.repo.AverageWeightGrams(ctx, productID)
		if err != nil {
			return nil, fmt.Errorf("средний вес товара %s: %w", productID, err)
		}
		avgKg := grams / 1000

		if uc.products != nil {
			if err := uc.products.UpdateAverageWeight(ctx, productID, avgKg); err != nil {
				warnings = append(warnings, fmt.Sprintf("товар %s: каталог не обновлён (%v)", productID, err))
			}
		}
		if uc.wiki != nil {
			if err := uc.wiki.UpdateProductAverageWeight(ctx, productID, strconv.FormatFloat(avgKg, 'f', -1, 64)); err != nil {
				warnings = append(warnings, fmt.Sprintf("товар %s: вики не обновлена (%v)", productID, err))
			}
		}
	}

	return warnings, nil
}

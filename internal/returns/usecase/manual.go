package usecase

import (
	"context"
	"fmt"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/stock"
)

// Методы страницы «Ручной возврат»: возврат кусков в продажу сканированием
// БЕЗ события аудита — ни заказов, ни ожиданий. Каждый скан — этикетка куска
// (29 цифр): товар резолвится по internal_code каталога, кусок ложится в лот
// (товар, срок из этикетки) записью в остатки (AcceptStock). Сервер —
// авторитет: батч валидируется целиком, ошибка любой строки отклоняет все
// сканы (ничего не записывается).

// ManualReturn — ручной возврат: сканы принимаются как есть (коробки 33 не
// участвуют; товар без кода склада в каталоге — ValidationError). Дубликаты
// сканов — отдельные куски: этикетки штучных товаров с одним сроком
// идентичны, повтор неотличим от второго куска. Возвращает число кусков.
func (uc *UseCase) ManualReturn(ctx context.Context, scans []string) (int, error) {
	if len(scans) == 0 {
		return 0, &ValidationError{Reason: "нет сканов"}
	}

	parsed := make([]innercode.Code, 0, len(scans))
	seen := make(map[string]struct{}, len(scans))
	for _, raw := range scans {
		code, err := innercode.Parse(raw)
		if err != nil {
			return 0, &ValidationError{Reason: fmt.Sprintf("неверный штрих-код %q: %v", raw, err)}
		}
		if code.Kind != innercode.KindItem {
			return 0, &ValidationError{Reason: fmt.Sprintf("штрих-код %q — коробка (33): возвращаются только куски", raw)}
		}
		parsed = append(parsed, code)
		seen[code.InternalCode] = struct{}{}
	}

	unique := make([]string, 0, len(seen))
	for code := range seen {
		unique = append(unique, code)
	}
	products, err := uc.catalog.ProductsByInternalCodes(ctx, unique)
	if err != nil {
		return 0, err
	}

	// Куски одного товара с одним сроком складываются в один лот.
	type key struct {
		productID  string
		bestBefore time.Time
	}
	counts := make(map[key]int64, len(parsed))
	for _, code := range parsed {
		p, ok := products[code.InternalCode]
		if !ok || p.ProductID == "" {
			return 0, &ValidationError{Reason: fmt.Sprintf("товар с кодом %s не в каталоге — кусок вернуть нельзя", code.InternalCode)}
		}
		k := key{productID: p.ProductID, bestBefore: code.ExpDate}
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

	if err := uc.stock.AcceptStock(ctx, lots); err != nil {
		return 0, fmt.Errorf("stock accept manual return: %w", err)
	}
	return len(parsed), nil
}

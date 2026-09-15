package usecase

import (
	"context"
	"fmt"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/stock"
)

// Методы страницы «Вывод из продажи»: снятие кусков с продажи сканированием
// внутренних этикеток — зеркало ручного возврата (manual.go) без события
// аудита и без документа МойСклад. Каждый скан — этикетка куска (29 цифр):
// товар резолвится по internal_code каталога, кусок списывается из лота
// (товар, срок из этикетки) записью в остатки (stock.PickStock). Сервер —
// авторитет: батч валидируется целиком, ошибка любой строки отклоняет все
// сканы (ничего не списывается).

// WithdrawFromSale — вывод из продажи: сканы этикеток кусков убирают куски из
// сроков. Принимаются только куски (29); коробки (33) и чужой/невалидный
// штрих-код — ValidationError, батч отклоняется целиком. Дубликаты сканов —
// отдельные куски: этикетки штучных товаров с одним сроком идентичны, повтор
// неотличим от второго куска. Куски одного товара с одним сроком складываются
// в один лот и списываются одним вызовом stock.PickStock. Дефицит (в лоте
// меньше, чем отсканировано) разбирает сам PickStock: списание до нуля и
// уведомление складу «Необходимо обновить сроки по …», отрицательных остатков
// не бывает. Возвращает число списанных (отсканированных) кусков.
func (uc *UseCase) WithdrawFromSale(ctx context.Context, scans []string) (int, error) {
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
			return 0, &ValidationError{Reason: fmt.Sprintf("штрих-код %q — коробка (33): снимаются только куски", raw)}
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

	// Куски одного товара с одним сроком складываются в один лот — одно
	// списание на лот (PickStock сам дедуплицирует и нормализует даты).
	type key struct {
		productID  string
		bestBefore time.Time
	}
	counts := make(map[key]int64, len(parsed))
	for _, code := range parsed {
		p, ok := products[code.InternalCode]
		if !ok || p.ProductID == "" {
			return 0, &ValidationError{Reason: fmt.Sprintf("товар с кодом %s не в каталоге — кусок снять нельзя", code.InternalCode)}
		}
		k := key{productID: p.ProductID, bestBefore: code.ExpDate}
		counts[k]++
	}

	lots := make([]stock.PickLotIn, 0, len(counts))
	for k, qty := range counts {
		lots = append(lots, stock.PickLotIn{
			ProductID:  k.productID,
			BestBefore: k.bestBefore,
			Qty:        qty,
		})
	}

	if err := uc.stock.PickStock(ctx, lots); err != nil {
		return 0, fmt.Errorf("stock pick withdraw from sale: %w", err)
	}
	return len(parsed), nil
}

package usecase

import (
	"context"
	"errors"
	"fmt"

	"warehouseHelper/internal/metrics"
)

// Срок поставки товара — «дней до поставки» для расчёта недостатка.
//
// Поле уже есть в расписании: product_order_schedules.delay_days — «макс. дней
// между заказом и доставкой», 0 = на следующий день (так было и у поставщика
// до переноса расписаний на пару). Дни недели (order_days/delivery_days) для
// срока НЕ используются: они задают, когда можно заказать и когда привозят,
// а срок поставки — отдельное число.
//
// Товар: минимум по вариантам, у которых срок заполнен (у пары может быть
// несколько режимов: «общий» 0-1 день, «под заказ» 7). Варианты без заполненного
// срока не в счёт; товар без таких вариантов в карту не попадает.
func (uc *GoodsUseCase) ProductLeadDays(ctx context.Context, productIDs []string) (map[string]int, error) {
	done := metrics.Track(trackPkg, "ProductLeadDays")
	defer done()

	out := make(map[string]int, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	if uc.schedules == nil {
		return nil, errors.New("варианты заказа не подключены")
	}

	items, err := uc.schedules.ListOrderSchedulesByProducts(ctx, productIDs)
	if err != nil {
		return nil, fmt.Errorf("варианты заказа списка товаров: %w", err)
	}

	for _, item := range items {
		if item.DelayDays == nil || *item.DelayDays < 0 {
			continue
		}
		lead := int(*item.DelayDays)
		if cur, ok := out[item.ProductID]; !ok || lead < cur {
			out[item.ProductID] = lead
		}
	}

	return out, nil
}

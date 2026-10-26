package usecase

import (
	"context"
	"fmt"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

// Срок поставки товара — «дней до поставки» для расчёта недостатка.
//
// Отдельного поля нет: срок выводится из вариантов заказа пары (товар,
// поставщик) — product_order_schedules. Срок варианта = минимум по его дням
// заказа дней до ближайшего дня доставки; срок товара = минимум по всем его
// вариантам (решение владельца 26.10.2026: «минимальное значение из доступных
// вариантов отгрузок»).
//
// Дни недели — ISO 1=Пн..7=Вс. Совпадение дня заказа и дня доставки (delta 0)
// считается неделей: доставка «в тот же день» поставкой не является, товар
// приедет следующей доставкой. delay_days (макс. задержка) в срок НЕ входит:
// это допуск к сроку, а не срок.

// ProductLeadDays — срок поставки по списку товаров: map[productID]дней.
// Товары без вариантов заказа (или с пустыми днями заказа/доставки) в карту
// не попадают — «дней до поставки не заполнено».
func (uc *GoodsUseCase) ProductLeadDays(ctx context.Context, productIDs []string) (map[string]int, error) {
	done := metrics.Track(trackPkg, "ProductLeadDays")
	defer done()

	out := make(map[string]int, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	if uc.schedules == nil {
		return nil, fmt.Errorf("варианты заказа не подключены: %w", domain.ErrProductNotFound)
	}

	schedules, err := uc.schedules.ListOrderSchedulesByProducts(ctx, productIDs)
	if err != nil {
		return nil, fmt.Errorf("варианты заказа списка товаров: %w", err)
	}

	byProduct := make(map[string][]domain.OrderSchedule, len(productIDs))
	for _, s := range schedules {
		byProduct[s.ProductID] = append(byProduct[s.ProductID], s)
	}

	for id, list := range byProduct {
		if lead, ok := minLeadDays(list); ok {
			out[id] = lead
		}
	}

	return out, nil
}

// minLeadDays — минимальный срок поставки по вариантам заказа одного товара.
// ok=false — ни у одного варианта срок не выводится.
func minLeadDays(variants []domain.OrderSchedule) (int, bool) {
	best := 0
	found := false
	for _, v := range variants {
		lead, ok := variantLeadDays(v.OrderDays, v.DeliveryDays)
		if !ok {
			continue
		}
		if !found || lead < best {
			best, found = lead, true
		}
	}

	return best, found
}

// variantLeadDays — срок поставки варианта: минимум по дням заказа дней до
// ближайшего дня доставки. ok=false — нет дней заказа или дней доставки.
func variantLeadDays(orderDays, deliveryDays []int16) (int, bool) {
	if len(orderDays) == 0 || len(deliveryDays) == 0 {
		return 0, false
	}

	best := 0
	found := false
	for _, o := range orderDays {
		for _, d := range deliveryDays {
			if !validWeekday(o) || !validWeekday(d) {
				continue
			}
			delta := (int(d) - int(o) + weekdayCount) % weekdayCount
			if delta == 0 {
				// Тот же день недели — это доставка следующего круга.
				delta = weekdayCount
			}
			if !found || delta < best {
				best, found = delta, true
			}
		}
	}

	return best, found
}

// weekdayCount — дней в неделе (дни заказа/доставки — ISO 1..7).
const weekdayCount = 7

// validWeekday — день в диапазоне 1..7 (мусор из старых данных не ломает срок).
func validWeekday(d int16) bool { return d >= 1 && d <= weekdayCount }

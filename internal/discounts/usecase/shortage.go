package usecase

// Недостаток товара — вторая сторона той же монеты, что избыток.
//
// Избыток отвечает «склад перегружен» (коэф Q/(v×D) > 1), недостаток —
// «до ближайшей поставки не доживём». Правило владельца (26.10.2026):
//
//	недостаток ⇔ наличие <= средние продажи в день × (дней до поставки + 1)
//
// Правая часть — сколько товара нужно, чтобы дотянуть до поставки и ещё на
// день после неё (страховка на день приёмки). «Дней до поставки» приходит
// из каталога: он выводит минимальный срок по вариантам заказа товара
// (`GoodsUseCase.ProductLeadDays`); у товара без вариантов заказа срок не
// заполнен — такой товар не оцениваем вовсе.
//
// Наличие — всё, что есть на складе: сумма остатков всех лотов товара
// (просроченные лоты к этому моменту уже выведены из оборота заморозкой).
// Продажи в день — Rate действующего среднего оборота (та же величина, по
// которой считается избыток); нет данных о продажах — не оцениваем.

// IsShortage — признак недостатка по одной позиции. leadDays <= 0 (срок не
// заполнен) и ratePerDay <= 0 (нет продаж/данных) — не недостаток: судить
// не по чему.
func IsShortage(available int64, ratePerDay float64, leadDays int) bool {
	if leadDays <= 0 || ratePerDay <= 0 {
		return false
	}

	// Наличие в штуках дробное не бывает, а порог — дробный: сравниваем как
	// float, чтобы не терять копейки оборота (2.5 шт/день × 3 дня = 7.5).
	return float64(available) <= ratePerDay*float64(leadDays+1)
}

// Shortages — признаки недостатка по разобранным парам (результат Evaluate).
// map[productID]bool заполняется ТОЛЬКО для товаров, по которым есть и срок
// поставки, и данные о продажах: остальные (нет вариантов заказа, нет истории)
// в карту не попадают — «признак не определён», а не «недостатка нет».
func Shortages(pairs []PairState, leadDays map[string]int) map[string]bool {
	available := make(map[string]int64, len(leadDays))
	rate := make(map[string]float64, len(leadDays))
	hasRate := make(map[string]bool, len(leadDays))

	for _, p := range pairs {
		// CumQty последней пары — весь остаток товара (пары идут по сроку
		// от ближнего к дальнему), поэтому берём максимум.
		if p.CumQty > available[p.ProductID] {
			available[p.ProductID] = p.CumQty
		}
		// Оборот у товара один на все его пары: достаточно первой пары с
		// данными.
		if p.HasRate && !hasRate[p.ProductID] {
			rate[p.ProductID] = p.Rate
			hasRate[p.ProductID] = true
		}
	}

	out := make(map[string]bool, len(leadDays))
	for id, lead := range leadDays {
		if _, hasLots := available[id]; !hasLots {
			continue // товара нет в выборке (например, закончился) — не судим
		}
		if !hasRate[id] {
			continue // продаж не было — среднего нет, признак не определить
		}
		out[id] = IsShortage(available[id], rate[id], lead)
	}

	return out
}

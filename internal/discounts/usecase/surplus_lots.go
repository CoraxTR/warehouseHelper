package usecase

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"warehouseHelper/internal/discounts"
	"warehouseHelper/internal/metrics"
)

// SurplusLots — избытки дня для отчёта в чат управления.
//
// Расчёт тот же, что даёт скидку 10 % по избытку: снапшот входа, оборот шва и
// Evaluate — поэтому числа чата не расходятся с движком. Пара попадает в
// список, если получает скидку по избытку (HasSurplus), а не по своему личному
// коэффициенту: избыток товара считается накопленно (Q до срока, FIFO).
//
// Числа для человека: CumQty — накопленный остаток до срока включительно,
// PlannedQty — сколько ориентировочно продадим до этой даты (v × D).
func (uc *UseCase) SurplusLots(ctx context.Context, now time.Time) ([]discounts.SurplusLot, error) {
	done := metrics.Track("discounts", "SurplusLots")
	defer done()

	today := beginningOfDay(now)

	inputs, err := uc.loadInputs(ctx, today)
	if err != nil {
		return nil, err
	}

	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return nil, fmt.Errorf("оборот товаров расчёта: %w", err)
	}

	pairs := Evaluate(inputs, rates, today)

	out := make([]discounts.SurplusLot, 0, 16)
	for _, p := range pairs {
		if !p.HasSurplus {
			continue
		}
		site, _ := discounts.Resolve(p.Manual, p.Permanent, p.Expiry, p.Surplus)
		tg := p.TelegramManual
		if tg == nil {
			tg = p.TelegramPlain
		}
		out = append(out, discounts.SurplusLot{
			ProductID:  p.ProductID,
			Name:       p.Name,
			BestBefore: p.BestBefore,
			CumQty:     p.CumQty,
			Qty:        p.Qty,
			PlannedQty: int64(math.Round(p.Rate * float64(p.DaysLeft))),
			Coeff:      p.Coeff,
			Expiry:     p.Expiry,
			Site:       site,
			TG:         tg,
		})
	}
	// Тяжёлые избытки — первыми: чем больше коэффициент, тем важнее решение.
	// При равном коэффициенте — по имени и сроку, чтобы отчёт был стабилен.
	slices.SortStableFunc(out, func(a, b discounts.SurplusLot) int {
		switch {
		case a.Coeff > b.Coeff:
			return -1
		case a.Coeff < b.Coeff:
			return 1
		}
		if a.Name != b.Name {
			if a.Name < b.Name {
				return -1
			}
			return 1
		}
		return a.BestBefore.Compare(b.BestBefore)
	})

	return out, nil
}

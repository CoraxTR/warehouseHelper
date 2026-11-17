package usecase

import (
	"testing"

	"warehouseHelper/internal/discounts"
)

// planPair — минимальное состояние пары для плана дня: срок, ступень по сроку
// и ручные скидки каналов.
func planPair(id string, manual, tgManual, expiry *int16) PairState {
	p := PairState{
		Key:            discounts.LotKey{ProductID: id, BestBefore: day(5)},
		ProductID:      id,
		DaysLeft:       4,
		Manual:         manual,
		TelegramManual: tgManual,
		Expiry:         expiry,
	}
	if manual != nil {
		p.Applied = manual
		p.AppliedPlain = manual
	}

	return p
}

// Запрет «не в ТГ» режет только рассылку: ступень дня уходит на сайт сразу, как
// у «лишних» (решение владельца 10.11.2026).
func TestBuildDayPlanTelegramBanGetsSiteStep(t *testing.T) {
	expiry, tgZero := int16(50), int16(0)

	plan := buildDayPlan([]PairState{planPair("p1", nil, &tgZero, &expiry)}, nil, 10)

	if len(plan.slot) != 0 {
		t.Errorf("слот = %d позиций, want 0 (запрет ТГ)", len(plan.slot))
	}
	if len(plan.extra) != 1 || plan.extra[0].percent != 50 {
		t.Fatalf("extra = %+v, want одна позиция 50 %%", plan.extra)
	}
}

// Ручная скидка сайта важнее плана (Resolve: ручная побеждает как есть), поэтому
// при запрете ТГ паре с ручной сайта ступень не пишем: на сайте всё равно стоит
// значение менеджера. Ветка сознательно узкая — не «дыра», а правило.
func TestBuildDayPlanTelegramBanManualSiteSkipsStep(t *testing.T) {
	expiry, manual, tgZero := int16(50), int16(40), int16(0)

	plan := buildDayPlan([]PairState{planPair("p2", &manual, &tgZero, &expiry)}, nil, 10)

	if len(plan.slot) != 0 || len(plan.extra) != 0 {
		t.Fatalf("план = %+v, want пусто: ручную сайта план не трогает", plan)
	}
}

// Заморозка ручным нулём сайта важнее плана: пару не трогаем вовсе, даже если
// ступень по сроку выше.
func TestBuildDayPlanFrozenPairStaysOut(t *testing.T) {
	expiry, zero := int16(50), int16(0)

	plan := buildDayPlan([]PairState{planPair("p3", &zero, nil, &expiry)}, nil, 10)

	if len(plan.slot) != 0 || len(plan.extra) != 0 {
		t.Fatalf("план = %+v, want пусто (пара заморожена)", plan)
	}
}

// Лот с остатком суток: в рассылку не идёт, ступень дня — сайту сразу (решение
// владельца 17.11.2026: «не в ТГ» ≠ «не поднимать сайт»).
func TestBuildDayPlanShortLifeGetsSiteStep(t *testing.T) {
	expiry := int16(50)
	p := planPair("p4", nil, nil, &expiry)
	p.DaysLeft = 1

	plan := buildDayPlan([]PairState{p}, nil, 10)

	if len(plan.slot) != 0 {
		t.Errorf("слот = %d позиций, want 0 (слишком короткий срок)", len(plan.slot))
	}
	if len(plan.extra) != 1 || plan.extra[0].percent != 50 {
		t.Fatalf("extra = %+v, want одна позиция 50 %%", plan.extra)
	}
}

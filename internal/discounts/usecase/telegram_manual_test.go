package usecase

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"warehouseHelper/internal/discounts"
)

// Ручная скидка ТГ-канала глазами движка (решение владельца 29.09.2026): пара в
// работе и занимает место в окне, как поставленная расчётом, а сайт получает её
// значение подъёмом 16:00. До этой правки движок ручную ТГ не видел вообще
// (TelegramManual не заполнялось), поэтому пара не попадала ни в окно, ни в план,
// ни в подъём — ни на «Скидках», ни в правой колонке «Сроков».

// tgManualOpt — ручная скидка ТГ-колонки (её пишет карточка количества).
func tgManualOpt(v int16) func(*PairState) {
	return func(p *PairState) { p.TelegramManual = &v }
}

// percentOf — значение пары для сообщений теста.
func percentOf(v *int16) string {
	if v == nil {
		return "нет"
	}

	return strconv.Itoa(int(*v))
}

// TestWindowResolveManualTelegram — что окно берёт у пары: кандидат дня важнее
// ручной ТГ, заморозка ручным нулём важнее тоже, ноль в ТГ — «скидки нет».
func TestWindowResolveManualTelegram(t *testing.T) {
	tests := []struct {
		name   string
		opts   []func(*PairState)
		want   *int16
		source discounts.Source
	}{
		{
			"ручная ТГ без кандидатов дня — пара в окне",
			[]func(*PairState){tgManualOpt(30)},
			new(int16(30)),
			discounts.SourceTelegramManual,
		},
		{
			"ручная ТГ 0 % — скидки нет",
			[]func(*PairState){tgManualOpt(0)},
			nil,
			discounts.SourceNone,
		},
		{
			"кандидат дня важнее: ручная сайта",
			[]func(*PairState){manualOpt(40), tgManualOpt(30)},
			new(int16(40)),
			discounts.SourceManual,
		},
		{
			"кандидат дня важнее: ступень по сроку",
			[]func(*PairState){expiryOpt(), tgManualOpt(30)},
			new(int16(30)),
			discounts.SourceExpiry,
		},
		{
			"заморозка сайта ручным нулём важнее ручной ТГ",
			[]func(*PairState){manualOpt(0), tgManualOpt(30)},
			new(int16(0)),
			discounts.SourceManual,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := regPair("p1", "Стейк", day(9), tc.opts...)

			got, src := p.windowResolve()
			if !sameDiscount(got, tc.want) || src != tc.source {
				t.Fatalf("windowResolve: (%s, %s), want (%s, %s)",
					percentOf(got), src, percentOf(tc.want), tc.source)
			}

			if tc.want == nil {
				return // пары без скидки в окне нет — строку не смотрим
			}
			row := p.Row()
			if row.Percent != *tc.want || row.Source != tc.source {
				t.Errorf("Row(): (%d, %s), want (%s, %s)",
					row.Percent, row.Source, percentOf(tc.want), tc.source)
			}
		})
	}
}

// TestActiveLotsIncludeManualTelegram — пара с ручной ТГ стоит в окне (его читают
// страница «Скидки» и правая колонка «Сроков»), с источником «ручная ТГ». Пара
// без скидки и замороженная ручным нулём в окно по-прежнему не попадают.
func TestActiveLotsIncludeManualTelegram(t *testing.T) {
	uc := NewUseCase(nil, nil, nil, nil, nil, nil, nil)
	uc.reg.Replace([]PairState{
		regPair("p-tg", "Стейк", day(15), tgManualOpt(30)),
		regPair("p-expiry", "Сыр", day(5), expiryOpt()),
		regPair("p-none", "Хлеб", day(20)),
		regPair("p-frozen", "Колбаса", day(3), manualOpt(0), tgManualOpt(30)),
	})

	got := lotKeys(uc.ActiveLots(12))
	want := []string{
		"p-tg@" + day(15).Format("02.01"),
		"p-expiry@" + day(5).Format("02.01"),
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("окно: %v, want %v", got, want)
	}

	rows := uc.reg.activeLocked()
	if len(rows) != 2 {
		t.Fatalf("строк окна %d, want 2 (%+v)", len(rows), rows)
	}
	if rows[0].ProductID != "p-tg" || rows[0].Percent != 30 || rows[0].Source != discounts.SourceTelegramManual {
		t.Errorf("строка ручной ТГ: %+v", rows[0])
	}
}

// TestRaiseWritesManualTelegram — подъём 16:00 доводит ручную ТГ до сайта теми же
// правилами, что и позиции рассылки, в том числе когда в рассылку пара не попала
// (скидку поставили уже после составления списка).
func TestRaiseWritesManualTelegram(t *testing.T) {
	t.Run("вне рассылки: сайт получает значение ручной ТГ", func(t *testing.T) {
		pairs := []PairState{regPair("p1", "Стейк", day(15), tgManualOpt(30))}

		raised, writes := raiseWrites(pairs, nil)
		if len(raised) != 1 || raised[0].ProductID != "p1" {
			t.Fatalf("поднятые: %v", raised)
		}
		if len(writes) != 1 || writes[0].General == nil || *writes[0].General != 30 {
			t.Fatalf("правки: %+v", writes)
		}
		if writes[0].Source != discounts.ReasonManual {
			t.Errorf("источник %q, want %q", writes[0].Source, discounts.ReasonManual)
		}
		if pairs[0].AppliedPlain == nil || *pairs[0].AppliedPlain != 30 {
			t.Errorf("снапшот пары не обновлён: %s", percentOf(pairs[0].AppliedPlain))
		}
	})

	t.Run("в рассылке: цель — ручная ТГ, а не план", func(t *testing.T) {
		pairs := []PairState{regPair("p1", "Стейк", day(9), tgManualOpt(30), telegramOpt(20))}
		plan := []discounts.SlotItem{{
			LotKey:  pairs[0].Key,
			Percent: 20,
			Reason:  discounts.ReasonExpiry,
		}}

		_, writes := raiseWrites(pairs, plan)
		if len(writes) != 1 || writes[0].General == nil || *writes[0].General != 30 {
			t.Fatalf("правки: %+v", writes)
		}
		if writes[0].Telegram == nil || *writes[0].Telegram != 20 {
			t.Errorf("ТГ-колонка плана потеряна: %s", percentOf(writes[0].Telegram))
		}
	})

	t.Run("сайт уже не ниже цели — не понижаем", func(t *testing.T) {
		pairs := []PairState{regPair("p1", "Стейк", day(15), tgManualOpt(30), appliedOpt(30))}

		if raised, writes := raiseWrites(pairs, nil); len(raised) != 0 || len(writes) != 0 {
			t.Fatalf("поднятые %v, правки %+v — ожидалось пусто", raised, writes)
		}
	})

	t.Run("ручная сайта важнее: заморозка ручную ТГ не поднимает", func(t *testing.T) {
		pairs := []PairState{regPair("p1", "Стейк", day(15), tgManualOpt(30), manualOpt(0))}

		if raised, _ := raiseWrites(pairs, nil); len(raised) != 0 {
			t.Fatalf("поднятые %v — ожидалось пусто", raised)
		}
	})
}

// TestEvaluateFillsTelegramManual — ручная ТГ из лота доезжает до состояния пары:
// до правки поле не заполнялось, и расчёт её не видел (см. TelegramManual).
func TestEvaluateFillsTelegramManual(t *testing.T) {
	in := discounts.Input{ProductID: "p1", Name: "Стейк", BestBefore: day(15), Qty: 5}
	in.TelegramManual = new(int16(30))

	pairs := Evaluate([]discounts.Input{in}, nil, testDay)
	if len(pairs) != 1 {
		t.Fatalf("пар %d, want 1", len(pairs))
	}

	percent, src := pairs[0].windowResolve()
	if percent == nil || *percent != 30 || src != discounts.SourceTelegramManual {
		t.Fatalf("окно пары: (%s, %s), want (30, %s)", percentOf(percent), src, discounts.SourceTelegramManual)
	}
}

// TestDayPlanForcesManualTelegram — ручная ТГ-скидка идёт в рассылку
// ПРИНУДИТЕЛЬНО: своим значением, первой в слоте, вне зависимости от ёмкости,
// антидубля прошлой рассылки и порога «успеет продаться» (решение владельца
// 19.10.2026). Заморозка сайта ручным нулём сильнее: такую пару движок не трогает.
func TestDayPlanForcesManualTelegram(t *testing.T) {
	t.Run("только ручная ТГ — позиция слота", func(t *testing.T) {
		p := regPair("p1", "Стейк", day(9), tgManualOpt(30))

		plan := buildDayPlan([]PairState{p}, nil, 10)
		if len(plan.slot) != 1 || len(plan.extra) != 0 {
			t.Fatalf("план: слот %d, лишние %d, want 1/0", len(plan.slot), len(plan.extra))
		}
		s := plan.slot[0]
		if s.percent != 30 || s.reason != discounts.ReasonManual {
			t.Errorf("позиция %d%% (%s), want 30%% (manual)", s.percent, s.reason)
		}
		if s.writeTelegram {
			t.Error("ТГ-колонку писать незачем: ручное значение уже стоит в ней")
		}
		if !s.tgManual {
			t.Error("метка tgManual не поставлена: склад не увидит (ТГ)")
		}
	})

	t.Run("ручная ТГ важнее ступени по сроку", func(t *testing.T) {
		expiry := int16(slotMainPercent)
		p := regPair("p1", "Стейк", day(9), tgManualOpt(30), func(p *PairState) { p.Expiry = &expiry })

		plan := buildDayPlan([]PairState{p}, nil, 10)
		if len(plan.slot) != 1 {
			t.Fatalf("слот %d, want 1", len(plan.slot))
		}
		if got := plan.slot[0].percent; got != 30 {
			t.Errorf("скидка позиции %d, want 30 (ручная ТГ, а не ступень %d)", got, expiry)
		}
	})

	t.Run("ноль в ТГ — не скидка", func(t *testing.T) {
		p := regPair("p1", "Стейк", day(9), tgManualOpt(0))

		if plan := buildDayPlan([]PairState{p}, nil, 10); len(plan.slot) != 0 {
			t.Errorf("слот %d, want 0", len(plan.slot))
		}
	})

	t.Run("заморозка сайта ручным нулём важнее", func(t *testing.T) {
		p := regPair("p1", "Стейк", day(9), tgManualOpt(30), manualOpt(0))

		if plan := buildDayPlan([]PairState{p}, nil, 10); len(plan.slot) != 0 {
			t.Errorf("слот %d, want 0", len(plan.slot))
		}
	})

	t.Run("антидубль прошлой рассылки не снимает", func(t *testing.T) {
		p := regPair("p1", "Стейк", day(9), tgManualOpt(30))
		prev := map[discounts.LotKey]struct{}{p.Key: {}}

		if plan := buildDayPlan([]PairState{p}, prev, 10); len(plan.slot) != 1 {
			t.Errorf("слот %d, want 1 (ручная ТГ сильнее антидубля)", len(plan.slot))
		}
	})

	t.Run("ёмкость не режет ручную ТГ", func(t *testing.T) {
		expiry := int16(30)
		pairs := []PairState{
			regPair("p1", "Стейк", day(9), tgManualOpt(30)),
			regPair("p2", "Сыр", day(9), tgManualOpt(20)),
			regPair("p3", "Хлеб", day(9), func(p *PairState) { p.Expiry = &expiry }),
		}

		plan := buildDayPlan(pairs, nil, 1)
		if len(plan.slot) != 2 {
			t.Fatalf("слот %d, want 2 (обе ручные ТГ, ёмкость 1 их не режет)", len(plan.slot))
		}
		for _, s := range plan.slot {
			if !s.tgManual {
				t.Errorf("в слоте не ручная ТГ: %+v", s)
			}
		}
		if len(plan.extra) != 1 || plan.extra[0].pair.ProductID != "p3" {
			t.Errorf("лишние %+v, want p3", plan.extra)
		}
	})

	t.Run("порог дней не применяется", func(t *testing.T) {
		p := regPair("p1", "Стейк", day(1), tgManualOpt(30)) // успеет ли продаться — не важно

		if plan := buildDayPlan([]PairState{p}, nil, 10); len(plan.slot) != 1 {
			t.Errorf("слот %d, want 1", len(plan.slot))
		}
	})
}

// TestWindowRowsPrintForcedManualTelegram — пара только с ручной ТГ печатается в
// письме складу, хотя расчётного кандидата у неё нет: иначе склад не увидит
// скидку, которую поставил менеджер.
func TestWindowRowsPrintForcedManualTelegram(t *testing.T) {
	uc := NewUseCase(nil, nil, nil, nil, nil, nil, nil)
	pair := regPair("p-tg", "Стейк", day(9), tgManualOpt(30))
	pairs := []PairState{pair}

	rows := uc.windowRows(pairs, buildDayPlan(pairs, nil, 10).slot)
	if len(rows) != 1 {
		t.Fatalf("строк %d, want 1", len(rows))
	}
	if rows[0].Percent != 30 || !rows[0].Telegram {
		t.Errorf("строка %+v, want 30%% с меткой ТГ", rows[0])
	}
}

// TestRaiseKeepsManualOwnerForForcedPlan — план стоит ровно ручной ТГ, и подъём
// 16:00 ставит сайту её значение владельцем «человек», а не «срок»: иначе
// часовой пересчёт снял бы значение как ступень по сроку.
func TestRaiseKeepsManualOwnerForForcedPlan(t *testing.T) {
	pairs := []PairState{regPair("p1", "Стейк", day(9), tgManualOpt(30))}
	plan := []discounts.SlotItem{{
		LotKey:  pairs[0].Key,
		Percent: 30,
		Reason:  discounts.ReasonManual,
	}}

	_, writes := raiseWrites(pairs, plan)
	if len(writes) != 1 || writes[0].General == nil || *writes[0].General != 30 {
		t.Fatalf("правки: %+v", writes)
	}
	if writes[0].GeneralOwner != discounts.OwnerManual.String() {
		t.Errorf("владелец %q, want %q", writes[0].GeneralOwner, discounts.OwnerManual.String())
	}
}

// TestRunSlotPlanForcesManualTelegram — сквозной путь 14:00: пара только с ручной
// ТГ попадает в письмо складу своим значением, ТГ-колонка при этом не пишется
// (значение уже в ручной колонке).
func TestRunSlotPlanForcesManualTelegram(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Стейк", day(9), 100, telegramManualInput(30)),
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 10); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	if len(h.writer.batches) != 0 {
		t.Errorf("правки %+v, want пусто (значение уже в ручной колонке)", h.writer.batches)
	}
	if len(h.repo.items) != 1 || len(h.repo.items[0]) != 1 {
		t.Fatalf("история рассылки: %+v, want одна позиция", h.repo.items)
	}
	it := h.repo.items[0][0]
	if it.Percent != 30 || it.Reason != discounts.ReasonManual {
		t.Errorf("позиция истории %+v, want 30%% manual", it)
	}
	if len(h.chat.texts) != 1 {
		t.Fatalf("сообщения складу: %q, want одно", h.chat.texts)
	}
	if text := h.chat.texts[0]; !strings.Contains(text, "(ТГ) Стейк") || !strings.Contains(text, "— 30%") {
		t.Errorf("сообщение складу: %q, want строку (ТГ) … — 30%%", text)
	}
}

package usecase

import (
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

// Метка пары с ручной ТГ в отчёте и на «Скидках» — «Ручная ТГ»: она не «ТГ» от
// рассылки, но и не сайтовая ручная (решение владельца 29.09.2026).
func TestChannelLabelManualTelegram(t *testing.T) {
	row := discounts.Row{Source: discounts.SourceTelegramManual, Percent: 30}
	if got := discounts.ChannelLabel(row); got != "Ручная ТГ" {
		t.Errorf("метка %q, want %q", got, "Ручная ТГ")
	}
}

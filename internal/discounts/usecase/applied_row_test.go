package usecase

import (
	"testing"

	"warehouseHelper/internal/discounts"
)

// tbPair — Ти-Бон как в проде 26.11: постоянная 5 % в карточке, ручная сайта
// 30 %, ручная ТГ 40 %. telegramPlain — значение ТГ-колонки (его пишет план
// 14:00): до рассылки nil.
func tbPair(telegramPlain *int16) PairState {
	bb := testDay.AddDate(0, 0, 11)
	manual := int16(30)
	tg := int16(40)
	perm := int16(5)

	return PairState{
		Key:            discounts.LotKey{ProductID: "tb", BestBefore: bb},
		ProductID:      "tb",
		Name:           "Ти-Бон",
		BestBefore:     bb,
		Qty:            16,
		DaysLeft:       11,
		Manual:         &manual,
		TelegramManual: &tg,
		Permanent:      &perm,
		Applied:        &manual,
		TelegramPlain:  telegramPlain,
	}
}

// До рассылки актуальные показывают сайт: 30 % ручная, а не 40 % из ТГ.
func TestAppliedRowBeforeMailingShowsSite(t *testing.T) {
	row := tbPair(nil).AppliedRow()
	if row.Percent != 30 || row.Source != discounts.SourceManual {
		t.Fatalf("до рассылки %d %s, want 30 manual", row.Percent, row.Source)
	}
}

// После рассылки — значение подписчиков с меткой ТГ, а не «постоянная».
func TestAppliedRowAfterMailingShowsTelegram(t *testing.T) {
	plain := int16(40)
	row := tbPair(&plain).AppliedRow()
	if row.Percent != 40 || row.Source != discounts.SourceTelegramManual {
		t.Fatalf("после рассылки %d %s, want 40 manual_tg", row.Percent, row.Source)
	}
}

// Ручная ТГ занимает слот: с постоянной в карточке строка не «постоянная» —
// иначе товар печатался дважды (07.12 и 28.12).
func TestManualTelegramTakesSlotOverPermanent(t *testing.T) {
	if tbPair(nil).Row().Permanent {
		t.Fatal("ручная ТГ с постоянной в карточке помечена «постоянной»: слот не занят")
	}
}

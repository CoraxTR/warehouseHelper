package usecase

import (
	"context"
	"testing"
	"time"
)

// TestSetPermanentDiscount — проверки ввода постоянной скидки поставщика
// (03.11.2026): без даты окончания скидка бессрочна, дата в прошлом уже
// ничего не значит, процент вне 1..90 — опечатка. Пустая пара — снятие.
func TestSetPermanentDiscount(t *testing.T) {
	t.Parallel()

	day := func(shift int) *time.Time {
		d := dateOnlyMSK(time.Now()).AddDate(0, 0, shift)

		return &d
	}
	pct := func(v int16) *int16 { return &v }

	tests := []struct {
		name    string
		percent *int16
		until   *time.Time
		wantErr bool
		// wantClear — ожидается снятие (NULL/NULL в хранилище).
		wantClear bool
	}{
		{name: "снять скидку", wantClear: true},
		{name: "процент без даты", percent: pct(20), wantErr: true},
		{name: "дата без процента", until: day(3), wantErr: true},
		{name: "дата в прошлом", percent: pct(20), until: day(-1), wantErr: true},
		{name: "процент 0", percent: pct(0), until: day(3), wantErr: true},
		{name: "процент 91", percent: pct(91), until: day(3), wantErr: true},
		{name: "сегодня — ещё действует", percent: pct(20), until: day(0)},
		{name: "нормальный ввод", percent: pct(15), until: day(10)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &stubProductsRepo{}
			uc := NewGoodsUseCase(nil, nil, repo, nil)

			err := uc.SetPermanentDiscount(context.Background(), "p-1", tt.percent, tt.until)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ожидалась ошибка валидации, её нет")
				}

				return
			}
			if err != nil {
				t.Fatalf("SetPermanentDiscount: %v", err)
			}
			if tt.wantClear {
				if repo.permPercents["p-1"] != nil || repo.permUntils["p-1"] != nil {
					t.Fatalf("ожидалось снятие, записано: %v / %v", repo.permPercents["p-1"], repo.permUntils["p-1"])
				}

				return
			}
			if got := repo.permPercents["p-1"]; got == nil || *got != *tt.percent {
				t.Fatalf("процент записан как %v, ждали %d", got, *tt.percent)
			}
			if got := repo.permUntils["p-1"]; got == nil || !got.Equal(*tt.until) {
				t.Fatalf("дата записана как %v, ждали %v", got, *tt.until)
			}
		})
	}
}

// TestPermanentDiscountGetter — окно карточки получает сохранённую пару.
func TestPermanentDiscountGetter(t *testing.T) {
	t.Parallel()

	percent := int16(12)
	until := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	repo := &stubProductsRepo{permGetPercent: &percent, permGetUntil: &until}
	uc := NewGoodsUseCase(nil, nil, repo, nil)

	gotPercent, gotUntil, err := uc.PermanentDiscount(context.Background(), "p-1")
	if err != nil {
		t.Fatalf("PermanentDiscount: %v", err)
	}
	if gotPercent == nil || *gotPercent != percent {
		t.Fatalf("процент: %v, ждали %d", gotPercent, percent)
	}
	if gotUntil == nil || !gotUntil.Equal(until) {
		t.Fatalf("дата: %v, ждали %v", gotUntil, until)
	}
}

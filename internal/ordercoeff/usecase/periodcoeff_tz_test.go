package usecase

import (
	"context"
	"testing"
	"time"

	"warehouseHelper/internal/ordercoeff"
)

// Ключи периодов в БД — даты в UTC, а часы модуля живут по местному времени.
// Без приведения к UTC ключ уезжает на смещение зоны, карта по ключу-времени
// пустеет, и отчёт теряет блок «избыток» целиком.
func TestPeriodCoefficientsAsksUTCDateKeys(t *testing.T) {
	msk := time.FixedZone("МСК", 3*60*60)
	now := time.Date(2026, time.November, 17, 16, 0, 0, 0, msk)

	repo := &periodRepo{stubRepo: &stubRepo{}, weekly: map[string]int16{"p1": 3}}
	uc := NewUseCase(repo, &stubProducts{weekly: true})

	got, err := uc.PeriodCoefficients(context.Background(), []string{"p1"}, now)
	if err != nil {
		t.Fatalf("PeriodCoefficients: %v", err)
	}
	if got["p1"] != 3 {
		t.Errorf("коэффициент = %d, want 3", got["p1"])
	}

	want := time.Date(2026, time.November, 16, 0, 0, 0, 0, time.UTC)
	if s := repo.starts[ordercoeff.PeriodWeek]; len(s) != 1 || !s[0].Equal(want) {
		t.Fatalf("неделя запрошена как %v, want %v", s, want)
	}
}

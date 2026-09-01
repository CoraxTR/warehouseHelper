package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/ordercoeff"
)

// stubRepo — заглушка хранилища коэффициентов: записывает вызовы, ошибку отдаёт по err.
type stubRepo struct {
	applied bool
	err     error
	coeffs  map[time.Time]int16

	applyCalls []coeffApplyCall
	coeffCalls int
}

type coeffApplyCall struct {
	productID  string
	periodType ordercoeff.PeriodType
	at         time.Time
	ev         ordercoeff.EventType
}

func (r *stubRepo) ApplyCoeffEvent(_ context.Context, productID string, periodType ordercoeff.PeriodType, at time.Time, ev ordercoeff.EventType) (bool, error) {
	r.applyCalls = append(r.applyCalls, coeffApplyCall{productID, periodType, at, ev})
	if r.err != nil {
		return false, r.err
	}
	return r.applied, nil
}

func (r *stubRepo) Coefficients(_ context.Context, _ string, periodType ordercoeff.PeriodType, intervals []time.Time) (map[time.Time]int16, error) {
	r.coeffCalls++
	if r.err != nil {
		return nil, r.err
	}
	return r.coeffs, nil
}

// stubProducts — заглушка каталога: недельность и ошибка.
type stubProducts struct {
	weekly bool
	err    error
}

func (p *stubProducts) TrackWeekly(_ context.Context, _ string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.weekly, nil
}

var testAt = time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
var testStart = time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)

func TestSoldOutWeeklyAndMonthly(t *testing.T) {
	cases := []struct {
		name   string
		weekly bool
		want   ordercoeff.PeriodType
	}{
		{"недельный товар → PeriodWeek", true, ordercoeff.PeriodWeek},
		{"месячный товар → PeriodMonth", false, ordercoeff.PeriodMonth},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepo{applied: true}
			uc := NewUseCase(repo, &stubProducts{weekly: tc.weekly})

			if err := uc.SoldOut(context.Background(), "p1", testAt); err != nil {
				t.Fatalf("SoldOut: %v", err)
			}
			if len(repo.applyCalls) != 1 {
				t.Fatalf("вызовов ApplyCoeffEvent = %d, want 1", len(repo.applyCalls))
			}
			got := repo.applyCalls[0]
			if got.periodType != tc.want {
				t.Errorf("periodType = %v, want %v", got.periodType, tc.want)
			}
			if got.ev != ordercoeff.EventSoldOut {
				t.Errorf("ev = %v, want EventSoldOut", got.ev)
			}
			if got.productID != "p1" || !got.at.Equal(testAt) {
				t.Errorf("productID/at = %q/%v, want p1/%v", got.productID, got.at, testAt)
			}
		})
	}
}

func TestEventMethodsMapToEventTypes(t *testing.T) {
	cases := []struct {
		name string
		call func(uc *UseCase) error
		want ordercoeff.EventType
	}{
		{"Discount", func(uc *UseCase) error { return uc.Discount(context.Background(), "p1", testAt) }, ordercoeff.EventDiscount},
		{"Frozen", func(uc *UseCase) error { return uc.Frozen(context.Background(), "p1", testAt) }, ordercoeff.EventFrozen},
		{"Unavailable", func(uc *UseCase) error { return uc.Unavailable(context.Background(), "p1", testAt) }, ordercoeff.EventUnavailable},
		{"RollbackSoldOut", func(uc *UseCase) error { _, err := uc.RollbackSoldOut(context.Background(), "p1", testAt); return err }, ordercoeff.EventRollbackSoldOut},
		{"RollbackDiscount", func(uc *UseCase) error { _, err := uc.RollbackDiscount(context.Background(), "p1", testAt); return err }, ordercoeff.EventRollbackDiscount},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepo{applied: true}
			uc := NewUseCase(repo, &stubProducts{weekly: true})

			if err := tc.call(uc); err != nil {
				t.Fatalf("вызов: %v", err)
			}
			if got := repo.applyCalls[0].ev; got != tc.want {
				t.Errorf("ev = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRollbackReturnsApplied(t *testing.T) {
	for _, applied := range []bool{true, false} {
		repo := &stubRepo{applied: applied}
		uc := NewUseCase(repo, &stubProducts{weekly: true})

		got, err := uc.RollbackSoldOut(context.Background(), "p1", testAt)
		if err != nil {
			t.Fatalf("RollbackSoldOut: %v", err)
		}
		if got != applied {
			t.Errorf("applied = %v, want %v", got, applied)
		}
	}
}

func TestErrorsPropagate(t *testing.T) {
	repoErr := errors.New("repo fail")
	catalogErr := errors.New("catalog fail")

	t.Run("ошибка каталога", func(t *testing.T) {
		uc := NewUseCase(&stubRepo{}, &stubProducts{err: catalogErr})
		if err := uc.SoldOut(context.Background(), "p1", testAt); !errors.Is(err, catalogErr) {
			t.Errorf("SoldOut err = %v, want catalogErr", err)
		}
	})

	t.Run("ошибка хранилища", func(t *testing.T) {
		uc := NewUseCase(&stubRepo{err: repoErr}, &stubProducts{weekly: true})
		if err := uc.Discount(context.Background(), "p1", testAt); !errors.Is(err, repoErr) {
			t.Errorf("Discount err = %v, want repoErr", err)
		}
	})
}

func TestCoefficientsPassThrough(t *testing.T) {
	repo := &stubRepo{coeffs: map[time.Time]int16{testStart: 2}}
	uc := NewUseCase(repo, &stubProducts{weekly: true})

	got, err := uc.Coefficients(context.Background(), "p1", ordercoeff.PeriodWeek, []time.Time{testStart})
	if err != nil {
		t.Fatalf("Coefficients: %v", err)
	}
	if got[testStart] != 2 {
		t.Errorf("coeff[start] = %d, want 2", got[testStart])
	}
	if repo.coeffCalls != 1 {
		t.Errorf("вызовов Coefficients = %d, want 1", repo.coeffCalls)
	}
}

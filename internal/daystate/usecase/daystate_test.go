package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"warehouseHelper/internal/daystate"
)

func i16(v int16) *int16 { return &v }

func b(v bool) *bool { return &v }

func day(d int) time.Time {
	return time.Date(2026, time.September, d, 0, 0, 0, 0, time.UTC)
}

func key(productID string, date time.Time) string {
	return productID + "|" + date.Format(time.DateOnly)
}

// fakeRepo — хранилище-заглушка: строки в map, счётчики вызовов.
type fakeRepo struct {
	days            map[string]*daystate.DayState
	lots            map[string][]daystate.LotState
	snapDone        bool
	snapInsertCalls int
	snapDoneCalls   int
	ensured         []daystate.DayState
	updated         []daystate.DayState
	orderableCalls  []orderableCall
	cleared         []string
	err             error
}

type orderableCall struct {
	productID string
	dates     []time.Time
	orderable bool
}

func (f *fakeRepo) EnsureDay(_ context.Context, d daystate.DayState) error {
	if f.err != nil {
		return f.err
	}
	f.ensured = append(f.ensured, d)
	k := key(d.ProductID, d.Date)
	if _, ok := f.days[k]; !ok {
		f.days[k] = &d
	}
	return nil
}

func (f *fakeRepo) GetDay(_ context.Context, productID string, date time.Time) (*daystate.DayState, error) {
	if f.err != nil {
		return nil, f.err
	}
	d, ok := f.days[key(productID, date)]
	if !ok {
		return nil, daystate.ErrDayNotFound
	}
	cp := *d
	return &cp, nil
}

func (f *fakeRepo) UpdateDay(_ context.Context, d daystate.DayState) error {
	if f.err != nil {
		return f.err
	}
	f.updated = append(f.updated, d)
	k := key(d.ProductID, d.Date)
	if _, ok := f.days[k]; !ok {
		return daystate.ErrDayNotFound
	}
	f.days[k] = &d
	return nil
}

func (f *fakeRepo) SetOrderable(_ context.Context, productID string, dates []time.Time, orderable bool) error {
	if f.err != nil {
		return f.err
	}
	f.orderableCalls = append(f.orderableCalls, orderableCall{productID, dates, orderable})
	return nil
}

func (f *fakeRepo) SnapshotDone(_ context.Context, _ time.Time) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.snapDoneCalls++
	return f.snapDone, nil
}

func (f *fakeRepo) SnapshotInsert(_ context.Context, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.snapInsertCalls++
	return nil
}

func (f *fakeRepo) LotsSnapshot(_ context.Context, productID string) ([]daystate.LotState, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.lots[productID], nil
}

func (f *fakeRepo) ClearSoldOut(_ context.Context, productID string, date time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.cleared = append(f.cleared, key(productID, date))
	return nil
}

// fakeSoldOut — получатель SoldOut.
type fakeSoldOut struct {
	calls []string
	err   error
}

func (f *fakeSoldOut) SoldOut(_ context.Context, productID string, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, productID)
	return nil
}

// fakeUnavailable — получатель Unavailable.
type fakeUnavailable struct {
	calls []string
	err   error
}

func (f *fakeUnavailable) Unavailable(_ context.Context, productID string, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, productID)
	return nil
}

// fakeRollback — получатель отката SoldOut.
type fakeRollback struct {
	calls []string
	err   error
}

func (f *fakeRollback) RollbackSoldOut(_ context.Context, productID string, _ time.Time) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.calls = append(f.calls, productID)
	return true, nil
}

func newTestUC(repo Repository, soldOut SoldOutNotifier, unavailable UnavailableNotifier, rollback SoldOutRollbackNotifier, now time.Time) *UseCase {
	uc := NewUseCase(repo, soldOut, unavailable, rollback)
	uc.now = func() time.Time { return now }
	return uc
}

func TestOnStockChanged_CreatesRowAndUpdates(t *testing.T) {
	today := day(1)
	repo := &fakeRepo{days: map[string]*daystate.DayState{}, lots: map[string][]daystate.LotState{
		"p1": {{Qty: 5, EffectiveGeneral: i16(7)}},
	}}
	soldOut := &fakeSoldOut{}
	uc := newTestUC(repo, soldOut, &fakeUnavailable{}, &fakeRollback{}, today)

	if err := uc.OnStockChanged(context.Background(), "p1"); err != nil {
		t.Fatalf("OnStockChanged: %v", err)
	}

	// Строка создана со снимком (in_stock, discount_start=discount).
	if len(repo.ensured) != 1 {
		t.Fatalf("ensured: len = %d, want 1", len(repo.ensured))
	}
	e := repo.ensured[0]
	if e.InStock == nil || !*e.InStock {
		t.Errorf("ensured.InStock = %v, want true", e.InStock)
	}
	if e.DiscountStart == nil || *e.DiscountStart != 7 || e.Discount == nil || *e.Discount != 7 {
		t.Errorf("ensured скидки = %v/%v, want 7/7", e.DiscountStart, e.Discount)
	}
	if !e.Orderable {
		t.Error("ensured.Orderable = false, want true (default)")
	}

	// Пересчёт записан.
	if len(repo.updated) != 1 {
		t.Fatalf("updated: len = %d, want 1", len(repo.updated))
	}
	u := repo.updated[0]
	if u.InStock == nil || !*u.InStock || u.Discount == nil || *u.Discount != 7 {
		t.Errorf("updated: %+v", u)
	}

	// Перехода в ноль не было — эмита нет.
	if len(soldOut.calls) != 0 {
		t.Errorf("SoldOut вызван: %v, want нет", soldOut.calls)
	}
}

func TestOnStockChanged_SoldOutTransition(t *testing.T) {
	today := day(1)
	repo := &fakeRepo{
		days: map[string]*daystate.DayState{
			key("p1", today): {ProductID: "p1", Date: today, InStock: b(true), Discount: i16(5), Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": {{Qty: 0}}},
	}
	soldOut := &fakeSoldOut{}
	uc := newTestUC(repo, soldOut, &fakeUnavailable{}, &fakeRollback{}, today)

	if err := uc.OnStockChanged(context.Background(), "p1"); err != nil {
		t.Fatalf("OnStockChanged: %v", err)
	}

	u := repo.updated[0]
	if !u.SoldOutToday {
		t.Error("SoldOutToday = false, want true")
	}
	if u.InStock == nil || *u.InStock {
		t.Errorf("InStock = %v, want false", u.InStock)
	}
	if len(soldOut.calls) != 1 || soldOut.calls[0] != "p1" {
		t.Errorf("SoldOut: %v, want [p1]", soldOut.calls)
	}
}

func TestOnStockChanged_NoEmitWhenAlreadyOut(t *testing.T) {
	today := day(1)
	repo := &fakeRepo{
		days: map[string]*daystate.DayState{
			key("p1", today): {ProductID: "p1", Date: today, InStock: b(false), SoldOutToday: true, Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": nil},
	}
	soldOut := &fakeSoldOut{}
	uc := newTestUC(repo, soldOut, &fakeUnavailable{}, &fakeRollback{}, today)

	if err := uc.OnStockChanged(context.Background(), "p1"); err != nil {
		t.Fatalf("OnStockChanged: %v", err)
	}
	if len(soldOut.calls) != 0 {
		t.Errorf("SoldOut: %v, want нет", soldOut.calls)
	}
	if !repo.updated[0].SoldOutToday {
		t.Error("маркер дня должен сохраниться")
	}
}

func TestOnStockChanged_DiscountIncreaseAppends(t *testing.T) {
	today := day(1)
	repo := &fakeRepo{
		days: map[string]*daystate.DayState{
			key("p1", today): {ProductID: "p1", Date: today, InStock: b(true), Discount: i16(5), DiscountIncreases: []int16{5}, Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": {{Qty: 1, EffectiveGeneral: i16(15)}}},
	}
	uc := newTestUC(repo, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, today)

	if err := uc.OnStockChanged(context.Background(), "p1"); err != nil {
		t.Fatalf("OnStockChanged: %v", err)
	}
	u := repo.updated[0]
	if u.Discount == nil || *u.Discount != 15 {
		t.Errorf("Discount = %v, want 15", u.Discount)
	}
	if !reflect.DeepEqual(u.DiscountIncreases, []int16{5, 15}) {
		t.Errorf("Increases = %v, want [5 15]", u.DiscountIncreases)
	}
}

func TestSetOrderable(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}}
	unavailable := &fakeUnavailable{}
	uc := newTestUC(repo, &fakeSoldOut{}, unavailable, &fakeRollback{}, day(1))

	d1 := day(10)
	d2 := day(11)

	// Недоступность → батч без дубликатов + эмит на каждую дату.
	if err := uc.SetUnavailable(context.Background(), "p1", []time.Time{d1, d2, d1}); err != nil {
		t.Fatalf("SetUnavailable: %v", err)
	}
	if len(repo.orderableCalls) != 1 {
		t.Fatalf("orderableCalls: len = %d, want 1", len(repo.orderableCalls))
	}
	oc := repo.orderableCalls[0]
	if oc.productID != "p1" || oc.orderable {
		t.Errorf("call = %+v, want p1/false", oc)
	}
	if len(oc.dates) != 2 || !oc.dates[0].Equal(d1) || !oc.dates[1].Equal(d2) {
		t.Errorf("dates = %v, want [%s %s] без дубликатов", oc.dates, d1.Format(time.DateOnly), d2.Format(time.DateOnly))
	}
	if len(unavailable.calls) != 2 {
		t.Errorf("Unavailable: %v, want 2 вызова", unavailable.calls)
	}

	// Доступность → без эмитов.
	unavailable.calls = nil
	if err := uc.SetOrderable(context.Background(), "p1", []time.Time{d1}); err != nil {
		t.Fatalf("SetOrderable: %v", err)
	}
	if len(unavailable.calls) != 0 {
		t.Errorf("Unavailable при true: %v, want нет", unavailable.calls)
	}
}

func TestSetOrderable_Validation(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}}
	uc := newTestUC(repo, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, day(1))

	if err := uc.SetUnavailable(context.Background(), "", []time.Time{day(10)}); err == nil {
		t.Error("пустой товар: ожидалась ошибка")
	}
	if err := uc.SetUnavailable(context.Background(), "p1", nil); err == nil {
		t.Error("пустые даты: ожидалась ошибка")
	}
}

func TestRollbackSoldOut(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}}
	rollback := &fakeRollback{}
	uc := newTestUC(repo, &fakeSoldOut{}, &fakeUnavailable{}, rollback, day(1))

	at := day(1)
	if err := uc.RollbackSoldOut(context.Background(), "p1", at); err != nil {
		t.Fatalf("RollbackSoldOut: %v", err)
	}
	if len(repo.cleared) != 1 || repo.cleared[0] != key("p1", at) {
		t.Errorf("cleared = %v, want [%s]", repo.cleared, key("p1", at))
	}
	if len(rollback.calls) != 1 || rollback.calls[0] != "p1" {
		t.Errorf("rollback: %v, want [p1]", rollback.calls)
	}
}

func TestRollbackSoldOut_NotifierError(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}}
	rollback := &fakeRollback{err: errors.New("coeff down")}
	uc := newTestUC(repo, &fakeSoldOut{}, &fakeUnavailable{}, rollback, day(1))

	if err := uc.RollbackSoldOut(context.Background(), "p1", day(1)); err == nil {
		t.Error("ошибка эмита должна вернуться вызывающему")
	}
}

func TestEnsureSnapshot(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}, snapDone: false}
	uc := newTestUC(repo, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, day(1))

	if err := uc.EnsureSnapshot(context.Background(), day(1)); err != nil {
		t.Fatalf("EnsureSnapshot: %v", err)
	}
	if repo.snapInsertCalls != 1 {
		t.Errorf("snapshot insert: %d, want 1", repo.snapInsertCalls)
	}

	// Уже сделан — повторный вызов ничего не пишет.
	repo.snapDone = true
	if err := uc.EnsureSnapshot(context.Background(), day(1)); err != nil {
		t.Fatalf("EnsureSnapshot: %v", err)
	}
	if repo.snapInsertCalls != 1 {
		t.Errorf("snapshot insert после done: %d, want 1 (идемпотентно)", repo.snapInsertCalls)
	}
}

// Тик снапшота: до времени снапшота — не проверяем, после — делаем.
func TestTrySnapshotTiming(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}, snapDone: false}
	uc := newTestUC(repo, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, day(1).Add(8*time.Hour))

	// 08:00, снапшот в 09:00 — рано.
	uc.trySnapshot(context.Background(), 9*time.Hour)
	if repo.snapDoneCalls != 0 {
		t.Errorf("в 08:00: SnapshotDone вызван %d раз, want 0", repo.snapDoneCalls)
	}

	// 09:05 — делаем.
	uc.now = func() time.Time { return day(1).Add(9*time.Hour + 5*time.Minute) }
	uc.trySnapshot(context.Background(), 9*time.Hour)
	if repo.snapDoneCalls != 1 || repo.snapInsertCalls != 1 {
		t.Errorf("в 09:05: done=%d insert=%d, want 1/1", repo.snapDoneCalls, repo.snapInsertCalls)
	}
}

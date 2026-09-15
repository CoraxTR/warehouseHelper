package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"warehouseHelper/internal/daystate"
)

//go:fix inline
func i16(v int16) *int16 { return new(v) }

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
	lastKnownCalls  int
	err             error
	lastKnownErr    error
}

// compile-check: заглушка обязана покрывать весь контракт хранилища.
var _ Repository = (*fakeRepo)(nil)

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

// LastKnownInStock повторяет SQL-контракт: ближайшая строка дня ДО before с
// заполненным in_stock; NULL-строки (календарь) пропускаются, истории нет — nil.
// Счётчик вызовов — чтобы тесты видели, что история читается только там, где
// состояние дня неизвестно (нет строки / строка от календаря).
func (f *fakeRepo) LastKnownInStock(_ context.Context, productID string, before time.Time) (*bool, error) {
	f.lastKnownCalls++
	if f.err != nil {
		return nil, f.err
	}
	if f.lastKnownErr != nil {
		return nil, f.lastKnownErr
	}

	var best *daystate.DayState
	for _, d := range f.days {
		if d.ProductID != productID || !d.Date.Before(before) || d.InStock == nil {
			continue
		}
		if best == nil || d.Date.After(best.Date) {
			best = d
		}
	}
	if best == nil {
		//nolint:nilnil // контракт репозитория: (nil, nil) = истории нет
		return nil, nil
	}
	v := *best.InStock

	return &v, nil
}

func (f *fakeRepo) ClearSoldOut(_ context.Context, productID string, date time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.cleared = append(f.cleared, key(productID, date))
	return nil
}

func (f *fakeRepo) ListByRange(_ context.Context, from, to time.Time) (map[string]map[time.Time]daystate.DayState, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]map[time.Time]daystate.DayState{}
	for _, d := range f.days {
		if d.Date.Before(from) || d.Date.After(to) {
			continue
		}
		byDate := out[d.ProductID]
		if byDate == nil {
			byDate = map[time.Time]daystate.DayState{}
			out[d.ProductID] = byDate
		}
		byDate[d.Date] = *d
	}
	return out, nil
}

// fakeCatalog — каталог-заглушка для страниц daystate.
type fakeCatalog struct {
	products []daystate.CatalogProduct
	err      error
}

func (f *fakeCatalog) CatalogProducts(_ context.Context) ([]daystate.CatalogProduct, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.products, nil
}

// fakeStockStatus — получатель уведомлений о смене наличия.
type fakeStockStatus struct {
	soldOut     []string
	backInStock []string
	err         error
}

func (f *fakeStockStatus) SoldOut(_ context.Context, productID string) error {
	if f.err != nil {
		return f.err
	}
	f.soldOut = append(f.soldOut, productID)
	return nil
}

func (f *fakeStockStatus) BackInStock(_ context.Context, productID string) error {
	if f.err != nil {
		return f.err
	}
	f.backInStock = append(f.backInStock, productID)
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

func newTestUC(repo Repository, catalog CatalogProvider, soldOut SoldOutNotifier, unavailable UnavailableNotifier, rollback SoldOutRollbackNotifier, stockStatus StockStatusNotifier, now time.Time) *UseCase {
	uc := NewUseCase(repo, catalog, soldOut, unavailable, rollback, stockStatus)
	uc.now = func() time.Time { return now }
	return uc
}

func TestOnStockChanged_CreatesRowAndUpdates(t *testing.T) {
	today := day(1)
	repo := &fakeRepo{days: map[string]*daystate.DayState{}, lots: map[string][]daystate.LotState{
		"p1": {{Qty: 5, EffectiveGeneral: i16(7)}},
	}}
	soldOut := &fakeSoldOut{}
	uc := newTestUC(repo, &fakeCatalog{}, soldOut, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, today)

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
			key("p1", today): {ProductID: "p1", Date: today, InStock: new(true), Discount: i16(5), Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": {{Qty: 0}}},
	}
	soldOut := &fakeSoldOut{}
	uc := newTestUC(repo, &fakeCatalog{}, soldOut, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, today)

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
			key("p1", today): {ProductID: "p1", Date: today, InStock: new(false), SoldOutToday: true, Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": nil},
	}
	soldOut := &fakeSoldOut{}
	uc := newTestUC(repo, &fakeCatalog{}, soldOut, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, today)

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
			key("p1", today): {ProductID: "p1", Date: today, InStock: new(true), Discount: i16(5), DiscountIncreases: []int16{5}, Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": {{Qty: 1, EffectiveGeneral: i16(15)}}},
	}
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, today)

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

// Уведомления о смене наличия: закончился и появился в течение дня.
func TestOnStockChangedNotifications(t *testing.T) {
	today := day(1)
	base := &fakeRepo{
		days: map[string]*daystate.DayState{
			key("p1", today): {ProductID: "p1", Date: today, InStock: new(true), Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": {{Qty: 0}}},
	}
	status := &fakeStockStatus{}
	uc := newTestUC(base, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, status, today)

	// Переход в ноль → уведомление «закончился».
	if err := uc.OnStockChanged(context.Background(), "p1"); err != nil {
		t.Fatalf("OnStockChanged: %v", err)
	}
	if len(status.soldOut) != 1 || status.soldOut[0] != "p1" {
		t.Errorf("soldOut = %v, want [p1]", status.soldOut)
	}
	if len(status.backInStock) != 0 {
		t.Errorf("backInStock = %v, want пусто", status.backInStock)
	}

	// Приход в тот же день → уведомление «появился», маркер не сброшен.
	base.lots["p1"] = []daystate.LotState{{Qty: 5}}
	if err := uc.OnStockChanged(context.Background(), "p1"); err != nil {
		t.Fatalf("OnStockChanged (приход): %v", err)
	}
	if len(status.backInStock) != 1 || status.backInStock[0] != "p1" {
		t.Errorf("backInStock = %v, want [p1]", status.backInStock)
	}
	if !base.days[key("p1", today)].SoldOutToday {
		t.Error("sold_out_today = false после прихода, want true")
	}
}

// Ошибка уведомления не роняет операцию стока (наблюдатель).
func TestOnStockChanged_NotifyError(t *testing.T) {
	today := day(1)
	repo := &fakeRepo{
		days: map[string]*daystate.DayState{
			key("p1", today): {ProductID: "p1", Date: today, InStock: new(true), Orderable: true},
		},
		lots: map[string][]daystate.LotState{"p1": {{Qty: 0}}},
	}
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{err: errors.New("tg down")}, today)

	if err := uc.OnStockChanged(context.Background(), "p1"); err != nil {
		t.Fatalf("OnStockChanged с ошибкой уведомления: %v", err)
	}
}

func TestSetOrderable(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}}
	unavailable := &fakeUnavailable{}
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, unavailable, &fakeRollback{}, &fakeStockStatus{}, day(1))

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
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, day(1))

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
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, rollback, &fakeStockStatus{}, day(1))

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
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, rollback, &fakeStockStatus{}, day(1))

	if err := uc.RollbackSoldOut(context.Background(), "p1", day(1)); err == nil {
		t.Error("ошибка эмита должна вернуться вызывающему")
	}
}

func TestEnsureSnapshot(t *testing.T) {
	repo := &fakeRepo{days: map[string]*daystate.DayState{}, snapDone: false}
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, day(1))

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
	uc := newTestUC(repo, &fakeCatalog{}, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, day(1).Add(8*time.Hour))

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

// Календарь доступности: строки дня переопределяют дефолт «доступна».
func TestAvailability(t *testing.T) {
	now := day(1)
	repo := &fakeRepo{days: map[string]*daystate.DayState{
		key("p1", day(10)): {ProductID: "p1", Date: day(10), Orderable: false},
		key("p2", day(10)): {ProductID: "p2", Date: day(10), Orderable: true},
	}}
	catalog := &fakeCatalog{products: []daystate.CatalogProduct{
		{ID: "p1", Name: "Хлеб", GroupName: "Хлебобулочные"},
		{ID: "p2", Name: "Молоко", GroupName: "Молочные"},
	}}
	uc := newTestUC(repo, catalog, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, now)

	page, err := uc.Availability(context.Background(), day(1))
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}
	if page.Days != 30 {
		t.Errorf("Days = %d, want 30 (сентябрь)", page.Days)
	}
	if len(page.Products) != 2 {
		t.Fatalf("товаров = %d, want 2", len(page.Products))
	}
	p1 := page.Products[0]
	if p1.ProductID != "p1" || p1.GroupName != "Хлебобулочные" {
		t.Errorf("p1 = %+v", p1)
	}
	if !p1.Orderable[0] {
		t.Error("день без строки должен быть доступен по умолчанию")
	}
	if p1.Orderable[9] {
		t.Error("день 10 у p1 должен быть недоступен (строка orderable=false)")
	}
	if !page.Products[1].Orderable[9] {
		t.Error("день 10 у p2 должен остаться доступным")
	}
}

// Отчёт по наличию: ячейки по правилам, отсутствующие строки — пустые.
func TestStockReport(t *testing.T) {
	now := day(15) // середина месяца: дни 1..2 уже прошли
	repo := &fakeRepo{days: map[string]*daystate.DayState{
		key("p1", day(1)): {ProductID: "p1", Date: day(1), InStock: new(true), Discount: i16(15), Orderable: true},
		key("p1", day(2)): {ProductID: "p1", Date: day(2), InStock: new(false), Orderable: true},
	}}
	catalog := &fakeCatalog{products: []daystate.CatalogProduct{
		{ID: "p1", Name: "Хлеб", GroupName: "Хлебобулочные"},
	}}
	uc := newTestUC(repo, catalog, &fakeSoldOut{}, &fakeUnavailable{}, &fakeRollback{}, &fakeStockStatus{}, now)

	page, err := uc.StockReport(context.Background(), day(1))
	if err != nil {
		t.Fatalf("StockReport: %v", err)
	}
	if len(page.Products) != 1 {
		t.Fatalf("товаров = %d, want 1", len(page.Products))
	}
	cells := page.Products[0].Cells
	if cells[0].Kind != daystate.CellYellow {
		t.Errorf("день 1: kind = %s, want yellow", cells[0].Kind)
	}
	if cells[1].Kind != daystate.CellRed {
		t.Errorf("день 2: kind = %s, want red", cells[1].Kind)
	}
	if cells[2].Kind != daystate.CellEmpty {
		t.Errorf("день 3 (нет строки): kind = %s, want empty", cells[2].Kind)
	}
}

// sameBoolPtr сравнивает два «необязательных» булевых (nil = «неизвестно»).
func sameBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// boolText печатает «необязательный» булев читаемо (в %v указатель даёт адрес).
func boolText(v *bool) string {
	switch {
	case v == nil:
		return "nil"
	case *v:
		return "true"
	default:
		return "false"
	}
}

// backInStockCase — сценарий теста истории наличия.
type backInStockCase struct {
	name             string
	days             map[string]*daystate.DayState
	lastKnownErr     error
	wantEnsured      int   // 1 — строку дня создавали (холодный путь), 0 — уже была
	wantSeed         *bool // ensured[0].InStock — «было» при создании строки
	wantLastKnown    int   // сколько раз читалась история наличия
	wantBackInStock  []string
	wantUpdated      *bool    // updated[0].InStock — пересчёт из лотов
	wantSoldOutToday bool     // маркер дня после пересчёта (сохраняется приходом)
	emptyLots        bool     // товар отсутствует (лотов нет)
	wantSoldOut      []string // уведомления «закончился» в общий канал
	wantSoldOutCalls []string // эмиты SoldOut в ordercoeff
	wantOrderable    *bool    // orderable строки дня после пересчёта (nil — не проверяем)
	wantErr          bool
}

// «Товар появился» определяется по истории наличия: у товара, который был в
// полном отсутствии, строки дня нет вовсе (лот, списанный до нуля, удаляется —
// снапшот дня его не видит), поэтому «было» берётся из последней известной
// строки. В лотах товара «p1» после приёмки qty > 0 — товар теперь в наличии.
// История читается только там, где состояние дня неизвестно: у строки дня от
// снапшота/события лишнего запроса нет (wantLastKnown = 0).
func TestOnStockChanged_BackInStockFromHistory(t *testing.T) {
	today := day(10)
	yesterday := day(9)
	twoDaysAgo := day(8)

	cases := []backInStockCase{
		{
			name: "нет строки дня, вчера не было в наличии → уведомление",
			days: map[string]*daystate.DayState{
				key("p1", yesterday): {ProductID: "p1", Date: yesterday, InStock: new(false), Orderable: true},
			},
			wantEnsured:     1,
			wantSeed:        new(false),
			wantLastKnown:   1,
			wantBackInStock: []string{"p1"},
			wantUpdated:     new(true),
		},
		{
			name:            "нет строки дня, истории нет → уведомления нет, посев из лотов",
			days:            map[string]*daystate.DayState{},
			wantEnsured:     1,
			wantSeed:        new(true),
			wantLastKnown:   1,
			wantBackInStock: nil,
			wantUpdated:     new(true),
		},
		{
			name: "вчера NULL (календарь), позавчера не было → уведомление",
			days: map[string]*daystate.DayState{
				key("p1", yesterday):  {ProductID: "p1", Date: yesterday},
				key("p1", twoDaysAgo): {ProductID: "p1", Date: twoDaysAgo, InStock: new(false)},
			},
			wantEnsured:     1,
			wantSeed:        new(false),
			wantLastKnown:   1,
			wantBackInStock: []string{"p1"},
			wantUpdated:     new(true),
		},
		{
			name: "строка дня уже есть с in_stock=true (снапшот) → ложного уведомления нет, история не читается",
			days: map[string]*daystate.DayState{
				key("p1", today):     {ProductID: "p1", Date: today, InStock: new(true), Orderable: true},
				key("p1", yesterday): {ProductID: "p1", Date: yesterday, InStock: new(false), Orderable: true},
			},
			wantEnsured:     0,
			wantLastKnown:   0,
			wantBackInStock: nil,
			wantUpdated:     new(true),
		},
		{
			name: "строка дня есть с in_stock=NULL (календарь) → «было» из истории, уведомление",
			days: map[string]*daystate.DayState{
				key("p1", today):     {ProductID: "p1", Date: today, Orderable: true},
				key("p1", yesterday): {ProductID: "p1", Date: yesterday, InStock: new(false), Orderable: true},
			},
			wantEnsured:     0,
			wantLastKnown:   1,
			wantBackInStock: []string{"p1"},
			wantUpdated:     new(true),
		},
		{
			name: "строка дня есть с in_stock=false (закончился), история true → уведомление по строке дня",
			days: map[string]*daystate.DayState{
				key("p1", today):     {ProductID: "p1", Date: today, InStock: new(false), SoldOutToday: true, Orderable: true},
				key("p1", yesterday): {ProductID: "p1", Date: yesterday, InStock: new(true), Orderable: true},
			},
			wantEnsured:     0,
			wantLastKnown:   0,
			wantBackInStock: []string{"p1"},
			wantUpdated:     new(true),
			// Маркер дня приход не снимает (снимает только откат расформирования).
			wantSoldOutToday: true,
		},
		{
			name:          "ошибка чтения истории → строка дня не создана и не пересчитана",
			days:          map[string]*daystate.DayState{},
			lastKnownErr:  errors.New("db down"),
			wantEnsured:   0,
			wantLastKnown: 1,
			wantErr:       true,
		},
		{
			// Решение владельца 15.09.2026: историю берём и для обратного перехода —
			// догоняющее «закончился» остаётся (товара действительно нет).
			name: "история true, лотов нет (обнуление контур не видел) → SoldOut",
			days: map[string]*daystate.DayState{
				key("p1", yesterday): {ProductID: "p1", Date: yesterday, InStock: new(true), Orderable: true},
			},
			emptyLots:        true,
			wantEnsured:      1,
			wantSeed:         new(true),
			wantLastKnown:    1,
			wantSoldOut:      []string{"p1"},
			wantSoldOutCalls: []string{"p1"},
			wantUpdated:      new(false),
			wantSoldOutToday: true,
		},
		{
			// Решение владельца 15.09.2026: позицию, снятую календарём с заказа,
			// приход всё равно уведомляет («товар появился» — факт поступления).
			name: "строка дня orderable=false (календарь), история false → уведомление, метка календаря цела",
			days: map[string]*daystate.DayState{
				key("p1", today):     {ProductID: "p1", Date: today, Orderable: false},
				key("p1", yesterday): {ProductID: "p1", Date: yesterday, InStock: new(false), Orderable: true},
			},
			wantEnsured:     0,
			wantLastKnown:   1,
			wantBackInStock: []string{"p1"},
			wantUpdated:     new(true),
			wantOrderable:   new(false),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runBackInStockCase(t, c, today)
		})
	}
}

// runBackInStockCase прогоняет один сценарий: свежие фейки, вызов шва и
// проверки. Вынесено из таблицы, чтобы она не разрасталась в одну функцию
// (gocognit).
func runBackInStockCase(t *testing.T, c backInStockCase, today time.Time) {
	t.Helper()

	// Свежие фейки на каждый подтест — состояние не переиспользуется.
	lots := []daystate.LotState{{Qty: 5, EffectiveGeneral: i16(0)}}
	if c.emptyLots {
		lots = nil
	}
	repo := &fakeRepo{
		days:         c.days,
		lots:         map[string][]daystate.LotState{"p1": lots},
		lastKnownErr: c.lastKnownErr,
	}
	soldOut := &fakeSoldOut{}
	status := &fakeStockStatus{}
	uc := newTestUC(repo, &fakeCatalog{}, soldOut, &fakeUnavailable{}, &fakeRollback{}, status, today)

	err := uc.OnStockChanged(context.Background(), "p1")
	if c.wantErr {
		if err == nil {
			t.Fatal("OnStockChanged: ошибки нет, want ошибку")
		}
		if len(repo.ensured) != c.wantEnsured {
			t.Errorf("ensured: %d записей, want %d (строка дня не создана)", len(repo.ensured), c.wantEnsured)
		}
		if len(repo.updated) != 0 {
			t.Errorf("updated: %d записей, want 0 (строка дня не пересчитана)", len(repo.updated))
		}
		if repo.lastKnownCalls != c.wantLastKnown {
			t.Errorf("LastKnownInStock: %d вызовов, want %d", repo.lastKnownCalls, c.wantLastKnown)
		}
		return
	}
	if err != nil {
		t.Fatalf("OnStockChanged: %v", err)
	}

	if !reflect.DeepEqual(status.backInStock, c.wantBackInStock) {
		t.Errorf("backInStock = %v, want %v", status.backInStock, c.wantBackInStock)
	}
	if !reflect.DeepEqual(status.soldOut, c.wantSoldOut) {
		t.Errorf("soldOut = %v, want %v", status.soldOut, c.wantSoldOut)
	}
	if !reflect.DeepEqual(soldOut.calls, c.wantSoldOutCalls) {
		t.Errorf("ordercoeff SoldOut: %v, want %v", soldOut.calls, c.wantSoldOutCalls)
	}
	if len(repo.ensured) != c.wantEnsured {
		t.Fatalf("ensured: %d записей, want %d", len(repo.ensured), c.wantEnsured)
	}
	if c.wantEnsured == 1 && !sameBoolPtr(repo.ensured[0].InStock, c.wantSeed) {
		t.Errorf("ensured.InStock = %s, want %s (посев «было»)",
			boolText(repo.ensured[0].InStock), boolText(c.wantSeed))
	}
	if repo.lastKnownCalls != c.wantLastKnown {
		t.Errorf("LastKnownInStock: %d вызовов, want %d", repo.lastKnownCalls, c.wantLastKnown)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("updated: len = %d, want 1", len(repo.updated))
	}
	if got := repo.updated[0].InStock; !sameBoolPtr(got, c.wantUpdated) {
		t.Errorf("updated.InStock = %s, want %s", boolText(got), boolText(c.wantUpdated))
	}
	if repo.updated[0].SoldOutToday != c.wantSoldOutToday {
		t.Errorf("SoldOutToday = %v, want %v", repo.updated[0].SoldOutToday, c.wantSoldOutToday)
	}
	if c.wantOrderable != nil && repo.updated[0].Orderable != *c.wantOrderable {
		t.Errorf("updated.Orderable = %v, want %v", repo.updated[0].Orderable, *c.wantOrderable)
	}
}

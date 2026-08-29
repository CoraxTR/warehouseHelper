package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/stock"
)

// mockRepo — хранилище-заглушка: возвращает фикс. лоты, запоминает UPDATE-вызовы.
type mockRepo struct {
	products  []stock.Product
	updates   []updateCall
	err       error // ошибка LoadAllStock (WarmUp)
	updateErr error // ошибка SetManualDiscount
}

type updateCall struct {
	productID      string
	bestBefore     time.Time
	generalManual  *int16
	telegramManual *int16
}

func (m *mockRepo) LoadAllStock(context.Context) ([]stock.Product, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.products, nil
}

func (m *mockRepo) SetManualDiscount(_ context.Context, productID string, bestBefore time.Time, generalManual, telegramManual *int16) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updates = append(m.updates, updateCall{productID, bestBefore, generalManual, telegramManual})
	return nil
}

// mockPub — публикатор-заглушка, копит события.
type mockPub struct {
	events []stock.Event
}

func (m *mockPub) PublishStockChange(e stock.Event) {
	m.events = append(m.events, e)
}

func i16(v int16) *int16 { return &v }

func d(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// testStock — два товара в двух группах, лоты по возрастанию сроков.
func testStock() []stock.Product {
	produced := d(2026, 8, 28)
	return []stock.Product{
		{
			ID: "p1", InternalCode: "10100001", Name: "Хлеб бородинский", GroupName: "Хлебобулочные", ShortList: true,
			Lots: []stock.Lot{
				{BestBefore: d(2026, 9, 1), Qty: 2, General: i16(5)},
				{BestBefore: d(2026, 9, 5), Qty: 5, Telegram: i16(20)},
				{BestBefore: d(2026, 9, 10), Qty: 10, ProducedOn: &produced},
			},
		},
		{
			ID: "p2", InternalCode: "20100002", Name: "Молоко", GroupName: "Молочные", ShortList: false,
			Lots: []stock.Lot{
				{BestBefore: d(2026, 9, 3), Qty: 7},
			},
		},
	}
}

func newTestUC(repo Repository, pub Publisher) *StockUseCase {
	return NewStockUseCase(repo, pub)
}

func TestWarmUp(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	uc := newTestUC(repo, nil)

	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// byCode построен — шов будущей приёмки.
	if id, ok := uc.byCode["10100001"]; !ok || id != "p1" {
		t.Errorf("byCode[10100001] = %q, %v; want p1, true", id, ok)
	}

	snap := uc.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot: len = %d, want 2", len(snap))
	}
	if snap[0].GroupName != "Молочные" || snap[1].GroupName != "Хлебобулочные" {
		t.Errorf("Snapshot порядок групп: %q, %q; want Молочные, Хлебобулочные", snap[0].GroupName, snap[1].GroupName)
	}
	if len(snap[1].Lots) != 3 {
		t.Fatalf("Лоты p1: len = %d, want 3", len(snap[1].Lots))
	}
	if !snap[1].Lots[0].BestBefore.Equal(d(2026, 9, 1)) || !snap[1].Lots[2].BestBefore.Equal(d(2026, 9, 10)) {
		t.Errorf("Лоты p1 не по возрастанию best_before: %v", snap[1].Lots)
	}
}

func TestWarmUpRepoError(t *testing.T) {
	repo := &mockRepo{err: errors.New("pg down")}
	uc := newTestUC(repo, nil)

	if err := uc.WarmUp(context.Background()); err == nil {
		t.Fatal("WarmUp: want error, got nil")
	}
}

func TestWarmUpEmpty(t *testing.T) {
	uc := newTestUC(&mockRepo{}, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}
	if snap := uc.Snapshot(); len(snap) != 0 {
		t.Fatalf("Snapshot пустого кэша: len = %d, want 0", len(snap))
	}
}

func TestSetManualDiscount(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.SetManualDiscount(context.Background(), "p1", d(2026, 9, 1), i16(7), i16(0))
	if err != nil {
		t.Fatalf("SetManualDiscount: %v", err)
	}

	// Репо получил значения.
	if len(repo.updates) != 1 {
		t.Fatalf("repo.updates: len = %d, want 1", len(repo.updates))
	}
	up := repo.updates[0]
	if up.productID != "p1" || !up.bestBefore.Equal(d(2026, 9, 1)) {
		t.Errorf("UPDATE args: (%s, %s)", up.productID, up.bestBefore.Format("2006-01-02"))
	}
	if up.generalManual == nil || *up.generalManual != 7 {
		t.Errorf("generalManual = %v, want 7", up.generalManual)
	}
	if up.telegramManual == nil || *up.telegramManual != 0 {
		t.Errorf("telegramManual = %v, want 0 (заданный ноль)", up.telegramManual)
	}

	// Кэш обновлён.
	snap := uc.Snapshot()
	if got := snap[1].Lots[0].GeneralManual; got == nil || *got != 7 {
		t.Errorf("кэш general_manual = %v, want 7", got)
	}

	// Событие опубликовано.
	if len(pub.events) != 1 {
		t.Fatalf("событий: %d, want 1", len(pub.events))
	}
	e := pub.events[0]
	if e.Kind != stock.EventLotUpsert || e.ProductID != "p1" {
		t.Errorf("событие: kind=%q product=%q", e.Kind, e.ProductID)
	}
	if e.Lot == nil || e.Lot.GeneralManual == nil || *e.Lot.GeneralManual != 7 {
		t.Errorf("событие не несёт обновлённый лот: %+v", e.Lot)
	}
}

func TestSetManualDiscountReset(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// nil = сброс (NULL в БД).
	if err := uc.SetManualDiscount(context.Background(), "p1", d(2026, 9, 1), nil, nil); err != nil {
		t.Fatalf("SetManualDiscount: %v", err)
	}
	if len(repo.updates) != 1 || repo.updates[0].generalManual != nil || repo.updates[0].telegramManual != nil {
		t.Errorf("сброс должен передать nil: %+v", repo.updates)
	}
	if got := uc.Snapshot()[1].Lots[0].GeneralManual; got != nil {
		t.Errorf("кэш general_manual = %v, want nil после сброса", got)
	}
}

func TestSetManualDiscountValidation(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	cases := []struct {
		name string
		gen  *int16
		tg   *int16
	}{
		{"генерал больше 100", i16(101), nil},
		{"телеграм меньше 0", nil, i16(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := uc.SetManualDiscount(context.Background(), "p1", d(2026, 9, 1), tc.gen, tc.tg); err == nil {
				t.Fatal("want validation error, got nil")
			}
			if len(repo.updates) != 0 {
				t.Fatal("UPDATE не должен был дойти до репо")
			}
		})
	}
}

func TestSetManualDiscountProductNotFound(t *testing.T) {
	uc := newTestUC(&mockRepo{products: testStock()}, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	if err := uc.SetManualDiscount(context.Background(), "nope", d(2026, 9, 1), i16(5), nil); !errors.Is(err, stock.ErrProductNotFound) {
		t.Fatalf("want ErrProductNotFound, got %v", err)
	}
}

func TestSetManualDiscountLotNotFound(t *testing.T) {
	uc := newTestUC(&mockRepo{products: testStock()}, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	if err := uc.SetManualDiscount(context.Background(), "p1", d(2030, 1, 1), i16(5), nil); !errors.Is(err, stock.ErrLotNotFound) {
		t.Fatalf("want ErrLotNotFound, got %v", err)
	}
}

func TestSetManualDiscountRepoError(t *testing.T) {
	repo := &mockRepo{products: testStock(), updateErr: errors.New("update failed")}
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.SetManualDiscount(context.Background(), "p1", d(2026, 9, 1), i16(5), nil)
	if err == nil {
		t.Fatal("want repo error, got nil")
	}
	// Кэш не тронут, событие не опубликовано.
	if got := uc.Snapshot()[1].Lots[0].GeneralManual; got != nil {
		t.Errorf("кэш изменён при ошибке репо: %v", got)
	}
	if len(pub.events) != 0 {
		t.Errorf("события опубликованы при ошибке репо: %+v", pub.events)
	}
}

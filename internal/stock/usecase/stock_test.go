package usecase

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"warehouseHelper/internal/stock"
)

// mockRepo — хранилище-заглушка: возвращает фикс. лоты, запоминает вызовы.
type mockRepo struct {
	products    []stock.Product
	updates     []updateCall
	err         error                    // ошибка LoadAllStock (WarmUp)
	updateErr   error                    // ошибка SetManualDiscount
	writeErr    error                    // ошибка ReplaceStockLots
	catalog     map[string]stock.Product // каталог для LoadProductsByCodes
	writes      []stock.ProductWrite     // записанные замены
	productByID map[string]stock.Product // для LoadProductByID
	groupNames  map[string]string        // для LoadGroupNameByCode
	accepted    []stock.LotIn            // принятые партии (AcceptStockLots)
	acceptErr   error                    // ошибка AcceptStockLots
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

func (m *mockRepo) LoadProductsByCodes(_ context.Context, codes []string) (map[string]stock.Product, error) {
	out := make(map[string]stock.Product, len(codes))
	for _, c := range codes {
		if p, ok := m.catalog[c]; ok {
			out[c] = p
		}
	}
	return out, nil
}

func (m *mockRepo) LoadProductByID(_ context.Context, productID string) (stock.Product, error) {
	p, ok := m.productByID[productID]
	if !ok {
		return stock.Product{}, stock.ErrProductNotFound
	}
	return p, nil
}

func (m *mockRepo) LoadGroupNameByCode(_ context.Context, groupCode string) (string, error) {
	return m.groupNames[groupCode], nil
}

func (m *mockRepo) ReplaceStockLots(_ context.Context, writes []stock.ProductWrite) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.writes = append(m.writes, writes...)
	return nil
}

func (m *mockRepo) AcceptStockLots(_ context.Context, lots []stock.LotIn) error {
	if m.acceptErr != nil {
		return m.acceptErr
	}
	m.accepted = append(m.accepted, lots...)
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
	return NewStockUseCase(repo, pub, nil)
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
		t.Errorf("UPDATE args: (%s, %s)", up.productID, up.bestBefore.Format(time.DateOnly))
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

// --- «Обновить сроки»: замена остатков по сканам ---

// day — дата через n дней от сегодня (UTC-полночь, как normalizeDate).
func day(n int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day()+n, 0, 0, 0, 0, time.UTC)
}

// codeItem собирает штрих-код куска (29 цифр) конкатенацией полей.
func codeItem(internalCode string, weight int, prod, exp time.Time) string {
	return internalCode + fmt.Sprintf("%05d", weight) + prod.Format("02012006") + exp.Format("02012006")
}

// codeBox собирает штрих-код коробки (33 цифры) конкатенацией полей.
func codeBox(internalCode string, weight, qty int, prod, exp time.Time) string {
	return internalCode + fmt.Sprintf("%06d", weight) + fmt.Sprintf("%03d", qty) + prod.Format("02012006") + exp.Format("02012006")
}

func replaceRepo() *mockRepo {
	prod := day(-10)
	return &mockRepo{
		products: []stock.Product{
			{
				ID: "p1", InternalCode: "10100001", Name: "Хлеб", GroupName: "Хлебобулочные",
				Lots: []stock.Lot{
					{BestBefore: day(1), Qty: 2, General: i16(5), GeneralManual: i16(3), TelegramManual: i16(7)},
					{BestBefore: day(5), Qty: 5, Telegram: i16(20)},
					{BestBefore: day(10), Qty: 10, ProducedOn: &prod},
				},
			},
		},
		catalog: map[string]stock.Product{
			"10100001": {ID: "p1", InternalCode: "10100001", Name: "Хлеб", GroupName: "Хлебобулочные"},
			"20100002": {ID: "p2", InternalCode: "20100002", Name: "Молоко", GroupName: "Молочные"},
			"10100003": {ID: "p3", InternalCode: "10100003", Name: "Сыр", GroupName: "Молочные"},
		},
	}
}

func TestReplaceStock(t *testing.T) {
	repo := replaceRepo()
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Кусок + коробка одного товара на один срок: qty = 1 + 10 = 11.
	// Лоты 05.09 и 10.09 не отсканированы — будут удалены.
	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{
			codeItem("10100001", 250, day(-10), day(1)),
			codeBox("10100001", 2500, 10, day(-10), day(1)),
		},
	})
	if err != nil {
		t.Fatalf("ReplaceStock: %v", err)
	}

	// Репо получил: один товар, upsert лота day(1) с qty 11 и сохранёнными manual,
	// deletes двух неотсканированных лотов.
	if len(repo.writes) != 1 {
		t.Fatalf("writes: len = %d, want 1", len(repo.writes))
	}
	w := repo.writes[0]
	if w.ProductID != "p1" {
		t.Errorf("productID = %q, want p1", w.ProductID)
	}
	if len(w.Upserts) != 1 {
		t.Fatalf("upserts: len = %d, want 1", len(w.Upserts))
	}
	u := w.Upserts[0]
	if !u.BestBefore.Equal(day(1)) || u.Qty != 11 {
		t.Errorf("upsert: (%s, qty %d), want (day(1), 11)", u.BestBefore.Format(time.DateOnly), u.Qty)
	}
	if u.GeneralManual == nil || *u.GeneralManual != 3 || u.TelegramManual == nil || *u.TelegramManual != 7 {
		t.Errorf("manual-скидки неистёкшего лота должны сохраниться: %v/%v", u.GeneralManual, u.TelegramManual)
	}
	if len(w.Deletes) != 2 {
		t.Fatalf("deletes: len = %d, want 2", len(w.Deletes))
	}

	// Кэш: остался один лот с суммой сканов и скидками.
	snap := uc.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("кэш: товаров %d, want 1", len(snap))
	}
	lots := snap[0].Lots
	if len(lots) != 1 || lots[0].Qty != 11 || !lots[0].BestBefore.Equal(day(1)) {
		t.Errorf("кэш p1: %+v", lots)
	}
	if lots[0].GeneralManual == nil || *lots[0].GeneralManual != 3 {
		t.Errorf("кэш general_manual = %v, want 3", lots[0].GeneralManual)
	}
}

// События замены: сначала удаления (05.09, 10.09), потом upsert с суммой.
func TestReplaceStockPublishesEvents(t *testing.T) {
	repo := replaceRepo()
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{
			codeItem("10100001", 250, day(-10), day(1)),
			codeBox("10100001", 2500, 10, day(-10), day(1)),
		},
	})
	if err != nil {
		t.Fatalf("ReplaceStock: %v", err)
	}

	if len(pub.events) != 3 {
		t.Fatalf("событий: %d, want 3", len(pub.events))
	}
	if pub.events[0].Kind != stock.EventLotDelete || !pub.events[0].BestBefore.Equal(day(5)) {
		t.Errorf("событие 1: %+v", pub.events[0])
	}
	if pub.events[1].Kind != stock.EventLotDelete || !pub.events[1].BestBefore.Equal(day(10)) {
		t.Errorf("событие 2: %+v", pub.events[1])
	}
	if pub.events[2].Kind != stock.EventLotUpsert || pub.events[2].Lot == nil || pub.events[2].Lot.Qty != 11 {
		t.Errorf("событие 3: %+v", pub.events[2])
	}
}

func TestReplaceStockExpiredClearsManual(t *testing.T) {
	repo := replaceRepo()
	// Истёкший лот (вчера) с ручной скидкой — в сканах: скидка должна сброситься.
	repo.products[0].Lots = append(repo.products[0].Lots,
		stock.Lot{BestBefore: day(-1), Qty: 3, GeneralManual: i16(50), TelegramManual: i16(50)})
	uc := newTestUC(repo, &mockPub{})
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{codeItem("10100001", 250, day(-10), day(-1))},
	})
	if err != nil {
		t.Fatalf("ReplaceStock: %v", err)
	}

	u := repo.writes[0].Upserts[0]
	if u.GeneralManual != nil || u.TelegramManual != nil {
		t.Errorf("manual истёкшего лота должны сброситься: %v/%v", u.GeneralManual, u.TelegramManual)
	}
}

func TestReplaceStockNewProduct(t *testing.T) {
	repo := replaceRepo() // p3 в каталоге, в кэше нет (нет лотов)
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{codeItem("10100003", 200, day(-5), day(7))},
	})
	if err != nil {
		t.Fatalf("ReplaceStock: %v", err)
	}

	if len(repo.writes) != 1 || repo.writes[0].ProductID != "p3" {
		t.Fatalf("writes: %+v", repo.writes)
	}
	// Товар появился в кэше с лотом, byCode пополнен.
	snap := uc.Snapshot()
	if len(snap) != 2 || snap[0].Name != "Сыр" {
		t.Fatalf("кэш: %+v", snap)
	}
	if len(snap[0].Lots) != 1 || snap[0].Lots[0].Qty != 1 || !snap[0].Lots[0].BestBefore.Equal(day(7)) {
		t.Errorf("лот p3: %+v", snap[0].Lots)
	}
	if id, ok := uc.byCode["10100003"]; !ok || id != "p3" {
		t.Errorf("byCode[10100003] = %q, %v; want p3, true", id, ok)
	}
	// Событие лота нового товара опубликовано.
	if len(pub.events) != 1 || pub.events[0].ProductID != "p3" {
		t.Errorf("события: %+v", pub.events)
	}
}

func TestReplaceStockConstraintProduct(t *testing.T) {
	repo := replaceRepo()
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Страница открыта по p1, а отсканирован код p2.
	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans:             []string{codeItem("20100002", 250, day(-10), day(1))},
		ExpectedProductID: "p1",
	})
	if !errors.Is(err, stock.ErrScanProductMismatch) {
		t.Fatalf("want ErrScanProductMismatch, got %v", err)
	}
	if len(repo.writes) != 0 {
		t.Fatal("репо не должен был вызываться")
	}
}

func TestReplaceStockConstraintGroup(t *testing.T) {
	repo := replaceRepo()
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Группа 021, а код 20100002 (группа 010 по индексам 1..3).
	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans:             []string{codeItem("20100002", 250, day(-10), day(1))},
		ExpectedGroupCode: "021",
	})
	if !errors.Is(err, stock.ErrScanGroupMismatch) {
		t.Fatalf("want ErrScanGroupMismatch, got %v", err)
	}
	if len(repo.writes) != 0 {
		t.Fatal("репо не должен был вызываться")
	}
}

func TestReplaceStockProductNotFound(t *testing.T) {
	repo := replaceRepo()
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{codeItem("10555555", 250, day(-10), day(1))},
	})
	if !errors.Is(err, stock.ErrProductNotFound) {
		t.Fatalf("want ErrProductNotFound, got %v", err)
	}
	if len(repo.writes) != 0 {
		t.Fatal("репо не должен был вызываться")
	}
}

func TestReplaceStockNotInternal(t *testing.T) {
	uc := newTestUC(replaceRepo(), nil)
	if err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{"1234567890"}, // 10 цифр — не внутренний
	}); !errors.Is(err, stock.ErrScanNotInternal) {
		t.Fatalf("want ErrScanNotInternal, got %v", err)
	}
}

func TestReplaceStockEmpty(t *testing.T) {
	uc := newTestUC(replaceRepo(), nil)
	if err := uc.ReplaceStock(context.Background(), ReplaceRequest{}); err == nil {
		t.Fatal("want error for empty scans, got nil")
	}
}

func TestReplaceStockRepoError(t *testing.T) {
	repo := replaceRepo()
	repo.writeErr = errors.New("tx failed")
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{codeItem("10100001", 250, day(-10), day(1))},
	})
	if err == nil {
		t.Fatal("want repo error, got nil")
	}
	// Кэш не тронут, событий нет.
	snap := uc.Snapshot()
	if len(snap[0].Lots) != 3 {
		t.Errorf("кэш изменён при ошибке репо: %+v", snap[0].Lots)
	}
	if len(pub.events) != 0 {
		t.Errorf("события при ошибке репо: %+v", pub.events)
	}
}

// --- контекст страницы «Обновить сроки» ---

func TestUpdatePageContextProduct(t *testing.T) {
	repo := &mockRepo{productByID: map[string]stock.Product{
		"p1": {ID: "p1", InternalCode: "10100001", Name: "Хлеб"},
	}}
	uc := newTestUC(repo, nil)

	pc, err := uc.UpdatePageContext(context.Background(), "p1", "")
	if err != nil {
		t.Fatalf("UpdatePageContext: %v", err)
	}
	if pc.ProductID != "p1" || pc.Code != "10100001" || pc.Name != "Хлеб" || pc.GroupCode != "" {
		t.Errorf("контекст товара: %+v", pc)
	}
}

func TestUpdatePageContextProductNoCode(t *testing.T) {
	repo := &mockRepo{productByID: map[string]stock.Product{
		"p1": {ID: "p1", Name: "Без кода"},
	}}
	uc := newTestUC(repo, nil)
	if _, err := uc.UpdatePageContext(context.Background(), "p1", ""); err == nil {
		t.Fatal("want error for product without internal code")
	}
}

func TestUpdatePageContextGroup(t *testing.T) {
	repo := &mockRepo{groupNames: map[string]string{"021": "021 - Zozulinsky&Potseluev"}}
	uc := newTestUC(repo, nil)

	pc, err := uc.UpdatePageContext(context.Background(), "", "021")
	if err != nil {
		t.Fatalf("UpdatePageContext: %v", err)
	}
	if pc.GroupCode != "021" || pc.Name != "021 - Zozulinsky&Potseluev" || pc.ProductID != "" {
		t.Errorf("контекст группы: %+v", pc)
	}
}

func TestUpdatePageContextEmpty(t *testing.T) {
	uc := newTestUC(&mockRepo{}, nil)
	pc, err := uc.UpdatePageContext(context.Background(), "", "")
	if err != nil {
		t.Fatalf("UpdatePageContext: %v", err)
	}
	if pc != (PageContext{}) {
		t.Errorf("полное обновление: контекст должен быть пустым, got %+v", pc)
	}
}

func TestValidLengths(t *testing.T) {
	uc := newTestUC(&mockRepo{}, nil)
	got := uc.ValidLengths()
	if len(got) != 2 || got[0] != 29 || got[1] != 33 {
		t.Errorf("ValidLengths = %v, want [29 33]", got)
	}
}

func TestAcceptStock_AddsToExistingLot(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Существующий срок 2026-09-01 (qty=2) — приёмка добавляет 3.
	err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p1", BestBefore: d(2026, 9, 1), Qty: 3},
	})
	if err != nil {
		t.Fatalf("AcceptStock: %v", err)
	}
	if len(repo.accepted) != 1 || repo.accepted[0].Qty != 3 {
		t.Fatalf("repo.accepted = %+v, want одна партия qty=3", repo.accepted)
	}

	snap := uc.Snapshot()
	var lot *stock.Lot
	for _, p := range snap {
		if p.ID == "p1" {
			for j := range p.Lots {
				if p.Lots[j].BestBefore.Equal(d(2026, 9, 1)) {
					lot = &p.Lots[j]
				}
			}
		}
	}
	if lot == nil || lot.Qty != 5 {
		t.Fatalf("лот 2026-09-01: %+v, want qty=5 (2+3)", lot)
	}

	if len(pub.events) != 1 || pub.events[0].Kind != stock.EventLotUpsert || pub.events[0].ProductID != "p1" {
		t.Fatalf("events = %+v, want один lot_upsert p1", pub.events)
	}
}

func TestAcceptStock_NewLot(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	uc := newTestUC(repo, &mockPub{})
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Новый срок — новая строка, лоты остаются отсортированными по датам.
	err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p1", BestBefore: d(2026, 9, 7), Qty: 4, ProducedOn: ptrTime(d(2026, 8, 30))},
	})
	if err != nil {
		t.Fatalf("AcceptStock: %v", err)
	}

	snap := uc.Snapshot()
	var lots []stock.Lot
	for _, p := range snap {
		if p.ID == "p1" {
			lots = p.Lots
		}
	}
	if len(lots) != 4 {
		t.Fatalf("p1 лотов = %d, want 4", len(lots))
	}
	if !lots[2].BestBefore.Equal(d(2026, 9, 7)) || lots[2].Qty != 4 {
		t.Fatalf("позиция нового лота: %+v, want 2026-09-07 qty=4 между 09-05 и 09-10", lots[2])
	}
}

func TestAcceptStock_ProductNotInCache(t *testing.T) {
	repo := &mockRepo{
		products: testStock(),
		productByID: map[string]stock.Product{
			"p9": {ID: "p9", InternalCode: "90900009", Name: "Новый товар"},
		},
	}
	pub := &mockPub{}
	uc := newTestUC(repo, pub)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p9", BestBefore: d(2026, 9, 20), Qty: 1},
	})
	if err != nil {
		t.Fatalf("AcceptStock: %v", err)
	}

	snap := uc.Snapshot()
	var found *stock.Product
	for i := range snap {
		if snap[i].ID == "p9" {
			found = &snap[i]
		}
	}
	if found == nil || len(found.Lots) != 1 || found.Lots[0].Qty != 1 {
		t.Fatalf("товар p9 в кэше: %+v, want лот qty=1", found)
	}
	if id, ok := uc.byCode["90900009"]; !ok || id != "p9" {
		t.Errorf("byCode[90900009] = %q, %v; want p9", id, ok)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want 1", len(pub.events))
	}
}

func TestAcceptStock_ProducedOnCoalesce(t *testing.T) {
	repo := &mockRepo{products: testStock()} // p1 2026-09-10 с produced_on 2026-08-28
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Приёмка знает другую дату выработки — существующая не затирается.
	err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p1", BestBefore: d(2026, 9, 10), Qty: 1, ProducedOn: ptrTime(d(2026, 8, 1))},
	})
	if err != nil {
		t.Fatalf("AcceptStock: %v", err)
	}

	snap := uc.Snapshot()
	for _, p := range snap {
		if p.ID != "p1" {
			continue
		}
		for _, l := range p.Lots {
			if l.BestBefore.Equal(d(2026, 9, 10)) {
				if l.ProducedOn == nil || !l.ProducedOn.Equal(d(2026, 8, 28)) {
					t.Fatalf("produced_on = %v, want 2026-08-28 (существующая)", l.ProducedOn)
				}
				if l.Qty != 11 {
					t.Fatalf("qty = %d, want 11 (10+1)", l.Qty)
				}
			}
		}
	}
}

func TestAcceptStock_Validation(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	cases := []struct {
		name string
		lots []stock.LotIn
	}{
		{"пустой список", nil},
		{"нет товара", []stock.LotIn{{BestBefore: d(2026, 9, 1), Qty: 1}}},
		{"qty = 0", []stock.LotIn{{ProductID: "p1", BestBefore: d(2026, 9, 1), Qty: 0}}},
		{"нет срока", []stock.LotIn{{ProductID: "p1", Qty: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := uc.AcceptStock(context.Background(), tc.lots); err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			if len(repo.accepted) != 0 {
				t.Fatalf("repo.accepted = %+v, репозиторий не должен вызываться", repo.accepted)
			}
		})
	}
}

func TestAcceptStock_RepoError(t *testing.T) {
	repo := &mockRepo{products: testStock(), acceptErr: errors.New("pg down")}
	uc := newTestUC(repo, nil)
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p1", BestBefore: d(2026, 9, 1), Qty: 1},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка репозитория")
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

// mockDayState — наблюдатель-заглушка состояния по дням: запоминает товары,
// может отдавать ошибку.
type mockDayState struct {
	calls []string
	err   error
}

func (m *mockDayState) OnStockChanged(_ context.Context, productID string) error {
	m.calls = append(m.calls, productID)
	if m.err != nil {
		return m.err
	}
	return nil
}

// Шов daystate: после каждой записи остатков наблюдатель вызывается по
// каждому уникальному товару; ошибка наблюдателя операцию не роняет.
func TestDayStateRecorder(t *testing.T) {
	repo := &mockRepo{products: testStock()}
	ds := &mockDayState{}
	uc := newTestUC(repo, nil)
	uc.dayState = ds
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	// Приёмка: два лота одного товара + один другого → по одному вызову на товар.
	if err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p1", BestBefore: d(2026, 9, 1), Qty: 3},
		{ProductID: "p1", BestBefore: d(2026, 9, 2), Qty: 1},
		{ProductID: "p2", BestBefore: d(2026, 9, 1), Qty: 2},
	}); err != nil {
		t.Fatalf("AcceptStock: %v", err)
	}
	if len(ds.calls) != 2 || ds.calls[0] != "p1" || ds.calls[1] != "p2" {
		t.Errorf("AcceptStock: вызовы = %v, want [p1 p2]", ds.calls)
	}

	// Ручная скидка.
	ds.calls = nil
	if err := uc.SetManualDiscount(context.Background(), "p1", d(2026, 9, 1), i16(10), nil); err != nil {
		t.Fatalf("SetManualDiscount: %v", err)
	}
	if len(ds.calls) != 1 || ds.calls[0] != "p1" {
		t.Errorf("SetManualDiscount: вызовы = %v, want [p1]", ds.calls)
	}

	// Ошибка наблюдателя не роняет операцию стока (вызов был, ошибка логируется).
	ds.err = errors.New("boom")
	ds.calls = nil
	if err := uc.AcceptStock(context.Background(), []stock.LotIn{
		{ProductID: "p1", BestBefore: d(2026, 9, 3), Qty: 1},
	}); err != nil {
		t.Fatalf("AcceptStock с ошибкой наблюдателя: %v", err)
	}
	if len(ds.calls) != 1 {
		t.Errorf("вызовов = %d, want 1", len(ds.calls))
	}
}

// Замена остатков («Обновить сроки») уведомляет daystate по каждому товару.
func TestDayStateRecorderReplace(t *testing.T) {
	repo := replaceRepo()
	ds := &mockDayState{}
	uc := newTestUC(repo, nil)
	uc.dayState = ds
	if err := uc.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	if err := uc.ReplaceStock(context.Background(), ReplaceRequest{
		Scans: []string{codeItem("10100001", 250, day(-10), day(1))},
	}); err != nil {
		t.Fatalf("ReplaceStock: %v", err)
	}
	if len(ds.calls) != 1 || ds.calls[0] != "p1" {
		t.Errorf("замена: вызовы = %v, want [p1]", ds.calls)
	}
}

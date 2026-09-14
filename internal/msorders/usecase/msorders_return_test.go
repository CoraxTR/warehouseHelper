package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/scanmatch"
	"warehouseHelper/internal/stock"
)

// fakeAcceptor — фейк шва приёма остатков (StockAcceptor).
type fakeAcceptor struct {
	lots  []stock.LotIn
	calls int
	err   error
}

func (f *fakeAcceptor) AcceptStock(_ context.Context, lots []stock.LotIn) error {
	f.calls++
	f.lots = append(f.lots, lots...)

	return f.err
}

// fakeNotifier — фейк шва уведомлений складу (WarehouseNotifier).
type fakeNotifier struct {
	texts []string
	err   error
}

func (f *fakeNotifier) NotifyWarehouse(text string) error {
	f.texts = append(f.texts, text)

	return f.err
}

// returnOrder — заказ под тесты возврата: штучная строка с резервом 5 шт и
// весовая с резервом 0,657 кг (обе — с остатком, как после подбора).
func returnOrder() (order *fakeOrderDetail, orderID string) {
	fake, o := submitOrder()
	fake.positions = []client.MSPosition{
		position("pos-1", "21110001", "Соус терияки", 5, 50000, 5),
		position("pos-2", "00220002", "Стейк Нью-Йорк", 0.657, 279000, 0.657),
	}

	return fake, o.ID
}

// returnUC собирает UseCase со фейками швов возврата.
func returnUC(fake *fakeOrderDetail, acceptor *fakeAcceptor, notifier *fakeNotifier) *UseCase {
	return NewUseCase(fake, submitCatalog(), &fakePicker{}, acceptor, notifier)
}

func lotsTotal(lots []stock.LotIn) int64 {
	var sum int64
	for _, l := range lots {
		sum += l.Qty
	}

	return sum
}

// TestSavePickReturnWeighted — весовая строка: один скан того же веса, что
// резерв, — единица уходит в остатки по сроку этикетки.
func TestSavePickReturnWeighted(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})

	res, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{{WeightG: 657, BB: "10102026"}}},
	}})
	if err != nil {
		t.Fatalf("SavePickReturn: %v", err)
	}
	if acceptor.calls != 1 {
		t.Fatalf("AcceptStock вызовов = %d, want 1", acceptor.calls)
	}
	if len(acceptor.lots) != 1 {
		t.Fatalf("лотов = %d, want 1: %+v", len(acceptor.lots), acceptor.lots)
	}
	lot := acceptor.lots[0]
	if lot.ProductID != "p2" || !lot.BestBefore.Equal(oktDate(10)) || lot.Qty != 1 {
		t.Errorf("лот = %+v, want p2/2026-10-10/1", lot)
	}
	if res.Units != 1 || len(res.Rows) != 1 || res.Rows[0].Code != "00220002" || res.Rows[0].Units != 1 {
		t.Errorf("сводка = %+v, want 1 единица по 00220002", res)
	}
}

// TestSavePickReturnWeightedWeightMismatch — вес скана не равен резерву: отказ
// ядра сверки, в остатки не пишем (выход — ручное закрытие).
func TestSavePickReturnWeightedWeightMismatch(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{{WeightG: 700, BB: "10102026"}}},
	}})
	if err == nil {
		t.Fatal("ожидался отказ сверки")
	}

	var verr *scanmatch.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("ошибка %v (%T), want *scanmatch.ValidationError", err, err)
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при отказе сверки: %d", acceptor.calls)
	}
}

// TestSavePickReturnWeightedSecondScanRejected — строку гасит ровно ОДИН скан:
// второй скан того же веса — отказ.
func TestSavePickReturnWeightedSecondScanRejected(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{
			{WeightG: 657, BB: "10102026"},
			{WeightG: 657, BB: "11102026"},
		}},
	}})
	if err == nil {
		t.Fatal("ожидался отказ: весовую строку гасит один скан")
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при отказе сверки: %d", acceptor.calls)
	}
}

// TestSavePickReturnPiecesPartial — штучная строка: вернулась часть резерва
// (2 из 5) — каждая единица отдельным лотом по своему сроку этикетки.
func TestSavePickReturnPiecesPartial(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})

	res, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: []ScanRecord{
			{BB: "10102026"},
			{BB: "11102026"},
		}},
	}})
	if err != nil {
		t.Fatalf("SavePickReturn: %v", err)
	}
	if len(acceptor.lots) != 2 {
		t.Fatalf("лотов = %d, want 2 (разные сроки): %+v", len(acceptor.lots), acceptor.lots)
	}
	if got := lotsTotal(acceptor.lots); got != 2 {
		t.Errorf("единиц = %d, want 2", got)
	}
	if res.Units != 2 {
		t.Errorf("Units = %d, want 2", res.Units)
	}
}

// TestSavePickReturnPiecesSameDateMerged — два куска с одним сроком склеиваются
// в один лот (остатки ключуются парой товар+срок).
func TestSavePickReturnPiecesSameDateMerged(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})

	if _, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: []ScanRecord{
			{BB: "10102026"},
			{BB: "10102026"},
		}},
	}}); err != nil {
		t.Fatalf("SavePickReturn: %v", err)
	}
	if len(acceptor.lots) != 1 || acceptor.lots[0].Qty != 2 {
		t.Errorf("лотов = %+v, want один лот qty 2", acceptor.lots)
	}
}

// TestSavePickReturnOverReserve — вернуть больше, чем в резерве строки, нельзя.
func TestSavePickReturnOverReserve(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})

	scans := make([]ScanRecord, 0, 6)
	for range 6 {
		scans = append(scans, ScanRecord{BB: "10102026"})
	}

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: scans},
	}})
	if !errors.Is(err, ErrReturnOverReserve) {
		t.Fatalf("ошибка = %v, want ErrReturnOverReserve", err)
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при отказе: %d", acceptor.calls)
	}
}

// TestSavePickReturnCodeNotInCatalog — товара строки нет в каталоге склада:
// в остатки писать нечем, отказ.
func TestSavePickReturnCodeNotInCatalog(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := NewUseCase(fake, &fakeCatalog{byCode: map[string]CatalogProduct{}}, &fakePicker{}, acceptor, &fakeNotifier{})

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: []ScanRecord{{BB: "10102026"}}},
	}})
	if !errors.Is(err, ErrReturnCodeMissing) {
		t.Fatalf("ошибка = %v, want ErrReturnCodeMissing", err)
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при отказе: %d", acceptor.calls)
	}
}

// TestSavePickReturnPiecesWithoutScans — штучная строка без сканов: возвращать
// нечего (клиент не должен присылать такие строки, но молча пропустить нельзя).
func TestSavePickReturnPiecesWithoutScans(t *testing.T) {
	fake, id := returnOrder()
	uc := returnUC(fake, &fakeAcceptor{}, &fakeNotifier{})

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001"},
	}})
	if !errors.Is(err, ErrReturnNoScans) {
		t.Fatalf("ошибка = %v, want ErrReturnNoScans", err)
	}
}

// TestSavePickReturnRowMissing — позиция строки не найдена в заказе: страница
// устарела, отказ до любых записей.
func TestSavePickReturnRowMissing(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"ghost"}, Code: "21110001", Scans: []ScanRecord{{BB: "10102026"}}},
	}})
	if !errors.Is(err, ErrSubmitRowMissing) {
		t.Fatalf("ошибка = %v, want ErrSubmitRowMissing", err)
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при отказе: %d", acceptor.calls)
	}
}

// TestSavePickReturnWithoutStockSeam — шов остатков не подключён: операция
// терять данные молча не должна (500, а не «сохранено»).
func TestSavePickReturnWithoutStockSeam(t *testing.T) {
	fake, id := returnOrder()
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, &fakeNotifier{})

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: []ScanRecord{{BB: "10102026"}}},
	}})
	if err == nil || !strings.Contains(err.Error(), "шов остатков не подключён") {
		t.Fatalf("ошибка = %v, want про неподключённый шов остатков", err)
	}
}

// TestClosePickReturnNotifiesWarehouse — ручной оверрайд: в остатки НЕ пишем,
// склад получает список незакрытых строк для пересчёта сроков.
func TestClosePickReturnNotifiesWarehouse(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	notifier := &fakeNotifier{}
	uc := returnUC(fake, acceptor, notifier)

	err := uc.ClosePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: []ScanRecord{{BB: "10102026"}}},
	}})
	if err != nil {
		t.Fatalf("ClosePickReturn: %v", err)
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при оверрайде: %d", acceptor.calls)
	}
	if len(notifier.texts) != 1 {
		t.Fatalf("уведомлений = %d, want 1: %+v", len(notifier.texts), notifier.texts)
	}
	text := notifier.texts[0]
	if !strings.Contains(text, "пересчитать сроки") || !strings.Contains(text, "21110001") {
		t.Errorf("текст уведомления = %q, want про пересчёт сроков и код строки", text)
	}
}

// TestClosePickReturnAllClosedSilent — все строки закрыты сканами: пересчитывать
// нечего, уведомление не шлём.
func TestClosePickReturnAllClosedSilent(t *testing.T) {
	fake, id := returnOrder()
	notifier := &fakeNotifier{}
	uc := returnUC(fake, &fakeAcceptor{}, notifier)

	scans := make([]ScanRecord, 0, 5)
	for range 5 {
		scans = append(scans, ScanRecord{BB: "10102026"})
	}

	if err := uc.ClosePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: scans},
	}}); err != nil {
		t.Fatalf("ClosePickReturn: %v", err)
	}
	if len(notifier.texts) != 0 {
		t.Errorf("уведомлений = %d, want 0 (строки закрыты): %+v", len(notifier.texts), notifier.texts)
	}
}

// TestSavePickReturnEmptyRows — пустой запрос возврата: отказ до чтения заказа.
func TestSavePickReturnEmptyRows(t *testing.T) {
	fake, id := returnOrder()
	uc := returnUC(fake, &fakeAcceptor{}, &fakeNotifier{})

	if _, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{}); !errors.Is(err, ErrReturnEmptyRows) {
		t.Fatalf("ошибка = %v, want ErrReturnEmptyRows", err)
	}
}

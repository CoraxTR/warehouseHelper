package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/msorders"
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

	if _, ok := errors.AsType[*scanmatch.ValidationError](err); !ok {
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

// reducedWeightOrder — заказ с вручную урезанной весовой строкой: резерв
// 1.962 кг (менеджер уменьшил вес), но с «Сроков» снят прежний кусок 2.482 кг
// (лежит в журнале подбора).
func reducedWeightOrder() (order *fakeOrderDetail, orderID string) {
	fake, o := submitOrder()
	fake.positions = []client.MSPosition{
		position("pos-1", "21110001", "Соус терияки", 5, 50000, 5),
		position("pos-2", "00220002", "Стейк Нью-Йорк", 1.962, 279000, 1.962),
	}

	return fake, o.ID
}

// pickedJournal — фейк журнала с одним весовым куском по позиции (прежний
// подбор: кусок тяжелее текущего резерва строки).
func pickedJournal(orderID, positionID string, weightKg float64) *fakeJournal {
	return &fakeJournal{units: []msorders.PickingUnit{
		{OrderID: orderID, PositionID: positionID, InternalCode: "00220002", Weighted: true, WeightKg: weightKg},
	}}
}

// TestReturnWeightG — ожидание возврата весовой строки: вес прежнего куска из
// журнала, если он тяжелее грамм резерва; иначе — граммы резерва.
func TestReturnWeightG(t *testing.T) {
	tests := []struct {
		name      string
		reserveKg float64
		pickedG   int64
		want      int64
	}{
		{name: "журнал тяжелее резерва — вес прежнего куска", reserveKg: 1.962, pickedG: 2482, want: 2482},
		{name: "журнал равен резерву — граммы резерва", reserveKg: 1.962, pickedG: 1962, want: 1962},
		{name: "журнала нет — граммы резерва", reserveKg: 1.962, pickedG: 0, want: 1962},
		{name: "журнал легче резерва — граммы резерва", reserveKg: 1.962, pickedG: 1000, want: 1962},
		{name: "хвостовые граммы резерва округляются", reserveKg: 2.4824, pickedG: 0, want: 2482},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := returnWeightG(tc.reserveKg, tc.pickedG); got != tc.want {
				t.Errorf("returnWeightG(%v, %d) = %d, want %d", tc.reserveKg, tc.pickedG, got, tc.want)
			}
		})
	}
}

// TestSavePickReturnReducedWeightedReturnsOldPiece — урезанная вручную весовая
// строка: оператор вернул прежний кусок 2.482 кг — единица уходит в остатки по
// сроку этикетки, а журнал чистится по весу 2.482 (не по резерву 1.962).
func TestSavePickReturnReducedWeightedReturnsOldPiece(t *testing.T) {
	fake, id := reducedWeightOrder()
	acceptor := &fakeAcceptor{}
	j := pickedJournal(id, "pos-2", 2.482)
	uc := returnUC(fake, acceptor, &fakeNotifier{})
	uc.SetPickingJournal(j)

	res, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{{WeightG: 2482, BB: "10102026"}}},
	}})
	if err != nil {
		t.Fatalf("SavePickReturn: %v", err)
	}
	if acceptor.calls != 1 || len(acceptor.lots) != 1 {
		t.Fatalf("AcceptStock вызовов = %d, лотов = %d, want 1/1: %+v", acceptor.calls, len(acceptor.lots), acceptor.lots)
	}
	lot := acceptor.lots[0]
	if lot.ProductID != "p2" || !lot.BestBefore.Equal(oktDate(10)) || lot.Qty != 1 {
		t.Errorf("лот = %+v, want p2/2026-10-10/1", lot)
	}
	if res.Units != 1 || len(res.Rows) != 1 || res.Rows[0].Code != "00220002" {
		t.Errorf("сводка = %+v, want 1 единица по 00220002", res)
	}

	checkRemovals(t, j.removals, []msorders.PickingReturn{
		{OrderID: id, InternalCode: "00220002", BestBefore: jDay(time.October, 10), WeightKg: 2.482, Count: 1},
	})
}

// TestSavePickReturnReducedWeightedReserveScanRejected — урезанная строка:
// скана по весу резерва (1.962) недостаточно — ожидается прежний кусок 2.482,
// отказ сверки, в остатки не пишем.
func TestSavePickReturnReducedWeightedReserveScanRejected(t *testing.T) {
	fake, id := reducedWeightOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})
	uc.SetPickingJournal(pickedJournal(id, "pos-2", 2.482))

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{{WeightG: 1962, BB: "10102026"}}},
	}})
	if err == nil {
		t.Fatal("ожидался отказ сверки: ожидание возврата — прежний кусок 2.482")
	}
	if _, ok := errors.AsType[*scanmatch.ValidationError](err); !ok {
		t.Fatalf("ошибка %v (%T), want *scanmatch.ValidationError", err, err)
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при отказе сверки: %d", acceptor.calls)
	}
}

// TestSavePickReturnWeightedWithoutJournalRegression — журнал пуст (pickedG
// нет): ожидание возврата = резерв строки, скан 1.962 кг проходит — старое
// поведение не сломано.
func TestSavePickReturnWeightedWithoutJournalRegression(t *testing.T) {
	fake, id := reducedWeightOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})
	uc.SetPickingJournal(&fakeJournal{}) // журнал подключён, но строк по заказу нет

	if _, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{{WeightG: 1962, BB: "10102026"}}},
	}}); err != nil {
		t.Fatalf("SavePickReturn: %v", err)
	}
	if acceptor.calls != 1 || len(acceptor.lots) != 1 || acceptor.lots[0].ProductID != "p2" {
		t.Fatalf("AcceptStock вызовов = %d, лоты = %+v, want один лот p2", acceptor.calls, acceptor.lots)
	}
}

// TestSavePickReturnJournalReadError — ошибка чтения журнала: 500 наружу,
// acceptor не зовём (ожидание возврата вычислить нечем).
func TestSavePickReturnJournalReadError(t *testing.T) {
	fake, id := reducedWeightOrder()
	acceptor := &fakeAcceptor{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})
	uc.SetPickingJournal(&fakeJournal{unitsErr: errors.New("pg down")})

	_, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{{WeightG: 2482, BB: "10102026"}}},
	}})
	if err == nil || !strings.Contains(err.Error(), "read order picking") {
		t.Fatalf("ошибка = %v, want про чтение журнала подбора", err)
	}
	if acceptor.calls != 0 {
		t.Errorf("AcceptStock вызван при ошибке журнала: %d", acceptor.calls)
	}
}

// TestClosePickReturnReducedWeightedStaysInNotice — ручное закрытие урезанной
// весовой строки: оператор отсканировал старую этикетку (2.482), но прежний
// кусок в «Сроки» НЕ записан (дат нет) — позиция обязана остаться в уведомлении
// складу о пересчёте сроков. Контракт: ручное закрытие не молчит.
func TestClosePickReturnReducedWeightedStaysInNotice(t *testing.T) {
	fake, id := reducedWeightOrder()
	notifier := &fakeNotifier{}
	j := pickedJournal(id, "pos-2", 2.482)
	uc := returnUC(fake, &fakeAcceptor{}, notifier)
	uc.SetPickingJournal(j)

	err := uc.ClosePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{{WeightG: 2482, BB: "10102026"}}},
	}})
	if err != nil {
		t.Fatalf("ClosePickReturn: %v", err)
	}
	if len(notifier.texts) != 1 {
		t.Fatalf("уведомлений = %d, want 1: %+v", len(notifier.texts), notifier.texts)
	}
	text := notifier.texts[0]
	if !strings.Contains(text, "пересчитать сроки") || !strings.Contains(text, "00220002") {
		t.Errorf("текст уведомления = %q, want про пересчёт сроков и код строки", text)
	}
	// Живая позиция строки запроса: журнал чистится (дат у ручного закрытия нет).
	checkClear(t, j.clears, id, []string{"pos-2"})
}

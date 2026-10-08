// Тесты записи журнала подбора (таблица order_picking): что уходит в шов
// PickingJournal из Submit/SubmitManual/SavePickReturn/ClosePickReturn, очистка
// при расформировании и ретеншен. Реальных запросов нет: МС, каталог, склад и
// журнал — фейки пакета (msorders_submit_test.go и соседи).
package usecase

import (
	"context"
	"errors"
	"maps"
	"math"
	"slices"
	"testing"
	"time"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/msorders"
)

// fakeJournal — фейк шва PickingJournal: запоминает вызовы записи, чтобы
// проверить, что сценарий пишет ровно то, что должен, и не пишет лишнего.
// Чтение журнала (/sroki) здесь не проверяется — им занят fakeShelfLifeJournal.
type fakeJournal struct {
	replaces []msorders.PickingReplace
	appends  [][]msorders.PickingUnit
	removals []msorders.PickingReturn
	clears   []journalClear
	cleanups []time.Time
	cleanupN int64

	// units — строки журнала для чтения (OrderPickingByOrder): нужны
	// расформированию, которое ищет мёртвые позиции по журналу и заказу.
	units    []msorders.PickingUnit
	unitsErr error

	replaceErr error
	appendErr  error
	removeErr  error
	clearErr   error
	cleanupErr error

	// cleanupCh — если задан, каждый проход чистки шлёт сюда отсечку: тест
	// ретеншена ждёт первый проход по каналу, а не читает слайс из чужой
	// горутины (гонок нет).
	cleanupCh chan time.Time
}

// journalClear — вызов ClearOrderPicking (orderID + позиции).
type journalClear struct {
	orderID     string
	positionIDs []string
}

func (f *fakeJournal) ReplaceOrderPicking(_ context.Context, r msorders.PickingReplace) error {
	f.replaces = append(f.replaces, r)

	return f.replaceErr
}

func (f *fakeJournal) AppendOrderPicking(_ context.Context, units []msorders.PickingUnit) error {
	f.appends = append(f.appends, units)

	return f.appendErr
}

func (f *fakeJournal) RemoveOrderPickingUnits(_ context.Context, r msorders.PickingReturn) error {
	f.removals = append(f.removals, r)

	return f.removeErr
}

func (f *fakeJournal) ClearOrderPicking(_ context.Context, orderID string, positionIDs []string) error {
	f.clears = append(f.clears, journalClear{orderID: orderID, positionIDs: positionIDs})

	return f.clearErr
}

func (f *fakeJournal) CleanupOrderPicking(_ context.Context, olderThan time.Time) (int64, error) {
	f.cleanups = append(f.cleanups, olderThan)
	if f.cleanupCh != nil {
		select {
		case f.cleanupCh <- olderThan:
		default:
		}
	}

	return f.cleanupN, f.cleanupErr
}

// OrderPickingByOrder — чтение журнала: сценарии записи его не трогают,
// расформированию отдаёт заданные строки (units).
func (f *fakeJournal) OrderPickingByOrder(context.Context, string) ([]msorders.PickingUnit, error) {
	return f.units, f.unitsErr
}

// jUnit — плоское ожидание строки журнала: даты сравниваются строкой дня,
// чтобы не возиться с *time.Time (пустая выработка — ProducedOn == nil).
type jUnit struct {
	order      string
	position   string
	code       string
	product    string
	name       string
	weighted   bool
	weightKg   float64
	produced   string // "" — ProducedOn == nil
	bestBefore string
}

func jUnits(units []msorders.PickingUnit) []jUnit {
	out := make([]jUnit, 0, len(units))
	for i := range units {
		u := &units[i]
		produced := ""
		if u.ProducedOn != nil {
			produced = u.ProducedOn.Format(time.DateOnly)
		}
		out = append(out, jUnit{
			order:      u.OrderID,
			position:   u.PositionID,
			code:       u.InternalCode,
			product:    u.ProductID,
			name:       u.ProductName,
			weighted:   u.Weighted,
			weightKg:   u.WeightKg,
			produced:   produced,
			bestBefore: u.BestBefore.Format(time.DateOnly),
		})
	}

	return out
}

// checkUnits сравнивает единицы журнала с ожиданием: вес — с точностью грамма,
// даты — по дню и UTC-полночи.
func checkUnits(t *testing.T, what string, got []msorders.PickingUnit, want []jUnit) {
	t.Helper()

	g := jUnits(got)
	if len(g) != len(want) {
		t.Fatalf("%s: единиц %d, want %d: %+v", what, len(g), len(want), g)
	}
	for i := range want {
		if g[i].order != want[i].order || g[i].position != want[i].position ||
			g[i].code != want[i].code || g[i].product != want[i].product ||
			g[i].name != want[i].name || g[i].weighted != want[i].weighted ||
			g[i].produced != want[i].produced || g[i].bestBefore != want[i].bestBefore {
			t.Errorf("%s: единица %d = %+v, want %+v", what, i, g[i], want[i])

			continue
		}
		if math.Abs(g[i].weightKg-want[i].weightKg) > 1e-9 {
			t.Errorf("%s: единица %d вес = %v, want %v", what, i, g[i].weightKg, want[i].weightKg)
		}
	}
}

// checkRemovals сравнивает вызовы RemoveOrderPickingUnits с ожиданием.
func checkRemovals(t *testing.T, got, want []msorders.PickingReturn) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("RemoveOrderPickingUnits вызовов = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.OrderID != w.OrderID || g.InternalCode != w.InternalCode || g.Count != w.Count ||
			!g.BestBefore.Equal(w.BestBefore) || math.Abs(g.WeightKg-w.WeightKg) > 1e-9 {
			t.Errorf("PickingReturn %d = {order:%s code:%s bb:%s кг:%v count:%d}, want {order:%s code:%s bb:%s кг:%v count:%d}",
				i, g.OrderID, g.InternalCode, g.BestBefore.Format(time.DateOnly), g.WeightKg, g.Count,
				w.OrderID, w.InternalCode, w.BestBefore.Format(time.DateOnly), w.WeightKg, w.Count)
		}
	}
}

// checkClear — ровно один вызов ClearOrderPicking с ожидаемыми orderID и
// позициями (nil и пустой список для slices.Equal равны).
func checkClear(t *testing.T, got []journalClear, wantOrder string, wantPositions []string) {
	t.Helper()

	if len(got) != 1 {
		t.Fatalf("ClearOrderPicking вызовов = %d, want 1: %+v", len(got), got)
	}
	if got[0].orderID != wantOrder || !slices.Equal(got[0].positionIDs, wantPositions) {
		t.Errorf("ClearOrderPicking = {%s %v}, want {%s %v}",
			got[0].orderID, got[0].positionIDs, wantOrder, wantPositions)
	}
}

// Подбор с нуля (from == 0): журнал получает замену по позициям строк — по
// единице на скан. Весовая — вес этикетки в кг (657 г → 0,657), штучная — 1;
// срок годности из bb, выработка из pd (пустая pd — ProducedOn == nil).
func TestSubmitJournalReplacesPickFromZero(t *testing.T) {
	fake, o := submitOrder()
	j := &fakeJournal{}
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, nil)
	uc.SetPickingJournal(j)

	res, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{
			{BB: "10102026", PD: "01092026"},
			{BB: "10102026"}, // старая страница pd не прислала — выработка неизвестна
		}},
		{IDs: []string{"pos-2"}, Records: []ScanRecord{
			{WeightG: 657, BB: "10102026", PD: "01092026"},
		}},
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.JournalWarn != "" {
		t.Errorf("JournalWarn = %q, want пусто", res.JournalWarn)
	}
	if len(j.appends) != 0 {
		t.Errorf("AppendOrderPicking при подборе с нуля: %d вызов(ов)", len(j.appends))
	}
	if len(j.replaces) != 1 {
		t.Fatalf("ReplaceOrderPicking вызовов = %d, want 1: %+v", len(j.replaces), j.replaces)
	}
	if rep := j.replaces[0]; rep.OrderID != o.ID || !slices.Equal(rep.PositionIDs, []string{"pos-1", "pos-2"}) {
		t.Errorf("ReplaceOrderPicking = {order:%s positions:%v}, want {order:%s positions:[pos-1 pos-2]}",
			rep.OrderID, rep.PositionIDs, o.ID)
	}

	checkUnits(t, "ReplaceOrderPicking", j.replaces[0].Units, []jUnit{
		{order: o.ID, position: "pos-1", code: "21110001", product: "p1", name: "Соус терияки",
			weightKg: 1, produced: "2026-09-01", bestBefore: "2026-10-10"},
		{order: o.ID, position: "pos-1", code: "21110001", product: "p1", name: "Соус терияки",
			weightKg: 1, bestBefore: "2026-10-10"},
		{order: o.ID, position: "pos-2", code: "00220002", product: "p2", name: "Стейк Нью-Йорк",
			weighted: true, weightKg: 0.657, produced: "2026-09-01", bestBefore: "2026-10-10"},
	})
}

// Добор (from > 0): часть строки подобрана раньше и уже в журнале — идёт
// дозапись единиц, замены по позиции НЕТ (иначе потерялись бы прежние даты).
func TestSubmitJournalAppendsOnTopup(t *testing.T) {
	fake, o := topupOrder()
	j := &fakeJournal{}
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, nil)
	uc.SetPickingJournal(j)

	res, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, From: 2, Records: []ScanRecord{
			{BB: "10102026", PD: "01092026"},
			{BB: "10102026", PD: "01092026"},
			{BB: "10102026", PD: "01092026"},
		}},
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.JournalWarn != "" {
		t.Errorf("JournalWarn = %q, want пусто", res.JournalWarn)
	}
	if len(j.replaces) != 0 {
		t.Errorf("замена при доборе: %+v", j.replaces)
	}
	if len(j.appends) != 1 {
		t.Fatalf("AppendOrderPicking вызовов = %d, want 1", len(j.appends))
	}

	want := make([]jUnit, 0, 3)
	for range 3 {
		want = append(want, jUnit{order: o.ID, position: "pos-1", code: "21110001", product: "p1",
			name: "Хлеб", weightKg: 1, produced: "2026-09-01", bestBefore: "2026-10-10"})
	}
	checkUnits(t, "AppendOrderPicking", j.appends[0], want)
}

// Переподбор (from == 0, резерв был): журнал заменяется по позиции, а не
// дозаписывается — клиент сбрасывает резерв при переподборе, старые единицы
// строки уходят вместе с заменой.
func TestSubmitJournalReplacesOnRepick(t *testing.T) {
	fake, o := topupOrder()
	j := &fakeJournal{}
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, nil)
	uc.SetPickingJournal(j)

	records := make([]ScanRecord, 0, 5)
	for range 5 {
		records = append(records, ScanRecord{BB: "10102026", PD: "01092026"})
	}

	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: records},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(j.appends) != 0 {
		t.Errorf("дозапись при переподборе: %d вызов(ов)", len(j.appends))
	}
	if len(j.replaces) != 1 {
		t.Fatalf("ReplaceOrderPicking вызовов = %d, want 1", len(j.replaces))
	}
	if !slices.Equal(j.replaces[0].PositionIDs, []string{"pos-1"}) {
		t.Errorf("позиции замены = %v, want [pos-1]", j.replaces[0].PositionIDs)
	}

	want := make([]jUnit, 0, 5)
	for range 5 {
		want = append(want, jUnit{order: o.ID, position: "pos-1", code: "21110001", product: "p1",
			name: "Хлеб", weightKg: 1, produced: "2026-09-01", bestBefore: "2026-10-10"})
	}
	checkUnits(t, "ReplaceOrderPicking", j.replaces[0].Units, want)
}

// Ошибка журнала подбор не роняет: заказ в МС уже обновлён, страница получает
// JournalWarn, а остатки через шов склада всё равно списываются (порядок —
// журнал, потом списание; ошибка журнала списание не отменяет).
func TestSubmitJournalErrorWarns(t *testing.T) {
	fake, o := submitOrder()
	picker := &fakePicker{}
	j := &fakeJournal{replaceErr: errors.New("pg down")}
	uc := NewUseCase(fake, submitCatalog(), picker, nil, nil)
	uc.SetPickingJournal(j)

	res, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "10102026"}}},
	}})
	if err != nil {
		t.Fatalf("Submit err = %v, want nil (заказ уже обновлён)", err)
	}
	if res.JournalWarn != journalWarn {
		t.Errorf("JournalWarn = %q, want %q", res.JournalWarn, journalWarn)
	}
	if picker.calls != 1 || len(picker.lots) == 0 {
		t.Errorf("списание остатков: вызовов %d, лотов %d — want 1 и непусто",
			picker.calls, len(picker.lots))
	}
}

// Успешный журнал — предупреждения нет (запись есть, JournalWarn пуст).
func TestSubmitJournalSuccessNoWarn(t *testing.T) {
	fake, o := submitOrder()
	j := &fakeJournal{}
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, nil)
	uc.SetPickingJournal(j)

	res, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "10102026"}}},
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.JournalWarn != "" {
		t.Errorf("JournalWarn = %q, want пусто", res.JournalWarn)
	}
	if len(j.replaces) != 1 {
		t.Fatalf("ReplaceOrderPicking вызовов = %d, want 1", len(j.replaces))
	}
}

// Возврат в сроки, весовая строка: кусок уходит в остатки — из журнала
// снимается ровно одна строка по коду, сроку этикетки и весу куска.
func TestSavePickReturnJournalRemovesWeightedUnit(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	j := &fakeJournal{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})
	uc.SetPickingJournal(j)

	if _, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{
			{WeightG: 657, BB: "10102026"},
		}},
	}}); err != nil {
		t.Fatalf("SavePickReturn: %v", err)
	}
	if acceptor.calls != 1 {
		t.Fatalf("AcceptStock вызовов = %d, want 1", acceptor.calls)
	}
	if len(j.replaces) != 0 || len(j.appends) != 0 {
		t.Errorf("возврат пишет журнал: replaces=%d appends=%d", len(j.replaces), len(j.appends))
	}

	checkRemovals(t, j.removals, []msorders.PickingReturn{
		{OrderID: id, InternalCode: "00220002", BestBefore: oktDate(10), WeightKg: 0.657, Count: 1},
	})
}

// Возврат в сроки, штучные: вес единицы — 1, совпадения по (код, срок) копятся
// в Count, разным срокам — разные вызовы (порядок сканов сохраняется).
func TestSavePickReturnJournalRemovesPieceUnits(t *testing.T) {
	fake, id := returnOrder()
	acceptor := &fakeAcceptor{}
	j := &fakeJournal{}
	uc := returnUC(fake, acceptor, &fakeNotifier{})
	uc.SetPickingJournal(j)

	if _, err := uc.SavePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: []ScanRecord{
			{BB: "10102026", PD: "01092026"},
			{BB: "10102026", PD: "01092026"},
			{BB: "11102026", PD: "01092026"},
		}},
	}}); err != nil {
		t.Fatalf("SavePickReturn: %v", err)
	}
	if len(j.replaces) != 0 || len(j.appends) != 0 {
		t.Errorf("возврат пишет журнал: replaces=%d appends=%d", len(j.replaces), len(j.appends))
	}

	checkRemovals(t, j.removals, []msorders.PickingReturn{
		{OrderID: id, InternalCode: "21110001", BestBefore: oktDate(10), WeightKg: 1, Count: 2},
		{OrderID: id, InternalCode: "21110001", BestBefore: oktDate(11), WeightKg: 1, Count: 1},
	})
}

// Ручное закрытие возврата в сроки: куски в остатки не вернулись — дат нет,
// строки журнала по «живым» позициям строк запроса убираются (в порядке строк).
func TestClosePickReturnClearsJournalPositions(t *testing.T) {
	fake, id := returnOrder()
	notifier := &fakeNotifier{}
	j := &fakeJournal{}
	uc := returnUC(fake, &fakeAcceptor{}, notifier)
	uc.SetPickingJournal(j)

	if err := uc.ClosePickReturn(context.Background(), id, PickReturnRequest{Rows: []PickReturnRow{
		{IDs: []string{"pos-1"}, Code: "21110001", Scans: []ScanRecord{{BB: "10102026"}}},
		{IDs: []string{"pos-2"}, Code: "00220002", Weighted: true, Scans: []ScanRecord{
			{WeightG: 657, BB: "10102026"},
		}},
	}}); err != nil {
		t.Fatalf("ClosePickReturn: %v", err)
	}

	checkClear(t, j.clears, id, []string{"pos-1", "pos-2"})
	if len(j.replaces) != 0 || len(j.appends) != 0 || len(j.removals) != 0 {
		t.Errorf("ручное закрытие пишет журнал: replaces=%d appends=%d removals=%d",
			len(j.replaces), len(j.appends), len(j.removals))
	}
}

// Ручной вес: дат у ручного ввода нет — строки журнала по позициям запроса
// убираются целиком (иначе /sroki показывал бы сроки прежнего подбора).
func TestSubmitManualClearsJournalPositions(t *testing.T) {
	fake, o := submitOrder()
	j := &fakeJournal{}
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, &fakeNotifier{})
	uc.SetPickingJournal(j)

	res, err := uc.SubmitManual(context.Background(), o.ID, ManualRequest{Rows: []ManualRow{
		{IDs: []string{"pos-1"}, Qty: 2},
	}})
	if err != nil {
		t.Fatalf("SubmitManual: %v", err)
	}
	if res.JournalWarn != "" {
		t.Errorf("JournalWarn = %q, want пусто", res.JournalWarn)
	}

	checkClear(t, j.clears, o.ID, []string{"pos-1"})
	if len(j.replaces) != 0 || len(j.appends) != 0 || len(j.removals) != 0 {
		t.Errorf("ручной вес пишет журнал: replaces=%d appends=%d removals=%d",
			len(j.replaces), len(j.appends), len(j.removals))
	}
}

// ClearShelfLife — очистка по расформированию заказа: пустой productIDs —
// весь заказ (отмена); непустой — только строки позиций, которых в заказе уже
// нет. Одинаковые товары живут отдельными позициями, поэтому удаление одной
// позиции НЕ должно трогать даты остальных (прод-баг 04.10.2026, заказ 07189:
// убрали строку Вырезки 2,125 кг — из ответа /sroki пропали все три строки
// Вырезки, две из них оставались в заказе).
func TestClearShelfLife(t *testing.T) {
	const (
		orderID   = "00023557-7e97-11e7-7a34-5acf0020c748"
		vyrezkaID = "10da451f-a1b1-11e6-7a31-d0fd000e9e27"
		otherID   = "9cc41a2b-ca87-11e6-7a34-5acf000f11f4"
	)

	posFixture := func(id, productID string) client.MSPosition {
		p := client.MSPosition{ID: id}
		p.Assortment.Meta.HREF = "https://api.moysklad.ru/api/remap/1.2/entity/product/" + productID

		return p
	}
	unitLine := func(positionID, productID string) msorders.PickingUnit {
		return msorders.PickingUnit{OrderID: orderID, PositionID: positionID, ProductID: productID}
	}

	t.Run("журнал не подключён", func(t *testing.T) {
		uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, nil, nil, nil)

		if err := uc.ClearShelfLife(context.Background(), orderID, []string{vyrezkaID}); err != nil {
			t.Fatalf("ClearShelfLife: %v", err)
		}
	})

	t.Run("пустой id — отказ", func(t *testing.T) {
		j := &fakeJournal{}
		uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)

		err := uc.ClearShelfLife(context.Background(), "   ", []string{vyrezkaID})
		if !errors.Is(err, ErrEmptyOrderID) {
			t.Fatalf("ClearShelfLife err = %v, want %v", err, ErrEmptyOrderID)
		}
		if len(j.clears) != 0 {
			t.Errorf("ClearOrderPicking вызовов = %d, want 0: %+v", len(j.clears), j.clears)
		}
	})

	t.Run("весь заказ", func(t *testing.T) {
		j := &fakeJournal{}
		uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)

		if err := uc.ClearShelfLife(context.Background(), orderID, nil); err != nil {
			t.Fatalf("ClearShelfLife: %v", err)
		}
		checkClear(t, j.clears, orderID, nil)
	})

	t.Run("удалённая позиция — чистим только её", func(t *testing.T) {
		// Три позиции одного товара и одна чужая; журнал помнит все четыре,
		// в заказе остались pos-2, pos-3 и pos-9 — удалена только pos-1.
		j := &fakeJournal{units: []msorders.PickingUnit{
			unitLine("pos-1", vyrezkaID),
			unitLine("pos-2", vyrezkaID),
			unitLine("pos-3", vyrezkaID),
			unitLine("pos-9", otherID),
		}}
		ms := &fakeOrderDetail{order: &client.MSOrder{ID: orderID}, positions: []client.MSPosition{
			posFixture("pos-2", vyrezkaID),
			posFixture("pos-3", vyrezkaID),
			posFixture("pos-9", otherID),
		}}
		uc := NewUseCase(ms, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)

		if err := uc.ClearShelfLife(context.Background(), orderID, []string{vyrezkaID}); err != nil {
			t.Fatalf("ClearShelfLife: %v", err)
		}
		checkClear(t, j.clears, orderID, []string{"pos-1"})
	})

	t.Run("товар заменён в строке — старые строки уходят", func(t *testing.T) {
		// Позиция жива, но держит уже другой товар: менеджер заменил ассортимент
		// в строке, МС пишет это правкой позиции — строки прежнего товара мёртвы.
		j := &fakeJournal{units: []msorders.PickingUnit{unitLine("pos-1", vyrezkaID)}}
		ms := &fakeOrderDetail{order: &client.MSOrder{ID: orderID}, positions: []client.MSPosition{
			posFixture("pos-1", otherID),
		}}
		uc := NewUseCase(ms, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)

		if err := uc.ClearShelfLife(context.Background(), orderID, []string{vyrezkaID}); err != nil {
			t.Fatalf("ClearShelfLife: %v", err)
		}
		checkClear(t, j.clears, orderID, []string{"pos-1"})
	})

	t.Run("чужие товары не трогаем", func(t *testing.T) {
		// Мёртвая позиция чужого товара (не из события) остаётся в журнале:
		// состав возврата даёт событие, за его пределами чистить нечего.
		j := &fakeJournal{units: []msorders.PickingUnit{unitLine("pos-9", otherID)}}
		ms := &fakeOrderDetail{order: &client.MSOrder{ID: orderID}}
		uc := NewUseCase(ms, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)

		if err := uc.ClearShelfLife(context.Background(), orderID, []string{vyrezkaID}); err != nil {
			t.Fatalf("ClearShelfLife: %v", err)
		}
		if len(j.clears) != 0 {
			t.Errorf("ClearOrderPicking вызовов = %d, want 0: %+v", len(j.clears), j.clears)
		}
	})

	t.Run("заказ не получен — строки не трогаем", func(t *testing.T) {
		// Состав заказа неизвестен: молча снести даты живых позиций нельзя,
		// ошибка уходит наверх, журнал остаётся как есть.
		j := &fakeJournal{units: []msorders.PickingUnit{unitLine("pos-1", vyrezkaID)}}
		ms := &fakeOrderDetail{
			order:        &client.MSOrder{ID: orderID},
			positionsErr: errors.New("МС недоступен"),
		}
		uc := NewUseCase(ms, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)

		if err := uc.ClearShelfLife(context.Background(), orderID, []string{vyrezkaID}); err == nil {
			t.Fatal("ClearShelfLife = nil, want ошибку чтения позиций заказа")
		}
		if len(j.clears) != 0 {
			t.Errorf("ClearOrderPicking вызовов = %d, want 0 (позиции не получены): %+v",
				len(j.clears), j.clears)
		}
	})
}

// Ретеншен: без журнала и без ретеншена чистка не запускается; с ретеншеном
// первый проход идёт сразу с отсечкой ≈ now − 180 дней; отменённый ctx
// завершает фон.
func TestRunShelfLifeCleanup(t *testing.T) {
	const retention = 180 * 24 * time.Hour

	t.Run("журнал не подключён", func(t *testing.T) {
		uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, nil, nil, nil)
		uc.SetShelfLifeRetention(retention)

		runCleanupSync(context.Background(), t, uc)
	})

	t.Run("ретеншен не задан", func(t *testing.T) {
		j := &fakeJournal{}
		uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)

		runCleanupSync(context.Background(), t, uc)
		if len(j.cleanups) != 0 {
			t.Errorf("CleanupOrderPicking вызовов = %d, want 0: %+v", len(j.cleanups), j.cleanups)
		}
	})

	t.Run("отменённый ctx — один проход", func(t *testing.T) {
		j := &fakeJournal{}
		uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)
		uc.SetShelfLifeRetention(retention)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		runCleanupSync(ctx, t, uc)
		if len(j.cleanups) != 1 {
			t.Fatalf("CleanupOrderPicking вызовов = %d, want 1: %+v", len(j.cleanups), j.cleanups)
		}
		checkCutoff(t, j.cleanups[0], retention)
	})

	t.Run("ctx отменяет цикл", func(t *testing.T) {
		j := &fakeJournal{cleanupCh: make(chan time.Time, 4)}
		uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, nil, nil, nil)
		uc.SetPickingJournal(j)
		uc.SetShelfLifeRetention(retention)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			uc.RunShelfLifeCleanup(ctx)
			close(done)
		}()

		select {
		case <-j.cleanupCh:
		case <-time.After(5 * time.Second):
			cancel()

			t.Fatal("первый проход чистки не прошёл")
		}

		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("RunShelfLifeCleanup не завершился по отмене ctx")
		}

		// Горутина завершена — читать счётчики фейка безопасно.
		if len(j.cleanups) != 1 {
			t.Errorf("CleanupOrderPicking вызовов = %d, want 1: %+v", len(j.cleanups), j.cleanups)
		}
		checkCutoff(t, j.cleanups[0], retention)
	})
}

// runCleanupSync прогоняет фон чистки с защитой от зависания: до отмены ctx
// RunShelfLifeCleanup не возвращается.
func runCleanupSync(ctx context.Context, t *testing.T, uc *UseCase) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		uc.RunShelfLifeCleanup(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunShelfLifeCleanup не завершился")
	}
}

// checkCutoff проверяет отсечку чистки: ретеншен в днях назад от «сейчас»,
// допуск — сутки (дата берётся внутри сценария, поэтому строго не сравнить).
func checkCutoff(t *testing.T, cutoff time.Time, retention time.Duration) {
	t.Helper()

	want := time.Now().UTC().AddDate(0, 0, -int(retention.Hours()/24))
	if diff := cutoff.Sub(want); diff < -24*time.Hour || diff > 24*time.Hour {
		t.Errorf("отсечка = %s, want ≈ %s (разница %v)", cutoff, want, diff)
	}
}

// manualRowPositions/returnRowPositions: берут первую позицию строки,
// обрезают пробелы, пропускают строки без ids и с пустой первой позицией,
// порядок строк сохраняют.
func TestJournalRowPositions(t *testing.T) {
	cases := []struct {
		name string
		rows [][]string
		want []string
	}{
		{name: "одна позиция", rows: [][]string{{"pos-1"}}, want: []string{"pos-1"}},
		{name: "первая из группы", rows: [][]string{{"pos-1", "pos-2"}}, want: []string{"pos-1"}},
		{name: "пробелы обрезаются", rows: [][]string{{" pos-1 "}}, want: []string{"pos-1"}},
		{name: "пустой ids", rows: [][]string{{}}, want: nil},
		{name: "пустая первая позиция", rows: [][]string{{"   "}}, want: nil},
		{name: "смешанные строки", rows: [][]string{
			{"pos-1"}, {}, {" pos-2 ", "tail"}, {"   "}, {"pos-3"},
		}, want: []string{"pos-1", "pos-2", "pos-3"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			manual := make([]ManualRow, 0, len(c.rows))
			returns := make([]PickReturnRow, 0, len(c.rows))
			for _, ids := range c.rows {
				manual = append(manual, ManualRow{IDs: ids})
				returns = append(returns, PickReturnRow{IDs: ids})
			}

			if got := manualRowPositions(manual); !slices.Equal(got, c.want) {
				t.Errorf("manualRowPositions = %v, want %v", got, c.want)
			}
			if got := returnRowPositions(returns); !slices.Equal(got, c.want) {
				t.Errorf("returnRowPositions = %v, want %v", got, c.want)
			}
		})
	}
}

// TestPickedWeights — карта весов кусков, снятых с «Сроков», по позициям
// (pickedWeights): только весовые единицы и только позиции ровно с одной
// единицей; nil-журнал — пустая карта без ошибки, ошибка БД — наружу.
func TestPickedWeights(t *testing.T) {
	const orderID = "00023557-7e97-11e7-7a34-5acf0020c748"
	unit := func(pos string, weighted bool, kg float64) msorders.PickingUnit {
		return msorders.PickingUnit{OrderID: orderID, PositionID: pos, Weighted: weighted, WeightKg: kg}
	}

	tests := []struct {
		name    string
		nilJrnl bool
		units   map[string][]msorders.PickingUnit
		jErr    error
		want    map[string]int64
		wantErr bool
	}{
		{
			name:  "одна весовая единица — граммы",
			units: map[string][]msorders.PickingUnit{orderID: {unit("pos-1", true, 2.482)}},
			want:  map[string]int64{"pos-1": 2482},
		},
		{
			name:  "дробный вес округляется до граммов",
			units: map[string][]msorders.PickingUnit{orderID: {unit("pos-1", true, 1.5)}},
			want:  map[string]int64{"pos-1": 1500},
		},
		{
			name: "две единицы по одной позиции — позиция пропущена",
			units: map[string][]msorders.PickingUnit{orderID: {
				unit("pos-1", true, 2.482),
				unit("pos-1", true, 1.1),
			}},
			want: map[string]int64{},
		},
		{
			name:  "штучная единица пропущена",
			units: map[string][]msorders.PickingUnit{orderID: {unit("pos-1", false, 1)}},
			want:  map[string]int64{},
		},
		{
			name: "единицы других позиций не мешают",
			units: map[string][]msorders.PickingUnit{orderID: {
				unit("pos-1", true, 2.482),
				unit("pos-2", true, 1),
				unit("pos-2", true, 2),
			}},
			want: map[string]int64{"pos-1": 2482},
		},
		{
			name:  "в журнале нет строк по заказу",
			units: map[string][]msorders.PickingUnit{},
			want:  map[string]int64{},
		},
		{
			name:    "журнал не подключён — пустая карта без ошибки",
			nilJrnl: true,
			want:    map[string]int64{},
		},
		{
			name:    "ошибка чтения журнала — наружу",
			jErr:    errors.New("pg down"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, &fakePicker{}, nil, nil)

			var jrnl *fakeShelfLifeJournal
			if !tc.nilJrnl {
				jrnl = &fakeShelfLifeJournal{units: tc.units, err: tc.jErr}
				uc.SetPickingJournal(jrnl)
			}

			got, err := uc.pickedWeights(context.Background(), orderID)
			if tc.wantErr {
				if err == nil {
					t.Fatal("pickedWeights error = nil, want ошибку чтения журнала")
				}

				return
			}
			if err != nil {
				t.Fatalf("pickedWeights: %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("pickedWeights = %v, want %v", got, tc.want)
			}
			if tc.nilJrnl {
				return // журнала нет — читать нечего, вызовов быть не может
			}
			if len(jrnl.requested) != 1 || jrnl.requested[0] != orderID {
				t.Errorf("журнал читали по %v, want [%s]", jrnl.requested, orderID)
			}
		})
	}
}

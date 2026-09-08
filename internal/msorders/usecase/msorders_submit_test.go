package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/stock"
)

// fakePicker — фейк шва списания сроков (StockPicker).
type fakePicker struct {
	lots  []stock.PickLotIn
	calls int
	err   error
}

func (f *fakePicker) PickStock(_ context.Context, lots []stock.PickLotIn) error {
	f.calls++
	f.lots = append(f.lots, lots...)
	return f.err
}

func bbDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// submitCatalog — каталог Submit-тестов: штучный p1 и весовой p2.
func submitCatalog() *fakeCatalog {
	return &fakeCatalog{byCode: map[string]CatalogProduct{
		"21110001": {ProductID: "p1", InternalCode: "21110001"},
		"00220002": {ProductID: "p2", InternalCode: "00220002", Weighted: true},
	}}
}

// submitOrder — заказ с двумя активными позициями (штучная + весовая).
func submitOrder() (*fakeOrderDetail, *client.MSOrder) {
	o := detailOrder()
	o.MSPositions = client.MSPositions{
		Meta: client.MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/" + o.ID + "/positions"},
	}
	fake := &fakeOrderDetail{
		order: o,
		positions: []client.MSPosition{
			position("pos-1", "21110001", "Соус терияки", 3, 50000, 0),
			position("pos-2", "00220002", "Стейк Нью-Йорк", 0.5, 279000, 0),
		},
	}
	return fake, o
}

// decodePutPositions достаёт positions из тела PUT.
func decodePutPositions(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var body struct {
		Positions []map[string]any `json:"positions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal put body: %v", err)
	}
	return body.Positions
}

func rowQty(t *testing.T, rows []map[string]any, id string) (qty, reserve float64) {
	t.Helper()
	for _, r := range rows {
		if r["id"] == id {
			return floatField(r["quantity"]), floatField(r["reserve"])
		}
	}
	t.Fatalf("строка %q не найдена в positions: %v", id, rows)
	return 0, 0
}

func TestSubmitPartialPiece(t *testing.T) {
	fake, o := submitOrder()
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{})
	if _, err := uc.Detail(context.Background(), o.ID); err != nil { // греем кэш отправки
		t.Fatalf("Detail: %v", err)
	}

	picker := &fakePicker{}
	uc = NewUseCase(fake, submitCatalog(), picker)
	res, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
		}},
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.StockWarn != "" {
		t.Errorf("StockWarn = %q, want пусто", res.StockWarn)
	}

	if len(fake.putBody) != 1 {
		t.Fatalf("PUT не выполнен (или выполнен N раз): %d", len(fake.putBody))
	}
	rows := decodePutPositions(t, fake.putBody[0])

	// Штучная недобор 2 из 3: живая строка qty=reserve=2 + одна заглушка 0,0001.
	q, rs := rowQty(t, rows, "pos-1")
	if q != 2 || rs != 2 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 2/2", q, rs)
	}
	// Весовая ненабранная активная → заглушка 0,0001.
	q2, rs2 := rowQty(t, rows, "pos-2")
	if q2 != 0.0001 || rs2 != 0 {
		t.Errorf("pos-2 qty/reserve = %v/%v, want 0,0001/0", q2, rs2)
	}
	// Одна строка без id (заглушка 0,0001).
	var stubs int
	for _, r := range rows {
		if _, ok := r["id"]; !ok {
			stubs++
			if q := floatField(r["quantity"]); q != 0.0001 {
				t.Errorf("заглушка qty = %v, want 0.0001", q)
			}
		}
	}
	if stubs != 1 {
		t.Errorf("заглушек = %d, want 1", stubs)
	}

	// Списание: каждый скан — единица (2 записи одного срока).
	if picker.calls != 1 || len(picker.lots) != 2 {
		t.Fatalf("picker: calls=%d lots=%d, want 1/2", picker.calls, len(picker.lots))
	}
	bb := bbDate(2026, 10, 10)
	for _, l := range picker.lots {
		if l.ProductID != "p1" || !l.BestBefore.Equal(bb) || l.Qty != 1 {
			t.Errorf("lot = %+v, want p1 %s qty 1", l, bb.Format("02.01.2006"))
		}
	}
}

func TestSubmitFullPiece(t *testing.T) {
	fake, o := submitOrder()
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
		}},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	q, rs := rowQty(t, rows, "pos-1")
	if q != 3 || rs != 3 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 3/3", q, rs)
	}
	if len(rows) != 2 { // pos-1 + весовая заглушка — заглушек штучной нет
		t.Errorf("позиций в PUT = %d, want 2", len(rows))
	}
	if len(picker.lots) != 3 {
		t.Errorf("lots = %d, want 3", len(picker.lots))
	}
}

func TestSubmitWeightedCovered(t *testing.T) {
	fake, o := submitOrder()
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-2"}, Records: []ScanRecord{
			{WeightG: 1250, BB: "01102026"},
			{WeightG: 1180, BB: "02102026"},
		}},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	q, rs := rowQty(t, rows, "pos-2")
	if q != 2.43 || rs != 2.43 {
		t.Errorf("pos-2 qty/reserve = %v/%v, want 2.43/2.43", q, rs)
	}
	// Штучная ненабранная активная pos-1 → заглушка 0,0001.
	q1, rs1 := rowQty(t, rows, "pos-1")
	if q1 != 0.0001 || rs1 != 0 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 0,0001/0", q1, rs1)
	}
	// Записи 1250+1180: две единицы, два разных срока.
	if len(picker.lots) != 2 {
		t.Fatalf("lots = %d, want 2", len(picker.lots))
	}
	if !picker.lots[0].BestBefore.Equal(bbDate(2026, 10, 1)) || !picker.lots[1].BestBefore.Equal(bbDate(2026, 10, 2)) {
		t.Errorf("сроки lots = %v / %v, want 01.10 / 02.10", picker.lots[0].BestBefore, picker.lots[1].BestBefore)
	}
}

func TestSubmitMergedWeighted(t *testing.T) {
	fake, o := submitOrder()
	// Две весовые строки одной группы (мердж на клиенте) + штучная.
	fake.positions = []client.MSPosition{
		position("pos-w1", "00220002", "Курица", 0.4, 100000, 0),
		position("pos-w2", "00220002", "Курица", 0.3, 100000, 0),
	}
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-w1", "pos-w2"}, Records: []ScanRecord{
			{WeightG: 1250, BB: "01102026"},
			{WeightG: 1180, BB: "02102026"},
		}},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	if len(rows) != 1 {
		t.Fatalf("позиций в PUT = %d, want 1 (смёрженная живая; хвост выбыл)", len(rows))
	}
	q, rs := rowQty(t, rows, "pos-w1")
	if q != 2.43 || rs != 2.43 {
		t.Errorf("pos-w1 qty/reserve = %v/%v, want 2.43/2.43", q, rs)
	}
	if len(picker.lots) != 2 {
		t.Errorf("lots = %d, want 2 (два куска)", len(picker.lots))
	}
}

func TestSubmitUncoveredPieceSplit(t *testing.T) {
	fake, o := submitOrder()
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	// Покрыта только весовая; штучная (3 ед.) не тронута → 3 заглушки 0,0001.
	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-2"}, Records: []ScanRecord{{WeightG: 1250, BB: "01102026"}}},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	if len(rows) != 4 { // pos-1-заглушка + 2 новых + pos-2
		t.Fatalf("позиций в PUT = %d, want 4", len(rows))
	}
	q1, rs1 := rowQty(t, rows, "pos-1")
	if q1 != 0.0001 || rs1 != 0 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 0,0001/0", q1, rs1)
	}
	var stubs int
	for _, r := range rows {
		if _, ok := r["id"]; !ok {
			stubs++
			if q := floatField(r["quantity"]); q != 0.0001 {
				t.Errorf("заглушка qty = %v, want 0.0001", q)
			}
		}
	}
	if stubs != 2 {
		t.Errorf("новых заглушек = %d, want 2", stubs)
	}
}

// TestSubmitValidation — битые входы отклоняются до PUT.
func TestSubmitValidation(t *testing.T) {
	cases := []struct {
		name string
		req  SubmitRequest
		want error
	}{
		{"пусто", SubmitRequest{}, ErrSubmitEmptyRows},
		{"нет записей", SubmitRequest{Rows: []SubmitRow{{IDs: []string{"pos-1"}}}}, ErrSubmitNoRecords},
		{"битая строка", SubmitRequest{Rows: []SubmitRow{{IDs: []string{" "}, Records: []ScanRecord{{BB: "10102026"}}}}}, ErrSubmitBadRow},
		{"плохая дата", SubmitRequest{Rows: []SubmitRow{{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "32022026"}}}}}, ErrSubmitBadBB},
		{"вес вне диапазона", SubmitRequest{Rows: []SubmitRow{{IDs: []string{"pos-2"}, Records: []ScanRecord{{WeightG: 100000, BB: "10102026"}}}}}, ErrSubmitBadWeight},
		{"дубликат id", SubmitRequest{Rows: []SubmitRow{
			{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "10102026"}}},
			{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "10102026"}}},
		}}, ErrSubmitDuplicateID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, o := submitOrder()
			uc := NewUseCase(fake, submitCatalog(), &fakePicker{})
			if _, err := uc.Detail(context.Background(), o.ID); err != nil {
				t.Fatalf("Detail: %v", err)
			}
			if _, err := uc.Submit(context.Background(), o.ID, tc.req); !errors.Is(err, tc.want) {
				t.Errorf("Submit err = %v, want %v", err, tc.want)
			}
			if len(fake.putBody) != 0 {
				t.Error("PUT выполнен при битом запросе")
			}
		})
	}
}

func TestSubmitMissingPosition(t *testing.T) {
	fake, o := submitOrder()
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{})
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	_, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"ghost"}, Records: []ScanRecord{{BB: "10102026"}}},
	}})
	if !errors.Is(err, ErrSubmitRowMissing) {
		t.Errorf("Submit err = %v, want ErrSubmitRowMissing", err)
	}
	if len(fake.putBody) != 0 {
		t.Error("PUT выполнен с чужой позицией")
	}
}

func TestSubmitOverpick(t *testing.T) {
	fake, o := submitOrder()
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{})
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	_, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{
			{BB: "10102026"}, {BB: "10102026"}, {BB: "10102026"}, {BB: "10102026"},
		}},
	}})
	if !errors.Is(err, ErrSubmitOverpick) {
		t.Errorf("Submit err = %v, want ErrSubmitOverpick", err)
	}
	if len(fake.putBody) != 0 {
		t.Error("PUT выполнен при переборе")
	}
}

// topupOrder — заказ с одной штучной позицией в частичном резерве
// (qty=5, reserve=2): менеджер увеличил количество после первого подбора.
func topupOrder() (*fakeOrderDetail, *client.MSOrder) {
	o := detailOrder()
	o.MSPositions = client.MSPositions{
		Meta: client.MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/" + o.ID + "/positions"},
	}
	fake := &fakeOrderDetail{
		order: o,
		positions: []client.MSPosition{
			position("pos-1", "21110001", "Хлеб", 5, 30000, 2),
		},
	}
	return fake, o
}

// TestSubmitTopupFull — добор: строка 5/2, добираем 3 → одна живая строка
// qty=reserve=5, заглушек нет; в сроки — только 3 новых скана.
func TestSubmitTopupFull(t *testing.T) {
	fake, o := topupOrder()
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, From: 2, Records: []ScanRecord{
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
		}},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	if len(rows) != 1 {
		t.Fatalf("позиций в PUT = %d, want 1 (полный добор без заглушек)", len(rows))
	}
	q, rs := rowQty(t, rows, "pos-1")
	if q != 5 || rs != 5 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 5/5 (2 в резерве + 3 добранных)", q, rs)
	}
	if len(picker.lots) != 3 {
		t.Errorf("lots = %d, want 3 (только новые сканы, базовые 2 уже списаны)", len(picker.lots))
	}
}

// TestSubmitTopupPartial — недобор при доборе: добираем 2 из 3 → живая
// строка qty=reserve=4 + одна заглушка 0,0001 на недостающую единицу.
func TestSubmitTopupPartial(t *testing.T) {
	fake, o := topupOrder()
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, From: 2, Records: []ScanRecord{
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
		}},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	q, rs := rowQty(t, rows, "pos-1")
	if q != 4 || rs != 4 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 4/4 (2 + 2 добранных)", q, rs)
	}
	var stubs int
	for _, r := range rows {
		if _, ok := r["id"]; !ok {
			stubs++
			if q := floatField(r["quantity"]); q != 0.0001 {
				t.Errorf("заглушка qty = %v, want 0.0001", q)
			}
		}
	}
	if stubs != 1 {
		t.Errorf("заглушек = %d, want 1 (одна недостающая единица)", stubs)
	}
	if len(picker.lots) != 2 {
		t.Errorf("lots = %d, want 2", len(picker.lots))
	}
}

// TestSubmitTopupOverpick — больше сканов, чем недобрано (4 при недоборе 3) —
// 400 до PUT.
func TestSubmitTopupOverpick(t *testing.T) {
	fake, o := topupOrder()
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{})
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	_, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, From: 2, Records: []ScanRecord{
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
		}},
	}})
	if !errors.Is(err, ErrSubmitOverpick) {
		t.Errorf("Submit err = %v, want ErrSubmitOverpick", err)
	}
	if len(fake.putBody) != 0 {
		t.Error("PUT выполнен при переборе добора")
	}
}

// TestSubmitTopupFromZero — переподбор частично зарезервированной строки
// с нуля (клиент сбросил резерв): From 0 + 5 сканов → qty=reserve=5.
func TestSubmitTopupFromZero(t *testing.T) {
	fake, o := topupOrder()
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, From: 0, Records: []ScanRecord{
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
			{WeightG: 0, BB: "10102026"},
		}},
	}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	q, rs := rowQty(t, rows, "pos-1")
	if q != 5 || rs != 5 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 5/5 (переподбор с нуля)", q, rs)
	}
}

// TestSubmitCacheMissRefetch — промах кэша отправки догружает заказ теми же
// GET (повторный Submit ходит в кэш, новых GET нет).
func TestSubmitCacheMissRefetch(t *testing.T) {
	fake, o := submitOrder()
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{})

	req := SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "10102026"}}},
	}}
	if _, err := uc.Submit(context.Background(), o.ID, req); err != nil {
		t.Fatalf("Submit #1: %v", err)
	}
	if fake.fetchByIDCalls != 1 {
		t.Fatalf("fetchByIDCalls = %d, want 1 (промах → догрузка)", fake.fetchByIDCalls)
	}

	if _, err := uc.Submit(context.Background(), o.ID, req); err != nil {
		t.Fatalf("Submit #2: %v", err)
	}
	if fake.fetchByIDCalls != 1 {
		t.Errorf("fetchByIDCalls = %d, want 1 (второй submit из кэша)", fake.fetchByIDCalls)
	}
	if len(fake.putBody) != 2 {
		t.Errorf("PUT = %d, want 2", len(fake.putBody))
	}
}

func TestSubmitMSError(t *testing.T) {
	fake, o := submitOrder()
	fake.putErr = errors.New("MS: 400 bad request")
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{})
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	picker := &fakePicker{}
	uc = NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "10102026"}}},
	}}); err == nil {
		t.Fatal("Submit err = nil, want ошибку МС")
	}
	if picker.calls != 0 {
		t.Errorf("picker.calls = %d, want 0 (списание только после 200)", picker.calls)
	}
}

func TestSubmitPickerErrorWarns(t *testing.T) {
	fake, o := submitOrder()
	picker := &fakePicker{err: errors.New("pg down")}
	uc := NewUseCase(fake, submitCatalog(), picker)
	if _, err := uc.Detail(context.Background(), o.ID); err != nil {
		t.Fatalf("Detail: %v", err)
	}

	res, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
		{IDs: []string{"pos-1"}, Records: []ScanRecord{{BB: "10102026"}}},
	}})
	if err != nil {
		t.Fatalf("Submit err = %v, want nil (заказ уже обновлён)", err)
	}
	if res.StockWarn == "" {
		t.Error("StockWarn пуст, want предупреждение о ручном списании")
	}
	if len(fake.putBody) != 1 {
		t.Errorf("PUT = %d, want 1", len(fake.putBody))
	}
}

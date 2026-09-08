package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/returns"
	"warehouseHelper/internal/stock"
)

// ── Стабы швов модуля ──────────────────────────────────────────────────────

type stubAudit struct {
	pageRows   []client.AuditRow
	details    map[string][]client.AuditEventRow
	positions  map[string][]client.MSPosition
	detailHits int
	posHits    int
}

func (s *stubAudit) FetchAuditPage(_ context.Context, _ time.Time, _ int) ([]client.AuditRow, int, error) {
	return s.pageRows, len(s.pageRows), nil
}
func (s *stubAudit) FetchAuditDetail(_ context.Context, id string) ([]client.AuditEventRow, error) {
	s.detailHits++
	return s.details[id], nil
}
func (s *stubAudit) FetchOrderPositions(_ context.Context, orderID string) ([]client.MSPosition, error) {
	s.posHits++
	return s.positions[orderID], nil
}

type stubRepo struct {
	cursor  *time.Time
	events  map[string]*returns.ReturnEvent
	sent    map[string][2]int64
	doneIDs []string
}

func newStubRepo() *stubRepo {
	return &stubRepo{events: map[string]*returns.ReturnEvent{}, sent: map[string][2]int64{}}
}
func (s *stubRepo) GetCursor(context.Context) (time.Time, bool, error) {
	if s.cursor == nil {
		return time.Time{}, false, nil
	}
	return *s.cursor, true, nil
}
func (s *stubRepo) SetCursor(_ context.Context, t time.Time) error { s.cursor = &t; return nil }
func (s *stubRepo) InsertEvent(_ context.Context, ev *returns.ReturnEvent) (bool, error) {
	if _, ok := s.events[ev.ID]; ok {
		return false, nil
	}
	c := *ev
	s.events[ev.ID] = &c
	return true, nil
}
func (s *stubRepo) MarkSent(_ context.Context, id string, chatID, messageID int64) error {
	ev, ok := s.events[id]
	if !ok {
		return returns.ErrEventNotFound
	}
	ev.Status = returns.StatusSent
	ev.ChatID = &chatID
	ev.MessageID = &messageID
	s.sent[id] = [2]int64{chatID, messageID}
	return nil
}
func (s *stubRepo) MarkDone(_ context.Context, id string, manual bool) error {
	ev, ok := s.events[id]
	if !ok {
		return returns.ErrEventNotFound
	}
	ev.Status = returns.StatusDone
	ev.Manual = manual
	s.doneIDs = append(s.doneIDs, id)
	return nil
}
func (s *stubRepo) GetEvent(_ context.Context, id string) (*returns.ReturnEvent, error) {
	ev, ok := s.events[id]
	if !ok {
		return nil, returns.ErrEventNotFound
	}
	c := *ev
	return &c, nil
}
func (s *stubRepo) ListActive(context.Context) ([]returns.ReturnEvent, error) {
	var out []returns.ReturnEvent
	for _, ev := range s.events {
		if ev.Status != returns.StatusDone {
			out = append(out, *ev)
		}
	}
	return out, nil
}

type stubCatalog map[string]returns.CatalogProduct

func (c stubCatalog) ProductsByMSIDs(_ context.Context, ids []string) (map[string]returns.CatalogProduct, error) {
	out := make(map[string]returns.CatalogProduct, len(ids))
	for _, id := range ids {
		if p, ok := c[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

type stubStock struct {
	accepted []stock.LotIn
	err      error
}

func (s *stubStock) AcceptStock(_ context.Context, lots []stock.LotIn) error {
	if s.err != nil {
		return s.err
	}
	s.accepted = append(s.accepted, lots...)
	return nil
}

type stubNotifier struct {
	sends     []string
	urls      []string
	deletes   [][2]int64
	chatID    int64
	messageID int64
	err       error
}

func (s *stubNotifier) SendWarehouseReturn(_ context.Context, text, buttonURL string) (tgChatID, tgMessageID int64, err error) {
	if s.err != nil {
		return 0, 0, s.err
	}
	s.sends = append(s.sends, text)
	s.urls = append(s.urls, buttonURL)
	return s.chatID, s.messageID, nil
}
func (s *stubNotifier) DeleteMessage(_ context.Context, chatID, messageID int64) error {
	s.deletes = append(s.deletes, [2]int64{chatID, messageID})
	return nil
}

// ── Фикстуры ───────────────────────────────────────────────────────────────

const (
	codeA      = "00210003" // Чак ролл, весовой
	codeD      = "10080001" // Соус, штучный
	prodA      = "a02a9121-7ef5-11e5-7a40-e897001b4cc6"
	prodD      = "d00d9121-7ef5-11e5-7a40-e897001b4cc6"
	prodNoCode = "n0c09121-7ef5-11e5-7a40-e897001b4cc6"
	orderID    = "211a86f4-b955-11f0-0a80-182e0028d0b8"
	auditID    = "884ff854-abc1-11f1-0a80-106a00019ed6"
)

func testCatalog() stubCatalog {
	return stubCatalog{
		prodA:      {ProductID: prodA, InternalCode: codeA, Weighted: true},
		prodD:      {ProductID: prodD, InternalCode: codeD, Weighted: false},
		prodNoCode: {ProductID: prodNoCode, InternalCode: "", Weighted: true},
	}
}

// etiketa — этикетка куска 29: internal_code(8)+вес_г(5)+выработка(8)+срок(8).
func etiketa(code string, weightG int, exp string) string {
	// выработка в тестовых этикетках фиксирована: 01.09.2026
	return code + fmt.Sprintf("%05d", weightG) + "01092026" + exp
}

// detailRow — строка раскрытия events (JSON по мотивам живого ответа).
func detailRow(diffJSON, name string) client.AuditEventRow {
	raw := `{"source":"app","eventType":"update","entityType":"customerorder","uid":"sklad@steakhome",` +
		`"moment":"2026-09-08 23:11:52.918","name":"` + name + `","diff":` + diffJSON + `,` +
		`"entity":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/customerorder/` + orderID + `"}}}`
	var row client.AuditEventRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		panic(err)
	}
	return row
}

func removedDiffJSON(name string, qty, reserve float64, uom string) string {
	return `{"positions":[{"oldValue":{"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/` + prodA + `"},"name":"` + name + `"},"quantity":` + f(qty) + `,"reserve":` + f(reserve) + `,"uom":"` + uom + `"}}]}`
}
func f(v float64) string {
	return jsonNumber(v)
}
func jsonNumber(v float64) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // float64 сериализуется всегда
	}
	return string(b)
}

// testEnv — окружение юнит-теста: usecase + стабы (один результат вместо
// четырёх — revive function-result-limit).
type testEnv struct {
	uc     *UseCase
	audit  *stubAudit
	stock  *stubStock
	notify *stubNotifier
}

func newTestEnv(repo Repo) *testEnv {
	audit := &stubAudit{details: map[string][]client.AuditEventRow{}, positions: map[string][]client.MSPosition{}}
	stockS := &stubStock{}
	notify := &stubNotifier{chatID: -100999, messageID: 42}
	uc := NewUseCase(Config{
		CancelledStateID: "8737d8a5-c0b9-11e3-ac8e-002590a28eca",
		SkipSources:      []string{"remap-1.2"},
		PublicURL:        "http://warehouse.local:8080",
	}, audit, repo, testCatalog(), stockS, notify)
	return &testEnv{uc: uc, audit: audit, stock: stockS, notify: notify}
}

// ── buildExpected ───────────────────────────────────────────────────────────

func TestBuildExpected_CancelledOnlyPhysicallyReserved(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit

	audit.positions[orderID] = []client.MSPosition{
		{Assortment: client.MSAssortment{Meta: client.MSMeta{HREF: "…/product/" + prodA}, Name: "Чак ролл"}, Quantity: 0.657, Reserve: 0.657},  // отложен
		{Assortment: client.MSAssortment{Meta: client.MSMeta{HREF: "…/product/" + prodD}, Name: "Соус"}, Quantity: 2, Reserve: 0},              // не отложен
		{Assortment: client.MSAssortment{Meta: client.MSMeta{HREF: "…/product/" + prodNoCode}, Name: "Без кода"}, Quantity: 0.5, Reserve: 0.5}, // отложен, но без internal_code
	}

	ev := &returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID}
	expected, err := uc.buildExpected(context.Background(), ev)
	if err != nil {
		t.Fatalf("buildExpected: %v", err)
	}
	if len(expected) != 1 {
		t.Fatalf("expected 1 строку (только отложенный с internal_code), got %d", len(expected))
	}
	if expected[0].ProductID != prodA || expected[0].ExpectedQty != 657 {
		t.Errorf("expected Чак ролл 657 г, got %+v", expected[0])
	}
}

func TestBuildExpected_AggregatesSameProduct(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit

	audit.positions[orderID] = []client.MSPosition{
		{Assortment: client.MSAssortment{Meta: client.MSMeta{HREF: "…/product/" + prodA}, Name: "Чак ролл"}, Quantity: 0.4, Reserve: 0.4},
		{Assortment: client.MSAssortment{Meta: client.MSMeta{HREF: "…/product/" + prodA}, Name: "Чак ролл"}, Quantity: 0.257, Reserve: 0.257},
	}

	ev := &returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID}
	expected, err := uc.buildExpected(context.Background(), ev)
	if err != nil {
		t.Fatalf("buildExpected: %v", err)
	}
	if len(expected) != 1 || expected[0].ExpectedQty != 657 {
		t.Fatalf("ожидалась одна строка «накопленного» Чак ролла 657 г, got %+v", expected)
	}
}

func TestBuildExpected_RemovedWithoutReserveIsNothing(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit

	// Удаление неотложенной позиции (reserve 0) — возвращать нечего.
	audit.details[auditID] = []client.AuditEventRow{detailRow(removedDiffJSON("Чак ролл", 0.657, 0, "кг"), "19191")}

	ev := &returns.ReturnEvent{ID: auditID, Kind: returns.KindRemoved, OrderID: orderID}
	_, err := uc.buildExpected(context.Background(), ev)
	if !errors.Is(err, returns.ErrNothingToReturn) {
		t.Fatalf("want ErrNothingToReturn, got %v", err)
	}
}

// ── matchScans ──────────────────────────────────────────────────────────────

func TestMatchScans_WeightedAccumulation(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	units, err := matchScans([]string{
		etiketa(codeA, 400, "15092026"),
		etiketa(codeA, 257, "15092026"),
	}, expected)
	if err != nil {
		t.Fatalf("matchScans: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("want 2 единицы, got %d", len(units))
	}
}

func TestMatchScans_StrictWeightNoTolerance(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	_, err := matchScans([]string{etiketa(codeA, 654, "15092026")}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "вес не сходится") {
		t.Fatalf("want ValidationError «вес не сходится», got %v", err)
	}
}

func TestMatchScans_OverflowRejected(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	_, err := matchScans([]string{
		etiketa(codeA, 400, "15092026"),
		etiketa(codeA, 300, "15092026"),
	}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError (перебор 700 > 657), got %v", err)
	}
}

func TestMatchScans_UnknownProductRejected(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	_, err := matchScans([]string{etiketa("00999000", 657, "15092026")}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "не в списке возврата") {
		t.Fatalf("want ValidationError «не в списке», got %v", err)
	}
}

func TestMatchScans_BoxRejected(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	// Коробка 33: код(8)+вес_общий_г(6)+кол-во(3)+выработка(8)+срок(8)
	box := codeA + "002500" + "002" + "01092026" + "15092026"
	_, err := matchScans([]string{box}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "коробка") {
		t.Fatalf("want ValidationError «коробка», got %v", err)
	}
}

func TestMatchScans_PieceGoodsByCount(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodD, InternalCode: codeD, Name: "Соус", Weighted: false, ExpectedQty: 2}}

	units, err := matchScans([]string{
		etiketa(codeD, 1, "15092026"), // вес-заглушка 00001 не участвует
		etiketa(codeD, 1, "15092026"),
	}, expected)
	if err != nil {
		t.Fatalf("matchScans: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("want 2 единицы, got %d", len(units))
	}
}

func TestAggregateLots_GroupsByProductAndDate(t *testing.T) {
	expected := returns.Expected{ProductID: prodA, InternalCode: codeA, Weighted: true}
	units := []scannedUnit{
		{expected: &expected, expDate: time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)},
		{expected: &expected, expDate: time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)},
		{expected: &expected, expDate: time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC)},
	}

	lots := aggregateLots(units)
	if len(lots) != 2 {
		t.Fatalf("want 2 лота (по срокам), got %d: %+v", len(lots), lots)
	}
	byDate := map[string]int64{}
	for _, l := range lots {
		byDate[l.BestBefore.Format("2006-01-02")] = l.Qty
	}
	if byDate["2026-09-15"] != 2 || byDate["2026-09-20"] != 1 {
		t.Errorf("qty по срокам = %+v, want 15.09→2, 20.09→1", byDate)
	}
}

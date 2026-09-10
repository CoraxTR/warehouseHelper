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
	// rows — всё окно листа [since..now] как его отдаёт МС: DESC, свежие
	// сверху. FetchAuditPage режет его страницами по pageSize (эмуляция
	// offset-пагинации листа).
	rows       []client.AuditRow
	pageSize   int // размер страницы листа (0 → 25)
	details    map[string][]client.AuditEventRow
	positions  map[string][]client.MSPosition
	detailHits int
	posHits    int
	err        error // сбой МС: возвращается из фетчей (проверка мягких путей)
}

func (s *stubAudit) FetchAuditPage(_ context.Context, _ time.Time, offset int) ([]client.AuditRow, int, error) {
	if s.pageSize <= 0 {
		s.pageSize = 25
	}
	if offset >= len(s.rows) {
		return nil, len(s.rows), nil
	}
	end := min(offset+s.pageSize, len(s.rows))
	return s.rows[offset:end], len(s.rows), nil
}
func (s *stubAudit) FetchAuditDetail(_ context.Context, id string) ([]client.AuditEventRow, error) {
	s.detailHits++
	return s.details[id], nil
}
func (s *stubAudit) FetchOrderPositions(_ context.Context, orderID string) ([]client.MSPosition, error) {
	s.posHits++
	if s.err != nil {
		return nil, s.err
	}
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

func (c stubCatalog) ProductsByInternalCodes(_ context.Context, codes []string) (map[string]returns.CatalogProduct, error) {
	out := make(map[string]returns.CatalogProduct, len(codes))
	for _, code := range codes {
		for _, p := range c {
			if p.InternalCode == code {
				out[code] = p
				break
			}
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
	recounts  []string
	chatID    int64
	messageID int64
	err       error
}

// stubOrders — живой заказ МС: по умолчанию статус = отменён (совпадает с
// testCancelledID), тесты «вернули в работу» меняют stateID.
type stubOrders struct {
	stateID    string
	stateErr   error
	clearErr   error
	stateHits  int
	clearHits  int
	clearedIDs []string
}

func (s *stubOrders) FetchOrderState(_ context.Context, _ string) (string, error) {
	s.stateHits++
	if s.stateErr != nil {
		return "", s.stateErr
	}
	if s.stateID == "" {
		return testCancelledID, nil
	}
	return s.stateID, nil
}

func (s *stubOrders) ClearOrderReserves(_ context.Context, orderID string) error {
	s.clearHits++
	s.clearedIDs = append(s.clearedIDs, orderID)
	return s.clearErr
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
func (s *stubNotifier) NotifyWarehouse(text string) error {
	if s.err != nil {
		return s.err
	}
	s.recounts = append(s.recounts, text)
	return nil
}

// ── Фикстуры ───────────────────────────────────────────────────────────────

// testCancelledID — id статуса «Отменён» в тестах (значение из .env-шаблона).
const testCancelledID = "8737d8a5-c0b9-11e3-ac8e-002590a28eca"

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
// Выработка/срок в тестах фиксированы (01.09.2026 / 15.09.2026).
func etiketa(code string, weightG int) string {
	return code + fmt.Sprintf("%05d", weightG) + "01092026" + "15092026"
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

// removedDiffJSON — diff удаления позиции Чак ролл (весовая, uom «кг»):
// oldValue без newValue. quantity/reserve фиксированы вариантами тестов.
func removedDiffJSON(reserve float64) string {
	return `{"positions":[{"oldValue":{"assortment":{"meta":{"href":"https://api.moysklad.ru/api/remap/1.2/entity/product/` + prodA + `"},"name":"Чак ролл"},"quantity":0.657,"reserve":` + f(reserve) + `,"uom":"кг"}}]}`
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
	orders *stubOrders
}

func newTestEnv(repo Repo) *testEnv {
	audit := &stubAudit{details: map[string][]client.AuditEventRow{}, positions: map[string][]client.MSPosition{}}
	stockS := &stubStock{}
	notify := &stubNotifier{chatID: -100999, messageID: 42}
	orders := &stubOrders{stateID: testCancelledID}
	uc := NewUseCase(Config{
		CancelledStateID: testCancelledID,
		SkipSources:      []string{"remap-1.2"},
		PublicURL:        "http://warehouse.local:8080",
	}, audit, repo, testCatalog(), stockS, notify, orders)
	return &testEnv{uc: uc, audit: audit, stock: stockS, notify: notify, orders: orders}
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

// Per-line модель (решение владельца 10.09): строки одного товара НЕ склеиваются.
func TestBuildExpected_KeepsLinePerPosition(t *testing.T) {
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
	if len(expected) != 2 {
		t.Fatalf("want 2 строки (без склейки по товару), got %d: %+v", len(expected), expected)
	}
	if expected[0].ExpectedQty != 400 || expected[1].ExpectedQty != 257 {
		t.Errorf("веса строк = %d/%d, want 400/257", expected[0].ExpectedQty, expected[1].ExpectedQty)
	}
	if expected[0].Idx != 0 || expected[1].Idx != 1 {
		t.Errorf("Idx = %d/%d, want 0/1 (порядок отчёта)", expected[0].Idx, expected[1].Idx)
	}
}

// Пустая строка (quantity == reserve == 0) в ожидания не попадает: погасить её
// сканом нельзя.
func TestBuildExpected_SkipsZeroQty(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit

	audit.positions[orderID] = []client.MSPosition{
		{Assortment: client.MSAssortment{Meta: client.MSMeta{HREF: "…/product/" + prodA}, Name: "Чак ролл"}, Quantity: 0, Reserve: 0},
	}

	ev := &returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID}
	_, err := uc.buildExpected(context.Background(), ev)
	if !errors.Is(err, returns.ErrNothingToReturn) {
		t.Fatalf("want ErrNothingToReturn, got %v", err)
	}
}

func TestBuildExpected_RemovedWithoutReserveIsNothing(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit

	// Удаление неотложенной позиции (reserve 0) — возвращать нечего.
	audit.details[auditID] = []client.AuditEventRow{detailRow(removedDiffJSON(0), "19191")}

	ev := &returns.ReturnEvent{ID: auditID, Kind: returns.KindRemoved, OrderID: orderID}
	_, err := uc.buildExpected(context.Background(), ev)
	if !errors.Is(err, returns.ErrNothingToReturn) {
		t.Fatalf("want ErrNothingToReturn, got %v", err)
	}
}

// ── matchScans ──────────────────────────────────────────────────────────────

// Весовая строка гасится своим сканом: 5 строк с разными весами = 5 сканов.
func TestMatchScans_WeightedLinePerScan(t *testing.T) {
	expected := []returns.Expected{
		{Idx: 0, ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 400},
		{Idx: 1, ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 257},
	}

	units, err := matchScans([]string{etiketa(codeA, 257), etiketa(codeA, 400)}, expected)
	if err != nil {
		t.Fatalf("matchScans: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("want 2 единицы, got %d", len(units))
	}
}

// Две строки одного кода с ОДИНАКОВЫМ весом различимы только порядком: два
// скана закрывают обе, третий — отказ.
func TestMatchScans_DuplicateWeightsCloseByOrder(t *testing.T) {
	expected := []returns.Expected{
		{Idx: 0, ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657},
		{Idx: 1, ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657},
	}

	if _, err := matchScans([]string{etiketa(codeA, 657), etiketa(codeA, 657)}, expected); err != nil {
		t.Fatalf("две строки 657 г должны закрываться двумя сканами: %v", err)
	}

	_, err := matchScans([]string{etiketa(codeA, 657), etiketa(codeA, 657), etiketa(codeA, 657)}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "не подходит ни одной строке") {
		t.Fatalf("want ValidationError «не подходит ни одной строке» на третий скан, got %v", err)
	}
}

// Вес, которого нет ни в одной строке (в т.ч. «слитая» при подборе позиция),
// отклоняется: бип и отказ, событие закрывается вручную.
func TestMatchScans_WeightNotInRowsRejected(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	_, err := matchScans([]string{etiketa(codeA, 400)}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "не подходит ни одной строке") {
		t.Fatalf("want ValidationError «не подходит ни одной строке», got %v", err)
	}
}

// Строгая сверка без допуска: на 657 г скан 654 г не принимается.
func TestMatchScans_StrictWeightNoTolerance(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	_, err := matchScans([]string{etiketa(codeA, 654)}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
}

// Штучная строка «5 шт» — 5 сканов в одну строку; недобор не сохраняется.
func TestMatchScans_PieceFiveScansOneRow(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodD, InternalCode: codeD, Name: "Соус", Weighted: false, ExpectedQty: 5}}

	scans := []string{etiketa(codeD, 1), etiketa(codeD, 1), etiketa(codeD, 1), etiketa(codeD, 1), etiketa(codeD, 1)}
	units, err := matchScans(scans, expected)
	if err != nil {
		t.Fatalf("matchScans: %v", err)
	}
	if len(units) != 5 {
		t.Fatalf("want 5 единиц, got %d", len(units))
	}

	_, err = matchScans(scans[:4], expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "вес не сходится") {
		t.Fatalf("want ValidationError «вес не сходится» на недобор, got %v", err)
	}
}

func TestMatchScans_UnknownProductRejected(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodA, InternalCode: codeA, Name: "Чак ролл", Weighted: true, ExpectedQty: 657}}

	_, err := matchScans([]string{etiketa("00999000", 657)}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "не в списке возврата") {
		t.Fatalf("want ValidationError «не в списке», got %v", err)
	}
}

// Перебор по штучной строке: все строки уже закрыты.
func TestMatchScans_PieceOverflowRejected(t *testing.T) {
	expected := []returns.Expected{{ProductID: prodD, InternalCode: codeD, Name: "Соус", Weighted: false, ExpectedQty: 2}}

	_, err := matchScans([]string{etiketa(codeD, 1), etiketa(codeD, 1), etiketa(codeD, 1)}, expected)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Reason, "перебор") {
		t.Fatalf("want ValidationError «перебор», got %v", err)
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
		etiketa(codeD, 1), // вес-заглушка 00001 не участвует
		etiketa(codeD, 1),
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
		{row: expected, expDate: time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)},
		{row: expected, expDate: time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)},
		{row: expected, expDate: time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC)},
	}

	lots := aggregateLots(units)
	if len(lots) != 2 {
		t.Fatalf("want 2 лота (по срокам), got %d: %+v", len(lots), lots)
	}
	byDate := map[string]int64{}
	for _, l := range lots {
		byDate[l.BestBefore.Format(time.DateOnly)] = l.Qty
	}
	if byDate["2026-09-15"] != 2 || byDate["2026-09-20"] != 1 {
		t.Errorf("qty по срокам = %+v, want 15.09→2, 20.09→1", byDate)
	}
}

// ── Ручное закрытие: уведомление о пересчёте сроков ─────────────────────────

func TestRecountText_GroupsDuplicateNames(t *testing.T) {
	rows := []returns.Expected{
		{Name: "Чак ролл", InternalCode: codeA, Weighted: true, ExpectedQty: 400},
		{Name: "Чак ролл", InternalCode: codeA, Weighted: true, ExpectedQty: 257},
		{Name: "Соус", InternalCode: codeD, Weighted: false, ExpectedQty: 2},
	}

	want := "Необходимо пересчитать сроки по позициям:\n— Чак ролл (00210003) ×2 строк\n— Соус (10080001)\nпострочно"
	if got := recountText(rows); got != want {
		t.Errorf("recountText:\n got %q\nwant %q", got, want)
	}
}

// Мягкий разбор для списка: чужие сканы игнорируются, закрытые строки в список
// не попадают, порядок — как в отчёте.
func TestUnclosedRows_LenientByReportOrder(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit
	repo.events[auditID] = &returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, OrderName: "19379", Status: returns.StatusSent}
	audit.positions[orderID] = []client.MSPosition{
		pos(prodA, "Чак ролл", 0.4, 0.4),
		pos(prodA, "Чак ролл", 0.257, 0.257),
		pos(prodD, "Соус", 2, 2),
	}

	rows := uc.unclosedRows(context.Background(), repo.events[auditID], []string{
		etiketa(codeA, 400),      // первая строка закрыта
		etiketa(codeD, 1),        // одна единица соуса из двух
		etiketa("00999000", 657), // чужой товар — игнор
	})
	if len(rows) != 2 {
		t.Fatalf("want 2 незакрытые строки, got %d: %+v", len(rows), rows)
	}
	if rows[0].ExpectedQty != 257 || rows[1].InternalCode != codeD {
		t.Errorf("незакрытые строки = %+v, want Чак ролл 257 г и Соус", rows)
	}
}

func TestCloseManual_NotifiesRecountOfUnclosedRows(t *testing.T) {
	repo := newStubRepo()
	chat, msg := int64(-100999), int64(42)
	repo.events[auditID] = &returns.ReturnEvent{
		ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, OrderName: "19379",
		Status: returns.StatusSent, ChatID: &chat, MessageID: &msg,
	}
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify
	audit.positions[orderID] = []client.MSPosition{
		pos(prodA, "Чак ролл", 0.4, 0.4),
		pos(prodA, "Чак ролл", 0.257, 0.257),
	}

	if err := uc.CloseManual(context.Background(), auditID, []string{etiketa(codeA, 400)}); err != nil {
		t.Fatalf("CloseManual: %v", err)
	}

	if len(notify.recounts) != 1 {
		t.Fatalf("want 1 уведомление о пересчёте, got %d: %+v", len(notify.recounts), notify.recounts)
	}
	text := notify.recounts[0]
	if !strings.Contains(text, "Необходимо пересчитать сроки по позициям:") || !strings.Contains(text, "построчно") {
		t.Errorf("текст уведомления = %q", text)
	}
	if !strings.Contains(text, "Чак ролл") || !strings.Contains(text, "("+codeA+")") {
		t.Errorf("в списке нет незакрытой позиции: %q", text)
	}
	if strings.Contains(text, "×2") {
		t.Errorf("закрытая строка не должна попадать в список: %q", text)
	}
}

// Ошибка отправки уведомления не блокирует ручное закрытие.
func TestCloseManual_NotifyErrorDoesNotBlockClose(t *testing.T) {
	repo := newStubRepo()
	repo.events[auditID] = &returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID, Status: returns.StatusSent}
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify
	audit.positions[orderID] = []client.MSPosition{pos(prodA, "Чак ролл", 0.4, 0.4)}
	notify.err = errors.New("telegram недоступен")

	if err := uc.CloseManual(context.Background(), auditID, nil); err != nil {
		t.Fatalf("CloseManual не должен падать из-за уведомления: %v", err)
	}
	if repo.events[auditID].Status != returns.StatusDone {
		t.Errorf("status = %s, want done", repo.events[auditID].Status)
	}
}

// Событие без ожиданий (возвращать нечего) закрывается молча.
func TestCloseManual_NothingToReturnNoNotify(t *testing.T) {
	repo := newStubRepo()
	repo.events[auditID] = &returns.ReturnEvent{ID: auditID, Kind: returns.KindRemoved, OrderID: orderID, Status: returns.StatusSent}
	env := newTestEnv(repo)
	uc, audit, notify := env.uc, env.audit, env.notify
	audit.details[auditID] = []client.AuditEventRow{detailRow(removedDiffJSON(0), "19191")}

	if err := uc.CloseManual(context.Background(), auditID, nil); err != nil {
		t.Fatalf("CloseManual: %v", err)
	}
	if len(notify.recounts) != 0 {
		t.Errorf("пересчитывать нечего — уведомлений быть не должно: %+v", notify.recounts)
	}
}

// ── Остаток заказа (ориентир оператору) ─────────────────────────────────────

// Остаток живого заказа собирается только для события удаления; тип учёта —
// из каталога склада (в позициях МС его нет), ошибка МС не роняет страницу.
func TestRemainingPositions_RemovedEventOnly(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit
	audit.positions[orderID] = []client.MSPosition{
		{Assortment: client.MSAssortment{Code: codeA, Name: "Чак ролл"}, Quantity: 0.25, Reserve: 0},
		{Assortment: client.MSAssortment{Code: codeD, Name: "Соус"}, Quantity: 3, Reserve: 0},
		{Assortment: client.MSAssortment{Code: "", Name: "Без кода"}, Quantity: 1, Reserve: 0},
	}

	rows := uc.remainingPositions(context.Background(),
		&returns.ReturnEvent{ID: auditID, Kind: returns.KindRemoved, OrderID: orderID})
	if len(rows) != 3 {
		t.Fatalf("want 3 строки остатка, got %d: %+v", len(rows), rows)
	}
	if !rows[0].Weighted || rows[0].Quantity != 0.25 || rows[0].InternalCode != codeA {
		t.Errorf("строка остатка = %+v, want весовой Чак ролл 0.25 (код %s)", rows[0], codeA)
	}
	if rows[1].Weighted || rows[1].InternalCode != codeD {
		t.Errorf("строка остатка = %+v, want штучный Соус", rows[1])
	}

	if rows := uc.remainingPositions(context.Background(),
		&returns.ReturnEvent{ID: auditID, Kind: returns.KindCancelled, OrderID: orderID}); rows != nil {
		t.Errorf("для отмены остаток не собирается, got %+v", rows)
	}
}

func TestRemainingPositions_FetchErrorIsSoft(t *testing.T) {
	repo := newStubRepo()
	env := newTestEnv(repo)
	uc, audit := env.uc, env.audit
	audit.err = errors.New("МС недоступен")

	rows := uc.remainingPositions(context.Background(),
		&returns.ReturnEvent{ID: auditID, Kind: returns.KindRemoved, OrderID: orderID})
	if rows != nil {
		t.Errorf("ошибка МС — блок остатка просто не показывается, got %+v", rows)
	}
}

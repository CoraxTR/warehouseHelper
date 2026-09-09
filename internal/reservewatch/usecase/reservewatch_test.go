package usecase

import (
	"context"
	"strings"
	"testing"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/reservewatch"
)

// ── Стабы швов модуля ──────────────────────────────────────────────────────

type stubOrders struct {
	orders    []client.MSOrder
	positions map[string][]client.MSPosition
	listHits  int
	posHits   int
}

func (s *stubOrders) FetchReserveWatchOrders(_ context.Context, _ int, _ []string) ([]client.MSOrder, error) {
	s.listHits++
	return s.orders, nil
}

func (s *stubOrders) FetchOrderPositions(_ context.Context, orderID string) ([]client.MSPosition, error) {
	s.posHits++
	return s.positions[orderID], nil
}

type stubRepo struct {
	notices map[string]reservewatch.Notice // key: noticeKey(order_id, kind)
	deleted []string
}

func newStubRepo() *stubRepo {
	return &stubRepo{notices: map[string]reservewatch.Notice{}}
}

func (s *stubRepo) ListActiveReserveNotices(context.Context) ([]reservewatch.Notice, error) {
	out := make([]reservewatch.Notice, 0, len(s.notices))
	for _, n := range s.notices {
		out = append(out, n)
	}
	return out, nil
}

func (s *stubRepo) InsertReserveNotice(_ context.Context, n reservewatch.Notice) (bool, error) {
	key := noticeKey(n.OrderID, n.Kind)
	if _, ok := s.notices[key]; ok {
		return false, nil
	}
	s.notices[key] = n
	return true, nil
}

func (s *stubRepo) DeleteReserveNotice(_ context.Context, orderID string, kind reservewatch.Kind) error {
	key := noticeKey(orderID, kind)
	if _, ok := s.notices[key]; !ok {
		return reservewatch.ErrNoActiveProblem
	}
	delete(s.notices, key)
	s.deleted = append(s.deleted, key)
	return nil
}

type stubCatalog map[string]reservewatch.CatalogProduct

func (c stubCatalog) ReserveWatchProductsByMSIDs(_ context.Context, ids []string) (map[string]reservewatch.CatalogProduct, error) {
	out := make(map[string]reservewatch.CatalogProduct, len(ids))
	for _, id := range ids {
		if p, ok := c[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

type sentMsg struct {
	text, buttonText, buttonURL string
	messageID                   int64
}

type stubNotifier struct {
	sent    []sentMsg
	deleted []int64
	nextID  int64
}

func (s *stubNotifier) SendWarehouseButton(_ context.Context, text, buttonText, buttonURL string) (chatID, messageID int64, err error) {
	s.nextID++
	mid := 1000 + s.nextID
	s.sent = append(s.sent, sentMsg{text: text, buttonText: buttonText, buttonURL: buttonURL, messageID: mid})
	return 1, mid, nil
}

func (s *stubNotifier) DeleteWarehouseMessage(_ context.Context, messageID int64) error {
	s.deleted = append(s.deleted, messageID)
	return nil
}

// ── Хелперы ────────────────────────────────────────────────────────────────

const productWeightedHREF = "https://api.moysklad.ru/api/remap/1.2/entity/product/w-prod"
const productPieceHREF = "https://api.moysklad.ru/api/remap/1.2/entity/product/p-prod"

func position(productHREF, name string, quantity, reserve float64) client.MSPosition {
	return client.MSPosition{
		Quantity: quantity,
		Reserve:  reserve,
		Assortment: client.MSAssortment{
			Meta: client.MSMeta{HREF: productHREF},
			Name: name,
		},
	}
}

func newUC(orders *stubOrders, repo *stubRepo, catalog stubCatalog, notify *stubNotifier) *UseCase {
	return NewUseCase(
		Config{StateIDs: []string{"s1"}, PublicURL: "http://wh.local:8080/"},
		orders, repo, catalog, notify,
	)
}

// ── Тесты тика ─────────────────────────────────────────────────────────────

// Один заказ, две проблемы: весовой товар без резерва (нужно отложить) и
// штучный с неверным резервом (отложены неверно) — два сообщения с кнопкой
// «Подобрать» на страницу заказа.
func TestTickSendsMissingAndWrong(t *testing.T) {
	orders := &stubOrders{orders: []client.MSOrder{{ID: "order-1", Name: "19191"}}}
	orders.positions = map[string][]client.MSPosition{
		"order-1": {
			position(productWeightedHREF, "Рибай охл.", 0.657, 0),
			position(productPieceHREF, "Соус BBQ", 5, 2),
		},
	}
	catalog := stubCatalog{
		"w-prod": {ProductID: "w-prod", InternalCode: "10390021", Weighted: true},
		"p-prod": {ProductID: "p-prod", InternalCode: "10080001", Weighted: false},
	}
	repo := newStubRepo()
	notify := &stubNotifier{}
	uc := newUC(orders, repo, catalog, notify)

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(notify.sent) != 2 {
		t.Fatalf("сообщений: %d, ждали 2", len(notify.sent))
	}

	missing := notify.sent[0]
	if !strings.HasPrefix(missing.text, "В заказ 19191 нужно отложить:") {
		t.Errorf("текст missing: %q", missing.text)
	}
	if !strings.Contains(missing.text, "Рибай охл. — 0.657 кг") {
		t.Errorf("missing не содержит весовую позицию: %q", missing.text)
	}
	if missing.buttonText != "Подобрать" || missing.buttonURL != "http://wh.local:8080/ms/orders/order-1" {
		t.Errorf("кнопка missing: %q %q", missing.buttonText, missing.buttonURL)
	}

	wrong := notify.sent[1]
	if !strings.HasPrefix(wrong.text, "В заказе 19191 позиции отложены неверно:") {
		t.Errorf("текст wrong: %q", wrong.text)
	}
	if !strings.Contains(wrong.text, "Соус BBQ — нужно 5 шт, отложено 2 шт") {
		t.Errorf("wrong не содержит штучную позицию: %q", wrong.text)
	}

	if len(repo.notices) != 2 {
		t.Fatalf("записей в репо: %d, ждали 2", len(repo.notices))
	}
}

// Тот же тик повторно: активные записи гасят повторную отправку.
func TestTickDedupSilentOnSameProblem(t *testing.T) {
	orders := &stubOrders{orders: []client.MSOrder{{ID: "order-1", Name: "19191"}}}
	orders.positions = map[string][]client.MSPosition{
		"order-1": {position(productPieceHREF, "Соус BBQ", 5, 0)},
	}
	catalog := stubCatalog{"p-prod": {ProductID: "p-prod", InternalCode: "10080001", Weighted: false}}
	repo := newStubRepo()
	notify := &stubNotifier{}
	uc := newUC(orders, repo, catalog, notify)

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(notify.sent) != 1 {
		t.Fatalf("первый тик: сообщений %d, ждали 1", len(notify.sent))
	}

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(notify.sent) != 1 {
		t.Errorf("второй тик дослал сообщение: %d (дедуп не сработал)", len(notify.sent))
	}
	if len(notify.deleted) != 0 {
		t.Errorf("второй тик удалил сообщения: %v", notify.deleted)
	}
}

// Проблема исчезла (позиции исправлены): сообщение удаляется из чата,
// запись закрывается. Вернулась — уведомление уходит снова.
func TestTickCloseOnRecoveryAndResendOnReturn(t *testing.T) {
	positions := map[string][]client.MSPosition{
		"order-1": {position(productPieceHREF, "Соус BBQ", 5, 0)},
	}
	orders := &stubOrders{orders: []client.MSOrder{{ID: "order-1", Name: "19191"}}, positions: positions}
	catalog := stubCatalog{"p-prod": {ProductID: "p-prod", InternalCode: "10080001", Weighted: false}}
	repo := newStubRepo()
	notify := &stubNotifier{}
	uc := newUC(orders, repo, catalog, notify)

	// Тик 1: проблема есть — уведомили.
	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(repo.notices) != 1 {
		t.Fatalf("записей после тика 1: %d", len(repo.notices))
	}

	// Тик 2: резерв выставлен — сообщение удаляется, запись закрывается.
	orders.positions["order-1"] = []client.MSPosition{position(productPieceHREF, "Соус BBQ", 5, 5)}
	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(notify.deleted) != 1 {
		t.Errorf("после выздоровления сообщение не удалено: %v", notify.deleted)
	}
	if len(repo.notices) != 0 {
		t.Errorf("запись не закрыта: %d", len(repo.notices))
	}

	// Тик 3: проблема вернулась — новое уведомление.
	orders.positions["order-1"] = []client.MSPosition{position(productPieceHREF, "Соус BBQ", 5, 0)}
	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if len(notify.sent) != 2 {
		t.Errorf("после возврата проблемы уведомление не ушло: sent=%d", len(notify.sent))
	}
	if len(repo.notices) != 1 {
		t.Errorf("запись после возврата не создана: %d", len(repo.notices))
	}
}

// Заказ вышел из листа (статус сменился): проблема закрывается.
func TestTickCloseWhenOrderLeftWindow(t *testing.T) {
	orders := &stubOrders{orders: []client.MSOrder{{ID: "order-1", Name: "19191"}}}
	orders.positions = map[string][]client.MSPosition{
		"order-1": {position(productPieceHREF, "Соус BBQ", 5, 0)},
	}
	catalog := stubCatalog{"p-prod": {ProductID: "p-prod", InternalCode: "10080001", Weighted: false}}
	repo := newStubRepo()
	notify := &stubNotifier{}
	uc := newUC(orders, repo, catalog, notify)

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	orders.orders = nil // заказ больше не в окне
	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(notify.deleted) != 1 || len(repo.notices) != 0 {
		t.Errorf("заказ вне окна не закрыт: deleted=%v notices=%d", notify.deleted, len(repo.notices))
	}
}

// Товары не из каталога (без internal_code) в проверку не идут; строки с
// quantity == 0 проблем не создают; весовой с равным резервом — здоров
// (сверка в граммах, float-хвосты не мешают).
func TestTickSkipsNonCatalogAndHealthy(t *testing.T) {
	orders := &stubOrders{orders: []client.MSOrder{{ID: "order-1", Name: "19191"}}}
	orders.positions = map[string][]client.MSPosition{
		"order-1": {
			position("https://api.moysklad.ru/api/remap/1.2/entity/product/unknown", "Не наш товар", 3, 0),
			position(productPieceHREF, "Соус BBQ", 0, 0),
			position(productWeightedHREF, "Рибай охл.", 0.657, 0.657),
		},
	}
	catalog := stubCatalog{
		"w-prod": {ProductID: "w-prod", InternalCode: "10390021", Weighted: true},
		"p-prod": {ProductID: "p-prod", InternalCode: "10080001", Weighted: false},
	}
	repo := newStubRepo()
	notify := &stubNotifier{}
	uc := newUC(orders, repo, catalog, notify)

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(notify.sent) != 0 {
		t.Errorf("сообщений: %d, ждали 0 (%v)", len(notify.sent), notify.sent)
	}
}

// ── Тексты сообщений ───────────────────────────────────────────────────────

func TestMessageTextFormats(t *testing.T) {
	uc := newUC(&stubOrders{}, newStubRepo(), stubCatalog{}, &stubNotifier{})

	missing := uc.messageText("19191", reservewatch.KindMissing, []problemItem{
		{name: "Рибай охл.", quantity: 657, unit: qtyGrams},
		{name: "Соус BBQ", quantity: 3, unit: qtyPieces},
	})
	wantMissing := "В заказ 19191 нужно отложить:\n— Рибай охл. — 0.657 кг\n— Соус BBQ — 3 шт"
	if missing != wantMissing {
		t.Errorf("missing:\n got %q\nwant %q", missing, wantMissing)
	}

	wrong := uc.messageText("19191", reservewatch.KindWrong, []problemItem{
		{name: "Соус BBQ", quantity: 5, reserve: 2, unit: qtyPieces},
	})
	wantWrong := "В заказе 19191 позиции отложены неверно:\n— Соус BBQ — нужно 5 шт, отложено 2 шт"
	if wrong != wantWrong {
		t.Errorf("wrong:\n got %q\nwant %q", wrong, wantWrong)
	}
}

// Весовая строка «заглушка» 0.001 кг (ожидание единицы при подборе) без
// резерва — это «нужно отложить» (сверка в граммах: 1 г != 0 г).
func TestTickWeightedStubMissing(t *testing.T) {
	orders := &stubOrders{orders: []client.MSOrder{{ID: "order-1", Name: "19191"}}}
	orders.positions = map[string][]client.MSPosition{
		"order-1": {position(productWeightedHREF, "Рибай охл.", 0.001, 0)},
	}
	catalog := stubCatalog{"w-prod": {ProductID: "w-prod", InternalCode: "10390021", Weighted: true}}
	repo := newStubRepo()
	notify := &stubNotifier{}
	uc := newUC(orders, repo, catalog, notify)

	if err := uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(notify.sent) != 1 {
		t.Fatalf("сообщений: %d, ждали 1", len(notify.sent))
	}
	if !strings.Contains(notify.sent[0].text, "Рибай охл. — 0.001 кг") {
		t.Errorf("текст: %q", notify.sent[0].text)
	}
}

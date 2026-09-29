package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/sitecheck"
)

// testClock — часы модуля в тестах: время задаётся тестом и меняется вручную.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

// fetchResult — ответ фида в очереди (тело или ошибка запроса).
type fetchResult struct {
	body string
	err  error
}

// fakeFetcher — шов фида: снимает ответы из очереди по одному вызову. Очередь
// кончилась — повторяем последний ответ (поллер вправе спросить больше раз, чем
// подготовлено).
type fakeFetcher struct {
	queue []fetchResult
	calls int
}

func (f *fakeFetcher) FetchFeed(_ context.Context) ([]byte, error) {
	if len(f.queue) == 0 {
		return nil, errors.New("фид не подготовлен")
	}
	i := f.calls
	f.calls++
	if i >= len(f.queue) {
		i = len(f.queue) - 1
	}
	if f.queue[i].err != nil {
		return nil, f.queue[i].err
	}

	return []byte(f.queue[i].body), nil
}

type fakeCatalog struct {
	targets []sitecheck.Target
	err     error
	calls   int
}

func (c *fakeCatalog) SiteCheckTargets(_ context.Context) ([]sitecheck.Target, error) {
	c.calls++

	return c.targets, c.err
}

type fakeStock struct {
	positions []sitecheck.Position
	err       error
}

func (s *fakeStock) SiteCheckPositions(_ context.Context) ([]sitecheck.Position, error) {
	return s.positions, s.err
}

// fakeTasks — задачи общего канала: «вид: текст» по порядку открытия.
type fakeTasks struct {
	opened []string
	err    error
}

func (t *fakeTasks) Open(_ context.Context, kind domain.TaskKind, text string) error {
	if t.err != nil {
		return t.err
	}
	t.opened = append(t.opened, fmt.Sprintf("%s: %s", kind, text))

	return nil
}

// fakeWarehouse — сообщения в чат склада (сбой проверки).
type fakeWarehouse struct{ texts []string }

func (w *fakeWarehouse) NotifyWarehouse(text string) error {
	w.texts = append(w.texts, text)

	return nil
}

// harness — собранный модуль с фейковыми швами.
type harness struct {
	uc      *UseCase
	clock   *testClock
	feed    *fakeFetcher
	catalog *fakeCatalog
	stock   *fakeStock
	tasks   *fakeTasks
	wh      *fakeWarehouse
}

func newHarness(now time.Time, maxAttempts int) *harness {
	h := &harness{
		clock:   &testClock{t: now},
		feed:    &fakeFetcher{},
		catalog: &fakeCatalog{},
		stock:   &fakeStock{},
		tasks:   &fakeTasks{},
		wh:      &fakeWarehouse{},
	}
	h.uc = NewUseCase(
		Config{PollInterval: 30 * time.Second, MaxAttempts: maxAttempts, Now: h.clock.now},
		h.feed, h.catalog, h.stock, h.tasks, h.wh,
	)

	return h
}

// feedBody — фид на дату с одной позицией (url, price, oldprice, available).
func feedBody(date string) string {
	return `<?xml version="1.0"?><yml_catalog date="` + date + `"><shop><offers>` +
		`<offer id="1" available="true"><url>https://www.steakhome.ru/catalog/element/ribeye/</url>` +
		`<price>800</price><oldprice>1000</oldprice><currencyId>RUB</currencyId></offer>` +
		`</offers></shop></yml_catalog>`
}

// target — позиция каталога с url фида (скидка по базе 20 %, остаток есть).
func withPosition(h *harness, inStock bool, discount *int16) {
	h.catalog.targets = []sitecheck.Target{{
		ProductID: "p1", Name: "Рибай", SiteURL: "https://steakhome.ru/catalog/element/ribeye/",
	}}
	h.stock.positions = []sitecheck.Position{{ProductID: "p1", InStock: inStock, Discount: discount}}
}

func TestTickSkipsOutsideWindow(t *testing.T) {
	// 10:31 — окно часа (30 мин) уже истекло: фид сайт к этому времени собрал бы,
	// опрашивать нечего до следующего часа.
	h := newHarness(time.Date(2026, 9, 29, 10, 31, 0, 0, time.Local), 60)
	h.feed.queue = []fetchResult{{body: feedBody("2026-09-29 10:00")}}

	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if h.feed.calls != 0 {
		t.Errorf("запросов фида: %d, want 0 (вне окна часа)", h.feed.calls)
	}
}

func TestTickProcessesFreshFeedOnce(t *testing.T) {
	h := newHarness(time.Date(2026, 9, 29, 10, 0, 30, 0, time.Local), 60)
	d := int16(20)
	withPosition(h, true, &d)
	// Первый ответ — фид прошлого часа (сайт ещё не пересобрал), второй — свежий.
	h.feed.queue = []fetchResult{
		{body: feedBody("2026-09-29 09:23")},
		{body: feedBody("2026-09-29 10:07")},
	}

	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if h.feed.calls != 1 || len(h.tasks.opened) != 0 {
		t.Fatalf("несвежий фид: запросов %d, задач %v — want 1/пусто", h.feed.calls, h.tasks.opened)
	}

	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if h.feed.calls != 2 {
		t.Fatalf("запросов фида: %d, want 2", h.feed.calls)
	}
	if len(h.tasks.opened) != 0 {
		t.Errorf("расхождений нет, а задачи открыты: %v", h.tasks.opened)
	}

	// Свежий фид разошёлся со базой — сверка идёт, но час уже закрыт: в том же
	// часе фид больше не запрашивается.
	h.stock.positions = []sitecheck.Position{{ProductID: "p1", InStock: true}}
	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if h.feed.calls != 2 {
		t.Errorf("час выполнен, а фид запрошен снова: запросов %d, want 2", h.feed.calls)
	}
}

func TestTickReportsMismatchesAfterFreshFeed(t *testing.T) {
	h := newHarness(time.Date(2026, 9, 29, 10, 0, 30, 0, time.Local), 60)
	h.feed.queue = []fetchResult{{body: feedBody("2026-09-29 10:00")}}
	h.catalog.targets = []sitecheck.Target{
		{ProductID: "p1", Name: "Рибай", SiteURL: "https://steakhome.ru/catalog/element/ribeye/"},
		{ProductID: "p2", Name: "Колбаса", SiteURL: "https://www.steakhome.ru/catalog/element/kolyasa/"},
	}
	h.stock.positions = []sitecheck.Position{
		{ProductID: "p1", InStock: true},                           // скидка на сайте 20 %, по базе нет
		{ProductID: "p2", InStock: true, Discount: new(int16(10))}, // нет в фиде, остаток есть
	}

	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	want := []string{
		"site_discount: Рибай: Не поменяли скидку на сайте",
		"site_return: Колбаса не вернули на сайт",
	}
	if strings.Join(h.tasks.opened, " | ") != strings.Join(want, " | ") {
		t.Errorf("задачи: %v, want %v", h.tasks.opened, want)
	}
}

func TestTickWarnsWarehouseAfterAttemptLimit(t *testing.T) {
	// Лимит 3 попытки (окно 30 с × 3 = 1,5 мин → минута): свежего фида нет —
	// одно сообщение в чат склада и стоп до следующего часа.
	h := newHarness(time.Date(2026, 9, 29, 10, 0, 30, 0, time.Local), 3)
	h.feed.queue = []fetchResult{{body: feedBody("2026-09-29 09:00")}}

	for i := 1; i <= 3; i++ {
		if err := h.uc.tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if h.feed.calls != 3 {
		t.Fatalf("запросов фида: %d, want 3 (лимит попыток)", h.feed.calls)
	}
	if len(h.wh.texts) != 1 {
		t.Fatalf("сообщений складу: %d, want 1", len(h.wh.texts))
	}
	if !strings.Contains(h.wh.texts[0], "фид не обновился за 3 попыток") {
		t.Errorf("текст сообщения складу: %q", h.wh.texts[0])
	}

	// После предупреждения опрос в этом часе не продолжается.
	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick после предупреждения: %v", err)
	}
	if h.feed.calls != 3 || len(h.wh.texts) != 1 {
		t.Errorf("после предупреждения: запросов %d, сообщений %d — want 3/1", h.feed.calls, len(h.wh.texts))
	}
}

func TestTickRetriesWhenCatalogFails(t *testing.T) {
	h := newHarness(time.Date(2026, 9, 29, 10, 0, 30, 0, time.Local), 60)
	h.feed.queue = []fetchResult{{body: feedBody("2026-09-29 10:00")}}
	withPosition(h, true, new(int16(20)))
	h.catalog.err = errors.New("pg down")

	if err := h.uc.tick(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка чтения каталога")
	}

	// Ошибка час не закрывает: следующий тик повторяет сверку по тому же свежему
	// фиду и доносит расхождения.
	h.catalog.err = nil
	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if h.feed.calls != 2 {
		t.Errorf("запросов фида: %d, want 2 (повтор после ошибки каталога)", h.feed.calls)
	}
	if len(h.tasks.opened) != 0 {
		t.Errorf("скидка совпала, задач быть не должно: %v", h.tasks.opened)
	}
}

func TestTickStartsNewCheckNextHour(t *testing.T) {
	h := newHarness(time.Date(2026, 9, 29, 10, 0, 30, 0, time.Local), 60)
	d := int16(20)
	withPosition(h, true, &d)
	h.feed.queue = []fetchResult{
		{body: feedBody("2026-09-29 10:00")},
		{body: feedBody("2026-09-29 11:02")},
	}

	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 10:00: %v", err)
	}

	// Следующий час — новый цикл проверки: фид запрашивается снова. Скидка та же
	// (расхождение по скидке молчит), а остатка нет — позицию не убрали с сайта.
	h.clock.t = time.Date(2026, 9, 29, 11, 0, 30, 0, time.Local)
	h.stock.positions = []sitecheck.Position{{ProductID: "p1", InStock: false, Discount: &d}}
	if err := h.uc.tick(context.Background()); err != nil {
		t.Fatalf("tick 11:00: %v", err)
	}
	if h.feed.calls != 2 {
		t.Fatalf("запросов фида: %d, want 2 (новый час)", h.feed.calls)
	}

	want := []string{"site_remove: Рибай не убрали с сайта"}
	if strings.Join(h.tasks.opened, " | ") != strings.Join(want, " | ") {
		t.Errorf("задачи нового часа: %v, want %v", h.tasks.opened, want)
	}
}

func TestTickSurvivesFeedErrors(t *testing.T) {
	h := newHarness(time.Date(2026, 9, 29, 10, 0, 30, 0, time.Local), 5)
	h.feed.queue = []fetchResult{
		{err: errors.New("connection refused")},
		{body: "<yml_catalog date="},
		{body: feedBody("2026-09-29 10:11")},
	}
	d := int16(20)
	withPosition(h, true, &d)

	for i := 1; i <= 3; i++ {
		if err := h.uc.tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if h.feed.calls != 3 {
		t.Fatalf("запросов фида: %d, want 3 (ошибки опрос не останавливают)", h.feed.calls)
	}
	if len(h.wh.texts) != 0 {
		t.Errorf("лимит не исчерпан, сообщений складу быть не должно: %v", h.wh.texts)
	}
}

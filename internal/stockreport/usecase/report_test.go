package usecase

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/stock"
)

// msk — зона склада (UTC+3): время отчёта считается по локальному времени
// процесса, time.Local в тестах запрещён линтером.
var msk = time.FixedZone("MSK", 3*60*60)

// day — дата склада.
func day(y int, m time.Month, d, hour, minute int) time.Time {
	return time.Date(y, m, d, hour, minute, 0, 0, msk)
}

// fakeStock — кэш остатков: товар → партии.
type fakeStock struct {
	products []stock.Product
}

func (f *fakeStock) Snapshot() []stock.Product { return f.products }

// fakeSales — средние продажи за неделю; requested фиксирует, какие товары
// спросили (отчёт обязан спрашивать весь каталог, включая позиции без остатка).
type fakeSales struct {
	avg       map[string]float64
	err       error
	requested []string
}

func (f *fakeSales) WeeklyAverages(_ context.Context, ids []string) (map[string]float64, error) {
	f.requested = append([]string{}, ids...)
	if f.err != nil {
		return nil, f.err
	}

	return f.avg, nil
}

// fakeNotifier — чат управления: копит тексты, умеет отказывать.
type fakeNotifier struct {
	texts []string
	fail  bool
}

func (f *fakeNotifier) NotifyWarehouseManagement(text string) error {
	if f.fail {
		return errors.New("telegram недоступен")
	}
	f.texts = append(f.texts, text)

	return nil
}

// fixture — каталог из четырёх позиций, по одной на каждый список отчёта.
func fixture() (*fakeStock, *fakeSales) {
	st := &fakeStock{products: []stock.Product{
		{ID: "p1", Name: "Молоко", Lots: []stock.Lot{
			{BestBefore: day(2026, time.November, 20, 0, 0), Qty: 2},
			{BestBefore: day(2026, time.November, 25, 0, 0), Qty: 3},
		}},
		{ID: "p2", Name: "Сыр", Lots: []stock.Lot{
			{BestBefore: day(2026, time.November, 20, 0, 0), Qty: 2},
		}},
		{ID: "p3", Name: "Говядина", Lots: []stock.Lot{
			{BestBefore: day(2026, time.December, 1, 0, 0), Qty: 10},
		}},
		{ID: "p4", Name: "Новинка", Lots: []stock.Lot{
			{BestBefore: day(2026, time.December, 1, 0, 0), Qty: 7},
		}},
		{ID: "p5", Name: "Пусто"},
	}}

	// p1 — «середина»: остаток больше недельной нормы, но меньше двух норм —
	// в отчёт не попадает (проверяется отдельным тестом ниже).
	sales := &fakeSales{avg: map[string]float64{"p1": 4, "p2": 2.5, "p3": 3}}

	return st, sales
}

// reportTime — 16:00, как в конфиге по умолчанию.
const reportTime = 16 * time.Hour

func TestTickSendsThreeMessages(t *testing.T) {
	st, sales := fixture()
	notify := &fakeNotifier{}
	uc := New(st, sales, notify)
	uc.now = func() time.Time { return day(2026, time.November, 11, 16, 0) }

	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Fatalf("сообщений = %d, want 3: %q", len(notify.texts), notify.texts)
	}

	want := []string{
		"Пусто - нет в наличии",
		"Сыр - в наличии: 2 - среднее в неделю: 2.5",
		"Говядина - в наличии: 10 - запас на 3.3 недель\nНовинка - нет истории продаж",
	}
	for i, w := range want {
		if notify.texts[i] != w {
			t.Errorf("сообщение %d:\n got  %q\n want %q", i+1, notify.texts[i], w)
		}
	}

	// Спросили средние по ВСЕМУ каталогу: позиция без остатка тоже в отчёте.
	if len(sales.requested) != 5 {
		t.Errorf("запрошено средних по %d товарам, want 5: %v", len(sales.requested), sales.requested)
	}
}

func TestTickBeforeTimeDoesNothing(t *testing.T) {
	st, sales := fixture()
	notify := &fakeNotifier{}
	uc := New(st, sales, notify)
	uc.now = func() time.Time { return day(2026, time.November, 11, 15, 59) }

	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 0 {
		t.Errorf("до 16:00 отправлено %q", notify.texts)
	}
}

func TestTickOncePerDay(t *testing.T) {
	st, sales := fixture()
	notify := &fakeNotifier{}
	uc := New(st, sales, notify)

	now := day(2026, time.November, 11, 16, 5)
	uc.now = func() time.Time { return now }

	uc.Tick(context.Background(), reportTime)
	uc.Tick(context.Background(), reportTime)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Fatalf("сообщений = %d, want 3 (один проход в сутки): %q", len(notify.texts), notify.texts)
	}

	// Следующий день — новый отчёт.
	now = day(2026, time.November, 12, 16, 5)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 6 {
		t.Errorf("сообщений после второго дня = %d, want 6", len(notify.texts))
	}
}

func TestTickSkipsEmptyLists(t *testing.T) {
	st := &fakeStock{products: []stock.Product{{ID: "p1", Name: "Молоко", Lots: []stock.Lot{
		{BestBefore: day(2026, time.November, 20, 0, 0), Qty: 4},
	}}}}
	sales := &fakeSales{avg: map[string]float64{"p1": 3}}
	notify := &fakeNotifier{}
	uc := New(st, sales, notify)
	uc.now = func() time.Time { return day(2026, time.November, 11, 16, 0) }

	uc.Tick(context.Background(), reportTime)

	// Остаток больше недельной нормы, но меньше двух — позиция никуда не попала,
	// отправлять нечего.
	if len(notify.texts) != 0 {
		t.Errorf("отправлено %q, want ничего", notify.texts)
	}
}

func TestTickRetriesUndelivered(t *testing.T) {
	st, sales := fixture()
	notify := &fakeNotifier{fail: true}
	uc := New(st, sales, notify)
	now := day(2026, time.November, 11, 16, 0)
	uc.now = func() time.Time { return now }

	uc.Tick(context.Background(), reportTime)
	if len(notify.texts) != 0 {
		t.Fatalf("при отказе Telegram что-то доставлено: %q", notify.texts)
	}

	// Telegram ожил: очередь доезжает на следующем тике, отчёт НЕ пересобирается
	// (те же три сообщения, а не шесть).
	notify.fail = false
	now = day(2026, time.November, 11, 16, 1)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Fatalf("после восстановления сообщений = %d, want 3: %q", len(notify.texts), notify.texts)
	}
}

func TestTickSalesErrorRetriesNextTick(t *testing.T) {
	st, sales := fixture()
	sales.err = errors.New("БД недоступна")
	notify := &fakeNotifier{}
	uc := New(st, sales, notify)
	now := day(2026, time.November, 11, 16, 0)
	uc.now = func() time.Time { return now }

	uc.Tick(context.Background(), reportTime)
	if len(notify.texts) != 0 {
		t.Fatalf("при ошибке БД что-то отправлено: %q", notify.texts)
	}

	// Ошибка не закрыла день: следующий тик собирает отчёт заново.
	sales.err = nil
	now = day(2026, time.November, 11, 16, 1)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Errorf("после восстановления БД сообщений = %d, want 3", len(notify.texts))
	}
}

func TestMessagePartsSplitsLongList(t *testing.T) {
	lines := make([]string, 0, 400)
	for i := range 400 {
		lines = append(lines, "Очень длинное название позиции номер "+strconv.Itoa(i)+" - нет в наличии")
	}

	parts := messageParts(lines, "Нет в наличии")
	if len(parts) < 2 {
		t.Fatalf("частей = %d, want больше одной", len(parts))
	}

	var restored int
	for i, p := range parts {
		if len(p) > messageMaxLen {
			t.Errorf("часть %d: %d байт, лимит %d", i+1, len(p), messageMaxLen)
		}
		if !strings.Contains(p, "(часть ") {
			t.Errorf("часть %d без заголовка: %q", i+1, p)
		}
		restored += len(strings.Split(p, "\n")) - 1
	}

	if restored != len(lines) {
		t.Errorf("строк в частях = %d, want %d", restored, len(lines))
	}
}

func TestMessagePartsSingleMessageWithoutHeader(t *testing.T) {
	parts := messageParts([]string{"Сыр - в наличии: 2 - среднее в неделю: 2.5"}, "Нет в наличии")

	if len(parts) != 1 {
		t.Fatalf("частей = %d, want 1", len(parts))
	}
	if parts[0] != "Сыр - в наличии: 2 - среднее в неделю: 2.5" {
		t.Errorf("одиночное сообщение = %q, want без заголовка", parts[0])
	}
}

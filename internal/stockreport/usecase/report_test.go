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

// day — момент времени в 2026 году по зоне склада (год фиксирован: тесты
// проверяют время суток, а не календарь).
func day(m time.Month, d, hour, minute int) time.Time {
	return time.Date(2026, m, d, hour, minute, 0, 0, msk)
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

// fakeCoeff — коэффициенты текущего периода (модуль ordercoeff); requested —
// какие товары спросили.
type fakeCoeff struct {
	coeff     map[string]int16
	err       error
	requested []string
	at        time.Time
}

func (f *fakeCoeff) PeriodCoefficients(_ context.Context, ids []string, at time.Time) (map[string]int16, error) {
	f.requested = append([]string{}, ids...)
	f.at = at
	if f.err != nil {
		return nil, f.err
	}

	return f.coeff, nil
}

// fakeNotifier — чат управления: копит тексты, умеет отказывать.
type fakeNotifier struct {
	texts []string
	fail  bool
}

func (f *fakeNotifier) NotifyWarehouseManagementHTML(text string) error {
	if f.fail {
		return errors.New("telegram недоступен")
	}
	f.texts = append(f.texts, text)

	return nil
}

// fixture — каталог из пяти позиций: по одной на каждый список отчёта плюс
// «середина», которая никуда не попадает.
func fixture() (*fakeStock, *fakeSales, *fakeCoeff) {
	st := &fakeStock{products: []stock.Product{
		{ID: "p1", Name: "Молоко", Lots: []stock.Lot{
			{BestBefore: day(time.November, 20, 0, 0), Qty: 2},
			{BestBefore: day(time.November, 25, 0, 0), Qty: 3},
		}},
		{ID: "p2", Name: "Сыр", Lots: []stock.Lot{
			{BestBefore: day(time.November, 20, 0, 0), Qty: 2},
		}},
		{ID: "p3", Name: "Говядина", Lots: []stock.Lot{
			{BestBefore: day(time.December, 1, 0, 0), Qty: 10},
		}},
		{ID: "p4", Name: "Новинка", Lots: []stock.Lot{
			{BestBefore: day(time.December, 1, 0, 0), Qty: 7},
		}},
		{ID: "p5", Name: "Пусто"},
	}}

	// p1 — «середина»: остаток больше недельной нормы (5 > 4), коэффициент 0 —
	// в отчёт не попадает (проверяется отдельным тестом ниже).
	sales := &fakeSales{avg: map[string]float64{"p1": 4, "p2": 2.5, "p3": 3}}
	coeff := &fakeCoeff{coeff: map[string]int16{"p3": 2, "p4": 3}}

	return st, sales, coeff
}

// reportTime — 16:00, как в конфиге по умолчанию.
const reportTime = 16 * time.Hour

func TestTickSendsThreeMessages(t *testing.T) {
	st, sales, coeff := fixture()
	notify := &fakeNotifier{}
	uc := New(st, sales, coeff, notify)
	uc.now = func() time.Time { return day(time.November, 11, 16, 0) }

	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Fatalf("сообщений = %d, want 3: %q", len(notify.texts), notify.texts)
	}

	want := []string{
		"🟥 <b>НЕТ В НАЛИЧИИ · 1</b>\n\n<b>1. Пусто</b>",
		"🟨 <b>КОНЧАЕТСЯ · 1</b>\n\n<b>1. Сыр</b>\n<code>остаток 2 шт · среднее 2.5/нед</code>",
		"🟦 <b>ИЗБЫТОК · коэф. > 1 · 2</b>\n\n" +
			"<b>1. Говядина</b>\n<code>10 шт · запас 3.3 нед · коэф 2</code>\n\n" +
			"<b>2. Новинка</b>\n<code>7 шт · нет истории продаж · коэф 3</code>",
	}
	for i, w := range want {
		if notify.texts[i] != w {
			t.Errorf("сообщение %d:\n got  %q\n want %q", i+1, notify.texts[i], w)
		}
	}

	// Спросили средние и коэффициенты по ВСЕМУ каталогу: позиция без остатка
	// тоже в отчёте.
	if len(sales.requested) != 5 {
		t.Errorf("запрошено средних по %d товарам, want 5: %v", len(sales.requested), sales.requested)
	}
	if len(coeff.requested) != 5 {
		t.Errorf("запрошено коэффициентов по %d товарам, want 5: %v", len(coeff.requested), coeff.requested)
	}
	if !coeff.at.Equal(uc.now()) {
		t.Errorf("коэффициенты спрошены на %v, want момент отчёта %v", coeff.at, uc.now())
	}
}

func TestTickBeforeTimeDoesNothing(t *testing.T) {
	st, sales, coeff := fixture()
	notify := &fakeNotifier{}
	uc := New(st, sales, coeff, notify)
	uc.now = func() time.Time { return day(time.November, 11, 15, 59) }

	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 0 {
		t.Errorf("до 16:00 отправлено %q", notify.texts)
	}
}

func TestTickOncePerDay(t *testing.T) {
	st, sales, coeff := fixture()
	notify := &fakeNotifier{}
	uc := New(st, sales, coeff, notify)

	now := day(time.November, 11, 16, 5)
	uc.now = func() time.Time { return now }

	uc.Tick(context.Background(), reportTime)
	uc.Tick(context.Background(), reportTime)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Fatalf("сообщений = %d, want 3 (один проход в сутки): %q", len(notify.texts), notify.texts)
	}

	// Следующий день — новый отчёт.
	now = day(time.November, 12, 16, 5)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 6 {
		t.Errorf("сообщений после второго дня = %d, want 6", len(notify.texts))
	}
}

func TestTickSkipsEmptyLists(t *testing.T) {
	st := &fakeStock{products: []stock.Product{{ID: "p1", Name: "Молоко", Lots: []stock.Lot{
		{BestBefore: day(time.November, 20, 0, 0), Qty: 4},
	}}}}
	sales := &fakeSales{avg: map[string]float64{"p1": 3}}
	coeff := &fakeCoeff{}
	notify := &fakeNotifier{}
	uc := New(st, sales, coeff, notify)
	uc.now = func() time.Time { return day(time.November, 11, 16, 0) }

	uc.Tick(context.Background(), reportTime)

	// Остаток выше недельной нормы, коэффициент 0 — позиция никуда не попала,
	// отправлять нечего.
	if len(notify.texts) != 0 {
		t.Errorf("отправлено %q, want ничего", notify.texts)
	}
}

func TestTickRetriesUndelivered(t *testing.T) {
	st, sales, coeff := fixture()
	notify := &fakeNotifier{fail: true}
	uc := New(st, sales, coeff, notify)
	now := day(time.November, 11, 16, 0)
	uc.now = func() time.Time { return now }

	uc.Tick(context.Background(), reportTime)
	if len(notify.texts) != 0 {
		t.Fatalf("при отказе Telegram что-то доставлено: %q", notify.texts)
	}

	// Telegram ожил: очередь доезжает на следующем тике, отчёт НЕ пересобирается
	// (те же три сообщения, а не шесть).
	notify.fail = false
	now = day(time.November, 11, 16, 1)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Fatalf("после восстановления сообщений = %d, want 3: %q", len(notify.texts), notify.texts)
	}
}

func TestTickSalesErrorRetriesNextTick(t *testing.T) {
	st, sales, coeff := fixture()
	sales.err = errors.New("БД недоступна")
	notify := &fakeNotifier{}
	uc := New(st, sales, coeff, notify)
	now := day(time.November, 11, 16, 0)
	uc.now = func() time.Time { return now }

	uc.Tick(context.Background(), reportTime)
	if len(notify.texts) != 0 {
		t.Fatalf("при ошибке БД что-то отправлено: %q", notify.texts)
	}

	// Ошибка не закрыла день: следующий тик собирает отчёт заново.
	sales.err = nil
	now = day(time.November, 11, 16, 1)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Errorf("после восстановления БД сообщений = %d, want 3", len(notify.texts))
	}
}

func TestTickCoeffErrorRetriesNextTick(t *testing.T) {
	st, sales, coeff := fixture()
	coeff.err = errors.New("product_period_coeff недоступна")
	notify := &fakeNotifier{}
	uc := New(st, sales, coeff, notify)
	now := day(time.November, 11, 16, 0)
	uc.now = func() time.Time { return now }

	uc.Tick(context.Background(), reportTime)
	if len(notify.texts) != 0 {
		t.Fatalf("при ошибке чтения коэффициентов что-то отправлено: %q", notify.texts)
	}

	// Отчёт без коэффициентов не собирается вовсе: иначе блок «избыток» молча
	// оказался бы пустым и управление не увидело бы позиций.
	coeff.err = nil
	now = day(time.November, 11, 16, 1)
	uc.Tick(context.Background(), reportTime)

	if len(notify.texts) != 3 {
		t.Errorf("после восстановления сообщений = %d, want 3", len(notify.texts))
	}
}

func TestMessagePartsSplitsLongList(t *testing.T) {
	items := make([]string, 0, 400)
	for i := range 400 {
		items = append(items, "<b>"+strconv.Itoa(i+1)+". Очень длинное название позиции номер "+
			strconv.Itoa(i)+"</b>\n<code>10 шт · запас 3.3 нед · коэф 2</code>")
	}

	parts := messageParts(items, "🟥", "НЕТ В НАЛИЧИИ")
	if len(parts) < 2 {
		t.Fatalf("частей = %d, want больше одной", len(parts))
	}

	var restored int
	for i, p := range parts {
		if len(p) > messageMaxLen {
			t.Errorf("часть %d: %d байт, лимит %d", i+1, len(p), messageMaxLen)
		}
		if !strings.Contains(p, "часть ") {
			t.Errorf("часть %d без заголовка: %q", i+1, p)
		}
		body := strings.SplitN(p, "\n\n", 2)
		if len(body) != 2 {
			t.Fatalf("часть %d без тела: %q", i+1, p)
		}
		restored += len(strings.Split(body[1], "\n\n"))
	}

	if restored != len(items) {
		t.Errorf("блоков в частях = %d, want %d", restored, len(items))
	}
}

func TestMessagePartsSingleMessageWithHeader(t *testing.T) {
	parts := messageParts([]string{"<b>1. Сыр</b>\n<code>остаток 2 шт · среднее 2.5/нед</code>"},
		"🟨", "КОНЧАЕТСЯ")

	if len(parts) != 1 {
		t.Fatalf("частей = %d, want 1", len(parts))
	}
	want := "🟨 <b>КОНЧАЕТСЯ · 1</b>\n\n<b>1. Сыр</b>\n<code>остаток 2 шт · среднее 2.5/нед</code>"
	if parts[0] != want {
		t.Errorf("сообщение = %q, want %q", parts[0], want)
	}
}

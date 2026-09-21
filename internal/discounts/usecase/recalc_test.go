package usecase

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// recalcHarness — обвязка тестов расчётного цикла: фейковые швы и юзкейс на них.
// «БД» фейкового репозитория правит фейковый шов записи, поэтому следующий тик
// видит то, что записал расчёт (так проверяются последовательные тики).
type recalcHarness struct {
	uc     *UseCase
	repo   *fakeDiscountRepo
	writer *fakeDiscountWriter
	turn   *fakeTurnover
	common *fakeCommonNotifier
	now    time.Time
}

// newRecalcHarness — обвязка на момент now с парами входа теста.
func newRecalcHarness(now time.Time, inputs ...discounts.Input) *recalcHarness {
	repo := newFakeDiscountRepo(inputs...)
	writer := &fakeDiscountWriter{repo: repo}
	turn := &fakeTurnover{}
	common := &fakeCommonNotifier{}
	uc := NewUseCase(repo, turn, writer, common, nil, func() time.Time { return now })

	return &recalcHarness{uc: uc, repo: repo, writer: writer, turn: turn, common: common, now: now}
}

// batches — батчи правок, ушедшие в шов записи (пустой батч в шов не уходит).
func (h *recalcHarness) batches() [][]discounts.DiscountWrite { return h.writer.batches }

// flag — стоит ли маркер дня (день обнуляется, как это делает расчёт).
func (h *recalcHarness) flag(date time.Time, f discounts.DayFlag) bool {
	return h.repo.flags[fakeFlagKey(date, f)]
}

// turnover — сохранённый оборот товара в шве (Averages): то, что модуль средних
// продаж уже знает по данным БД. Значение — штук за период товара (месячный ряд:
// lotInput не выставляет TrackWeekly, то есть период 30 дней).
func (h *recalcHarness) turnover(pid string, v float64) {
	if h.turn.rates == nil {
		h.turn.rates = make(map[string]float64)
	}
	h.turn.rates[pid] = v
}

// recalcNow — утро дня теста (время суток у пересчётов не рассматривается).
func recalcNow(d int) time.Time { return day(d).Add(9 * time.Hour) }

// fakeDiscountRepo — репозиторий расчёта в памяти: «таблица» product_stock
// (её правит фейковый шов записи), маркеры дня и след вызовов.
type fakeDiscountRepo struct {
	inputs    []discounts.Input
	flags     map[string]bool
	loads     int
	lastToday time.Time
	loadErr   error
	flagErr   error
	markErr   error
}

func newFakeDiscountRepo(inputs ...discounts.Input) *fakeDiscountRepo {
	return &fakeDiscountRepo{inputs: inputs, flags: map[string]bool{}}
}

// fakeFlagKey — ключ маркера дня в фейке: день + маркер (как пара PK+колонка).
func fakeFlagKey(date time.Time, f discounts.DayFlag) string {
	return beginningOfDay(date).Format(time.DateOnly) + "|" + string(f)
}

// apply — правки расчёта ложатся в «БД» теста: general/telegram лота получают
// значение правки (nil — NULL), метка источника — как её передал расчёт.
func (r *fakeDiscountRepo) apply(writes []discounts.DiscountWrite) {
	for _, w := range writes {
		for i := range r.inputs {
			in := &r.inputs[i]
			if in.ProductID != w.ProductID || !beginningOfDay(in.BestBefore).Equal(beginningOfDay(w.BestBefore)) {
				continue
			}
			in.GeneralPlain = copyDiscount(w.General)
			in.TelegramPlain = copyDiscount(w.Telegram)
			in.DiscountSource = w.Source
		}
	}
}

// LoadDiscountInput — снапшот входа: копия пар теста (правки шва записи видны
// следующему вызову).
func (r *fakeDiscountRepo) LoadDiscountInput(_ context.Context, today time.Time) ([]discounts.Input, error) {
	r.loads++
	r.lastToday = today
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	inputs := make([]discounts.Input, len(r.inputs))
	copy(inputs, r.inputs)

	return inputs, nil
}

// DayFlagDone — сделан ли шаг дня: строки дня нет → false (не сделан).
func (r *fakeDiscountRepo) DayFlagDone(_ context.Context, date time.Time, f discounts.DayFlag) (bool, error) {
	if r.flagErr != nil {
		return false, r.flagErr
	}
	return r.flags[fakeFlagKey(date, f)], nil
}

// MarkDayFlag — отметить шаг дня сделанным (повторная отметка — no-op).
func (r *fakeDiscountRepo) MarkDayFlag(_ context.Context, date time.Time, f discounts.DayFlag) error {
	if r.markErr != nil {
		return r.markErr
	}
	r.flags[fakeFlagKey(date, f)] = true

	return nil
}

// Остальные методы репозитория пересчёту не нужны: тест падает явной ошибкой,
// а не молчаливым нулём, если расчёт в них полезет.
func (r *fakeDiscountRepo) SaveDigest(context.Context, discounts.DigestRecord, []discounts.DigestItem) error {
	return errRepoMethodUnused
}

func (r *fakeDiscountRepo) MarkDigestSent(context.Context, string, time.Time, time.Time) error {
	return errRepoMethodUnused
}

func (r *fakeDiscountRepo) LastDigestPairs(context.Context) (map[discounts.LotKey]struct{}, error) {
	return nil, errRepoMethodUnused
}

func (r *fakeDiscountRepo) TodaySlot(context.Context, time.Time) (map[discounts.LotKey]int16, error) {
	return nil, errRepoMethodUnused
}

func (r *fakeDiscountRepo) MarkGeneralRaised(context.Context, []discounts.LotKey, time.Time) error {
	return errRepoMethodUnused
}

// errRepoMethodUnused — метод репозитория, которого пересчёт не касается.
var errRepoMethodUnused = errors.New("фейк-репозиторий: метод вне расчётного цикла")

// fakeTurnover — шов оборота расчёта: сохранённый оборот (Averages) и свежий по
// кандидатам (RefreshCurrent) отдаёт заданная тестом карта; запросы и вызовы
// считаются раздельно (сохранённый спрашивают по всему входу, свежий — по парам
// с избытком и событиям стока).
type fakeTurnover struct {
	rates      map[string]float64 // сохранённый оборот товаров (Averages)
	freshRates map[string]float64 // свежий оборот кандидатов (nil — тот же, что сохранённый)
	avgErr     error              // ошибка сохранённого оборота
	err        error              // ошибка свежего оборота
	asked      []string
	avgAsked   []string
	calls      int
	avgCalls   int
}

func (t *fakeTurnover) RefreshCurrent(_ context.Context, productIDs []string) (map[string]float64, error) {
	t.calls++
	t.asked = append(t.asked, productIDs...)
	if t.err != nil {
		return nil, t.err
	}
	if t.freshRates != nil {
		return copyRates(t.freshRates), nil
	}
	return copyRates(t.rates), nil
}

// Averages — сохранённый оборот: тот же шов, но по всему входу расчёта, поэтому
// вызов считается отдельно от свежего.
func (t *fakeTurnover) Averages(_ context.Context, productIDs []string) (map[string]float64, error) {
	t.avgCalls++
	t.avgAsked = append(t.avgAsked, productIDs...)
	if t.avgErr != nil {
		return nil, t.avgErr
	}
	return copyRates(t.rates), nil
}

// copyRates — копия карты оборота: расчёт правит её свежими цифрами, а заданная
// тестом карта должна остаться такой, какой её задали (и не nil — как у шва).
func copyRates(rates map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(rates))
	maps.Copy(out, rates)

	return out
}

// RefreshWindow — полное окно оборотов расчётный цикл не обновляет.
func (t *fakeTurnover) RefreshWindow(context.Context, []string) (map[string]float64, error) {
	return nil, errors.New("фейк-оборот: окно в расчётном цикле не обновляется")
}

// fakeDiscountWriter — шов записи расчёта: батчи правок по порядку (в «БД»
// фейка их применяет repo.apply) и, по желанию теста, ошибка записи.
type fakeDiscountWriter struct {
	repo    *fakeDiscountRepo
	batches [][]discounts.DiscountWrite
	err     error
}

func (w *fakeDiscountWriter) SetDiscounts(_ context.Context, writes []discounts.DiscountWrite) error {
	if w.err != nil {
		return w.err
	}
	w.batches = append(w.batches, writes)
	w.repo.apply(writes)

	return nil
}

// fakeCommonNotifier — общий канал теста: тексты уведомлений по порядку
// (texts — отправленные, tries — все попытки) и, по желанию теста, ошибка
// отправки. chats — ответы в конкретный чат (команда /discounts), errDetails —
// ошибка такого ответа.
type fakeCommonNotifier struct {
	texts      []string
	tries      []string
	err        error
	chats      []chatMessage
	errDetails error
}

// chatMessage — попытка ответа в конкретный чат (чат + текст).
type chatMessage struct {
	chatID int64
	text   string
}

func (n *fakeCommonNotifier) NotifyCommon(_ context.Context, text string) error {
	n.tries = append(n.tries, text)
	if n.err != nil {
		return n.err
	}
	n.texts = append(n.texts, text)

	return nil
}

// SendDetails — ответ в конкретный чат: помнит все попытки, ошибку отдаёт по
// флагу теста. Удачные попытки отдельно не копим: команде важен сам факт ответа.
func (n *fakeCommonNotifier) SendDetails(_ context.Context, chatID int64, text string) error {
	n.chats = append(n.chats, chatMessage{chatID: chatID, text: text})
	if n.errDetails != nil {
		return n.errDetails
	}

	return nil
}

// lotInput — пара входа расчёта: товар, имя, срок годности, остаток и опции.
// TrackWeekly не выставляем — товар месячного ряда (период оборота 30 дней).
func lotInput(pid, name string, bestBefore time.Time, qty int64, opts ...func(*discounts.Input)) discounts.Input {
	in := discounts.Input{ProductID: pid, Name: name, BestBefore: bestBefore, Qty: qty}
	for _, opt := range opts {
		opt(&in)
	}
	return in
}

// shelfLifeInput — срок хранения товара (Г лестницы), дни.
func shelfLifeInput(v int16) func(*discounts.Input) {
	return func(in *discounts.Input) { in.ShelfLife = &v }
}

// manualInput — ручная скидка канала сайта (её пишет UI сроков).
func manualInput(v int16) func(*discounts.Input) {
	return func(in *discounts.Input) { in.GeneralManual = &v }
}

// plainInput — «простая» скидка канала сайта (её пишет движок расчёта).
func plainInput(v int16) func(*discounts.Input) {
	return func(in *discounts.Input) { in.GeneralPlain = &v }
}

// telegramInput — «простая» скидка ТГ-колонки лота.
func telegramInput(v int16) func(*discounts.Input) {
	return func(in *discounts.Input) { in.TelegramPlain = &v }
}

// sourceInput — метка источника plain-значения (product_stock.discount_source).
func sourceInput(s string) func(*discounts.Input) {
	return func(in *discounts.Input) { in.DiscountSource = s }
}

// Пересмотр лестницы идёт только в КТ-дни (вт/чт/сб): вне окна шаг не трогает
// ни записи, ни снапшот входа (одна и та же пара во все семь дней недели).
func TestRecalcExpiryOnlyOnExpiryDays(t *testing.T) {
	tests := []struct {
		title string
		now   time.Time
		want  bool
	}{
		{"понедельник", recalcNow(0), false},
		{"вторник", recalcNow(1), true},
		{"среда", recalcNow(2), false},
		{"четверг", recalcNow(3), true},
		{"пятница", recalcNow(4), false},
		{"суббота", recalcNow(5), true},
		{"воскресенье", recalcNow(6), false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			// Срок — за все семь дней недели (D > 0 у каждого дня):
			// проверяем окно КТ-дней, а не остаток дней.
			h := newRecalcHarness(tt.now, lotInput("p1", "Творог", day(10), 20, shelfLifeInput(30)))

			if err := h.uc.RecalcExpiry(context.Background(), h.now); err != nil {
				t.Fatalf("RecalcExpiry: %v", err)
			}

			if got := len(h.batches()) == 1; got != tt.want {
				t.Fatalf("запись: %v, want %v (батчей %d)", got, tt.want, len(h.batches()))
			}
			if !tt.want {
				if h.repo.loads != 0 {
					t.Errorf("вне КТ-дня снапшот не читаем, чтений %d", h.repo.loads)
				}
				if h.flag(h.now, discounts.FlagExpiry) {
					t.Error("вне КТ-дня маркер дня не ставим")
				}
				return
			}
			if !h.flag(h.now, discounts.FlagExpiry) {
				t.Error("после пересчёта маркер дня не отмечен")
			}
		})
	}
}

// Вторник с ростом ступени: правка уходит в шов записи (general, метка
// источника, сохранённое значение ТГ-колонки), день обнуляется до суток, а
// реестр показывает новое значение.
func TestRecalcExpiryWritesGrowth(t *testing.T) {
	now := recalcNow(1)
	h := newRecalcHarness(now,
		lotInput("p1", "Творог", day(5), 20,
			shelfLifeInput(30), telegramInput(15), sourceInput(discounts.SourceSurplus.String())))

	if err := h.uc.RecalcExpiry(context.Background(), h.now); err != nil {
		t.Fatalf("RecalcExpiry: %v", err)
	}

	if !h.repo.lastToday.Equal(beginningOfDay(now)) {
		t.Errorf("снапшот запрошен на %v, want %v", h.repo.lastToday, beginningOfDay(now))
	}

	batches := h.batches()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("батчи правок: %+v", batches)
	}
	w := batches[0][0]
	if w.ProductID != "p1" || w.General == nil || *w.General != 40 {
		t.Errorf("правка general: %+v, want p1 40%%", w)
	}
	if w.Telegram == nil || *w.Telegram != 15 {
		t.Errorf("ТГ-колонка правки: %v, want 15 (значение ТГ-дня не затираем)", w.Telegram)
	}
	if w.Source != discounts.SourceExpiry.String() {
		t.Errorf("метка источника %q, want %q", w.Source, discounts.SourceExpiry)
	}

	// «БД» теста и реестр видят записанное значение.
	if got := h.repo.inputs[0].GeneralPlain; got == nil || *got != 40 {
		t.Errorf("general в БД: %v, want 40", got)
	}
	window := h.uc.Window(12)
	if len(window) != 1 || window[0].Percent != 40 || window[0].Source != discounts.SourceExpiry {
		t.Errorf("окно реестра: %+v, want срок 40%%", window)
	}
}

// Повторный вызов в тот же день: маркер дня закрыт — снапшот не читается,
// правок нет.
func TestRecalcExpirySecondCallSameDay(t *testing.T) {
	now := recalcNow(1)
	h := newRecalcHarness(now, lotInput("p1", "Творог", day(5), 20, shelfLifeInput(30)))

	for i := range 2 {
		if err := h.uc.RecalcExpiry(context.Background(), h.now); err != nil {
			t.Fatalf("RecalcExpiry #%d: %v", i+1, err)
		}
	}
	if h.repo.loads != 1 {
		t.Errorf("снапшот читался %d раз, want 1", h.repo.loads)
	}
	if got := len(h.batches()); got != 1 {
		t.Errorf("батчей правок %d, want 1", got)
	}
}

// «Только вверх»: ступень ниже применённого или равная ему в правки не идёт,
// рост под ручной скидкой — идёт (ручная остаётся эффективной на сайте).
func TestRecalcExpiryKeepsHigherApplied(t *testing.T) {
	tests := []struct {
		title  string
		opts   []func(*discounts.Input)
		want   bool
		expect *int16
	}{
		{
			title: "пусто → ступень 40: пишем",
			want:  true, expect: new(int16(40)),
		},
		{
			title: "применено 50, ступень 40: тишина",
			opts:  []func(*discounts.Input){plainInput(50)},
		},
		{
			title: "применено 40, ступень 40: тишина",
			opts:  []func(*discounts.Input){plainInput(40)},
		},
		{
			title: "ручная 50, ступень 40: тишина",
			opts:  []func(*discounts.Input){manualInput(50)},
		},
		{
			title: "ручная 30 ниже ступени 40: пишем колонку движка",
			opts:  []func(*discounts.Input){manualInput(30)},
			want:  true, expect: new(int16(40)),
		},
		{
			title: "plain 10 и ручная 30, ступень 40: пишем",
			opts:  []func(*discounts.Input){plainInput(10), manualInput(30)},
			want:  true, expect: new(int16(40)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			opts := append([]func(*discounts.Input){shelfLifeInput(30)}, tt.opts...)
			// Четверг, D = 5 дней → ступень 40 % (не зависит от недели теста).
			h := newRecalcHarness(recalcNow(3), lotInput("p1", "Творог", day(8), 20, opts...))

			if err := h.uc.RecalcExpiry(context.Background(), h.now); err != nil {
				t.Fatalf("RecalcExpiry: %v", err)
			}

			batches := h.batches()
			if got := len(batches) == 1; got != tt.want {
				t.Fatalf("батчей правок %d, want %v", len(batches), tt.want)
			}
			if !tt.want {
				return
			}
			got := batches[0][0].General
			if got == nil || tt.expect == nil || *got != *tt.expect {
				t.Errorf("general правки %v, want %v", got, tt.expect)
			}
		})
	}
}

// Пары без ступени на сегодня (вне окна, срок не задан, партия просрочена) не
// пишутся вовсе: 0 и NULL в колонки скидок не попадают.
func TestRecalcExpiryNeverWritesOutsideWindow(t *testing.T) {
	h := newRecalcHarness(recalcNow(3),
		lotInput("p-out", "Молоко", day(100), 10, shelfLifeInput(30), plainInput(20)),
		lotInput("p-noshelf", "Сыр", day(5), 10, plainInput(20)),
		lotInput("p-expired", "Кефир", day(0), 10, shelfLifeInput(30)),
	)

	if err := h.uc.RecalcExpiry(context.Background(), h.now); err != nil {
		t.Fatalf("RecalcExpiry: %v", err)
	}
	if got := len(h.batches()); got != 0 {
		t.Fatalf("батчей правок %d, want 0: %+v", got, h.batches())
	}
}

// Ошибки швов не прячутся: снапшот, маркер дня и запись возвращают ошибку, а
// шаг дня без записи не закрывается (после сбоя пересчёт можно повторить).
func TestRecalcExpiryErrors(t *testing.T) {
	t.Run("снапшот", func(t *testing.T) {
		h := newRecalcHarness(recalcNow(1), lotInput("p1", "Творог", day(5), 20, shelfLifeInput(30)))
		h.repo.loadErr = errors.New("нет связи")

		if err := h.uc.RecalcExpiry(context.Background(), h.now); err == nil {
			t.Fatal("ошибка снапшота не вернулась")
		}
	})
	t.Run("маркер дня на чтении", func(t *testing.T) {
		h := newRecalcHarness(recalcNow(1), lotInput("p1", "Творог", day(5), 20, shelfLifeInput(30)))
		h.repo.flagErr = errors.New("нет связи")

		if err := h.uc.RecalcExpiry(context.Background(), h.now); err == nil {
			t.Fatal("ошибка маркера дня не вернулась")
		}
		if h.repo.loads != 0 {
			t.Errorf("после ошибки маркера снапшот не читаем, чтений %d", h.repo.loads)
		}
	})
	t.Run("запись", func(t *testing.T) {
		h := newRecalcHarness(recalcNow(1), lotInput("p1", "Творог", day(5), 20, shelfLifeInput(30)))
		h.writer.err = errors.New("нет связи")

		if err := h.uc.RecalcExpiry(context.Background(), h.now); err == nil {
			t.Fatal("ошибка записи не вернулась")
		}
		if h.flag(h.now, discounts.FlagExpiry) {
			t.Error("при сбое записи шаг дня не закрываем")
		}
	})
	t.Run("маркер дня на записи", func(t *testing.T) {
		h := newRecalcHarness(recalcNow(1), lotInput("p1", "Творог", day(5), 20, shelfLifeInput(30)))
		h.repo.markErr = errors.New("нет связи")

		if err := h.uc.RecalcExpiry(context.Background(), h.now); err == nil {
			t.Fatal("ошибка отметки шага дня не вернулась")
		}
	})
}

// Значение 0 в PLAIN-колонке читается как «скидки нет» (движок пишет NULL): нулевая
// ступень ничего не пишет.
func TestRecalcExpiryZeroDiscountIsNoDiscount(t *testing.T) {
	h := newRecalcHarness(recalcNow(1),
		lotInput("p1", "Творог", day(5), 20, shelfLifeInput(30), plainInput(0)),
	)

	if err := h.uc.RecalcExpiry(context.Background(), h.now); err != nil {
		t.Fatalf("RecalcExpiry: %v", err)
	}
	batches := h.batches()
	if len(batches) != 1 {
		t.Fatalf("батчей правок %d, want 1", len(batches))
	}
	if got := batches[0][0].General; got == nil || *got != 40 {
		t.Errorf("general правки %v, want 40 (нулевая скидка — пусто)", got)
	}
}

// Часовой пересчёт избытка: 10 % на пустом месте, метка источника, значение
// ТГ-колонки не затирается, маркер дня отмечается. Оборот даёт шов: сохранённый
// (Averages) по всему входу, затем свежий (RefreshCurrent) по паре с избытком —
// решение может уйти в любой момент.
func TestRecalcSurplusWritesTenAtExcess(t *testing.T) {
	h := newRecalcHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(10), 100, telegramInput(20)))
	h.turnover("p1", 30)

	if err := h.uc.RecalcSurplus(context.Background(), h.now); err != nil {
		t.Fatalf("RecalcSurplus: %v", err)
	}

	batches := h.batches()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("батчи правок: %+v", batches)
	}
	w := batches[0][0]
	if w.General == nil || *w.General != discounts.SurplusPercent() {
		t.Errorf("general правки %v, want %d", w.General, discounts.SurplusPercent())
	}
	if w.Source != discounts.SourceSurplus.String() {
		t.Errorf("метка источника %q, want %q", w.Source, discounts.SourceSurplus)
	}
	if w.Telegram == nil || *w.Telegram != 20 {
		t.Errorf("ТГ-колонка правки: %v, want 20 (значение ТГ-дня не затираем)", w.Telegram)
	}

	if !h.flag(h.now, discounts.FlagSurplus) {
		t.Error("после пересчёта маркер дня не отмечен")
	}
	if got := h.uc.Window(12); len(got) != 1 || got[0].Source != discounts.SourceSurplus {
		t.Errorf("окно реестра: %+v, want избыток", got)
	}
	if h.turn.avgCalls != 1 || len(h.turn.avgAsked) != 1 || h.turn.avgAsked[0] != "p1" {
		t.Errorf("сохранённый оборот запрошен %v (вызовов %d), want [p1]", h.turn.avgAsked, h.turn.avgCalls)
	}
	if h.turn.calls != 1 || len(h.turn.asked) != 1 || h.turn.asked[0] != "p1" {
		t.Errorf("свежий оборот запрошен %v (вызовов %d), want [p1]", h.turn.asked, h.turn.calls)
	}
}

// Условие избытка перестало выполняться (продажи ускорились) — 10 % снимается
// в NULL, метка источника уходит вместе со значением.
func TestRecalcSurplusClearsWhenExcessGone(t *testing.T) {
	h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
	h.turnover("p1", 30)
	ctx := context.Background()

	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus #1: %v", err)
	}

	// Продажи ускорились: модуль средних продаж знает про товар уже другой оборот
	// (сохранённый — из его таблиц, свежий — из МС) — избытка нет.
	h.turnover("p1", 400)

	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus #2: %v", err)
	}

	batches := h.batches()
	if len(batches) != 2 {
		t.Fatalf("батчей правок %d, want 2", len(batches))
	}
	if got := batches[1][0].General; got != nil {
		t.Errorf("снятие избытка: general правки %v, want nil (NULL, а не 0)", got)
	}
	if got := h.repo.inputs[0].GeneralPlain; got != nil {
		t.Errorf("general в БД: %v, want NULL", got)
	}
	if got := h.repo.inputs[0].DiscountSource; got != "" {
		t.Errorf("метка источника %q, want пусто (значение снято)", got)
	}
}

// Повторный тик того же часа: значение уже стоит — новых правок нет, но свежий
// оборот пары с избытком спрашивается каждый час (условие может уйти в любой момент).
func TestRecalcSurplusSecondTickIsSilent(t *testing.T) {
	h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
	h.turnover("p1", 30)
	ctx := context.Background()

	for i := range 2 {
		if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
			t.Fatalf("RecalcSurplus #%d: %v", i+1, err)
		}
	}
	if got := len(h.batches()); got != 1 {
		t.Errorf("батчей правок %d, want 1 (10 %% уже стоит)", got)
	}
	if len(h.turn.asked) != 2 {
		t.Errorf("свежий оборот запрошен %v, want пару и в первом, и во втором тике", h.turn.asked)
	}
	if h.turn.avgCalls != 2 {
		t.Errorf("сохранённый оборот запрошен %d раз, want 2 (каждый тик)", h.turn.avgCalls)
	}
}

// Чужие скидки избыток не трогает: ручная, ступень по сроку, подъём ТГ-дня
// (стоящее значение) и значение с чужой меткой источника.
func TestRecalcSurplusLeavesForeignDiscounts(t *testing.T) {
	tests := []struct {
		title string
		opts  []func(*discounts.Input)
	}{
		{"ручная 30", []func(*discounts.Input){manualInput(30)}},
		{"сроковая ступень 20 держит значение", []func(*discounts.Input){shelfLifeInput(30)}},
		{"подъём ТГ-дня: стоит 20 %", []func(*discounts.Input){plainInput(20)}},
		{"значение помечено сроком", []func(*discounts.Input){plainInput(20), sourceInput("expiry")}},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100, tt.opts...))
			h.turnover("p1", 30)

			// Пара действительно избыточна: иначе тест был бы пустым.
			pairs := Evaluate(h.repo.inputs, h.turn.rates, beginningOfDay(h.now))
			if len(pairs) != 1 || !pairs[0].HasSurplus {
				t.Fatalf("пара без избытка: %+v", pairs)
			}

			if err := h.uc.RecalcSurplus(context.Background(), h.now); err != nil {
				t.Fatalf("RecalcSurplus: %v", err)
			}
			if got := len(h.batches()); got != 0 {
				t.Fatalf("батчей правок %d, want 0: %+v", got, h.batches())
			}
		})
	}
}

// Избыток снимается и тогда, когда дорогу уступил ручной скидке: значение 10 %
// осталось с прошлого тика, а ручная скидка стоит уже своя.
func TestRecalcSurplusClearsOwnTenUnderManual(t *testing.T) {
	h := newRecalcHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(10), 100,
			plainInput(10), manualInput(30), sourceInput(discounts.SourceSurplus.String())))
	h.turnover("p1", 30)

	if err := h.uc.RecalcSurplus(context.Background(), h.now); err != nil {
		t.Fatalf("RecalcSurplus: %v", err)
	}
	batches := h.batches()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("батчи правок: %+v", batches)
	}
	if got := batches[0][0].General; got != nil {
		t.Errorf("снятие под ручной: general правки %v, want nil", got)
	}
}

// Свежий оборот меняет решение: сохранённого оборота нет, событие стока
// (MarkDirty) просит цифры из МС — и по ним появляется избыток.
func TestRecalcSurplusRefreshChangesDecision(t *testing.T) {
	h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
	h.turn.freshRates = map[string]float64{"p1": 30}
	h.uc.MarkDirty("p1")

	// По сохранённому обороту (пустому) избытка не было бы — решение меняет свежий.
	if pairs := Evaluate(h.repo.inputs, h.turn.rates, beginningOfDay(h.now)); pairs[0].HasSurplus {
		t.Fatalf("пара избыточна по пустому обороту: %+v", pairs)
	}

	if err := h.uc.RecalcSurplus(context.Background(), h.now); err != nil {
		t.Fatalf("RecalcSurplus: %v", err)
	}
	if len(h.turn.asked) != 1 || h.turn.asked[0] != "p1" {
		t.Fatalf("свежий оборот запрошен %v, want [p1]", h.turn.asked)
	}
	batches := h.batches()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("батчи правок: %+v", batches)
	}
	if got := batches[0][0].General; got == nil || *got != discounts.SurplusPercent() {
		t.Errorf("general правки %v, want %d", got, discounts.SurplusPercent())
	}
}

// Ошибка сохранённого оборота (Averages) часовой тик НЕ выполняет: без оборота
// все избытки выглядели бы снятыми, а снятие — это запись. Ошибка возвращается,
// записей нет, маркер дня не ставится.
func TestRecalcSurplusAveragesErrorStopsTick(t *testing.T) {
	h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
	h.turnover("p1", 30)
	h.turn.avgErr = errors.New("МС недоступен")

	if err := h.uc.RecalcSurplus(context.Background(), h.now); !errors.Is(err, h.turn.avgErr) {
		t.Fatalf("RecalcSurplus на ошибке сохранённого оборота: %v, want обёртку ошибки шва", err)
	}
	if got := len(h.batches()); got != 0 {
		t.Errorf("батчей правок %d, want 0 (тик не выполнен)", got)
	}
	if h.flag(h.now, discounts.FlagSurplus) {
		t.Error("при сбое оборота шаг дня не закрываем")
	}
	if h.turn.calls != 0 {
		t.Errorf("свежий оборот после ошибки сохранённого не спрашиваем, вызовов %d", h.turn.calls)
	}
}

// Ошибка свежего оборота (RefreshCurrent) тик не роняет: решение принимается по
// сохранённому обороту, свежие цифры догонит следующий час.
func TestRecalcSurplusRefreshErrorUsesSavedTurnover(t *testing.T) {
	h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
	h.turnover("p1", 30)
	h.turn.err = errors.New("МС недоступен")
	h.uc.MarkDirty("p1")

	if err := h.uc.RecalcSurplus(context.Background(), h.now); err != nil {
		t.Fatalf("тик избытка упал на ошибке свежего оборота: %v", err)
	}
	if len(h.turn.asked) != 1 || h.turn.asked[0] != "p1" {
		t.Fatalf("свежий оборот запрошен %v, want [p1]", h.turn.asked)
	}
	batches := h.batches()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("батчи правок: %+v, want 10 %% по сохранённому обороту", batches)
	}
	if got := batches[0][0].General; got == nil || *got != discounts.SurplusPercent() {
		t.Errorf("general правки %v, want %d", got, discounts.SurplusPercent())
	}
}

// Ошибки расчёта избытка не прячутся: снапшот, запись и маркер дня возвращают
// ошибку, шаг дня при сбое записи не закрывается.
func TestRecalcSurplusErrors(t *testing.T) {
	t.Run("снапшот", func(t *testing.T) {
		h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
		h.repo.loadErr = errors.New("нет связи")

		if err := h.uc.RecalcSurplus(context.Background(), h.now); err == nil {
			t.Fatal("ошибка снапшота не вернулась")
		}
	})
	t.Run("запись", func(t *testing.T) {
		h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
		h.turnover("p1", 30)
		h.writer.err = errors.New("нет связи")

		if err := h.uc.RecalcSurplus(context.Background(), h.now); err == nil {
			t.Fatal("ошибка записи не вернулась")
		}
		if h.flag(h.now, discounts.FlagSurplus) {
			t.Error("при сбое записи шаг дня не закрываем")
		}
	})
	t.Run("маркер дня", func(t *testing.T) {
		h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
		h.repo.markErr = errors.New("нет связи")

		if err := h.uc.RecalcSurplus(context.Background(), h.now); err == nil {
			t.Fatal("ошибка отметки шага дня не вернулась")
		}
	})
}

// Изменение эффективной скидки уходит людям: рост ступени — «поставить X %»
// в общий канал (уведомляем о том, что человеку надо сделать на сайте).
func TestRecalcExpiryNotifiesGrowth(t *testing.T) {
	h := newRecalcHarness(recalcNow(1),
		lotInput("p1", "Творог", day(5), 20, shelfLifeInput(30), manualInput(30)))
	ctx := context.Background()

	// Первый пересчёт процесса только наполняет реестр — уведомлений он не даёт.
	if err := h.uc.RecalcExpiry(ctx, h.now); err != nil {
		t.Fatalf("RecalcExpiry (наполнение): %v", err)
	}
	h.common.texts, h.common.tries = nil, nil

	// Ручную скидку менеджер снял: теперь на сайте решает ступень лестницы —
	// о росте движок сообщает человеку.
	h.repo.inputs[0].GeneralManual = nil
	delete(h.repo.flags, fakeFlagKey(beginningOfDay(h.now), discounts.FlagExpiry))

	if err := h.uc.RecalcExpiry(ctx, h.now); err != nil {
		t.Fatalf("RecalcExpiry: %v", err)
	}
	want := []string{"Творог (до 19.09): Необходимо поднять скидку до 40%"}
	if !reflect.DeepEqual(h.common.texts, want) {
		t.Errorf("уведомления %q, want %q", h.common.texts, want)
	}
}

// Два изменения избытка дают два уведомления, а тик без изменений молчит:
// «поставить 10 %» при появлении избытка и «убрать скидку» при его уходе.
func TestRecalcSurplusNotifiesChanges(t *testing.T) {
	h := newRecalcHarness(recalcNow(1), lotInput("p1", "Колбаса", day(10), 100))
	h.turnover("p1", 400)
	ctx := context.Background()

	// Наполнение реестра: продажи быстрые, избытка нет — уведомлений тоже нет.
	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.common.texts, h.common.tries = nil, nil

	// Продажи замедлились — появился избыток: движок ставит 10 % и сообщает.
	h.turnover("p1", 30)

	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus #1: %v", err)
	}
	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus #2: %v", err)
	}

	// Продажи ускорились — избыток ушёл.
	h.turnover("p1", 400)

	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus #3: %v", err)
	}

	want := []string{
		"Колбаса (до 24.09): Необходимо поставить скидку 10%",
		"Колбаса (до 24.09): Необходимо убрать скидку",
	}
	if !reflect.DeepEqual(h.common.texts, want) {
		t.Errorf("уведомления %q, want %q", h.common.texts, want)
	}
}

// Товар без данных о продажах избытка не получает (решение владельца): нулевой
// оборот и товар, которого в карте шва нет вовсе, — не повод ставить 10 %.
// Сохранённый оборот при этом спрашиваем по всему входу одним вызовом.
func TestRecalcSurplusNoSalesNoExcess(t *testing.T) {
	h := newRecalcHarness(recalcNow(1),
		lotInput("p-zero", "Колбаса", day(10), 100),
		lotInput("p-none", "Сыр", day(10), 100),
	)
	h.turnover("p-zero", 0)

	if err := h.uc.RecalcSurplus(context.Background(), h.now); err != nil {
		t.Fatalf("RecalcSurplus: %v", err)
	}
	if got := len(h.batches()); got != 0 {
		t.Fatalf("батчей правок %d, want 0: %+v", got, h.batches())
	}
	if h.turn.avgCalls != 1 || len(h.turn.avgAsked) != 2 {
		t.Errorf("сохранённый оборот запрошен %v (вызовов %d), want оба товара одним вызовом",
			h.turn.avgAsked, h.turn.avgCalls)
	}
	if h.turn.calls != 0 {
		t.Errorf("свежий оборот по парам без избытка не спрашиваем, вызовов %d", h.turn.calls)
	}
}

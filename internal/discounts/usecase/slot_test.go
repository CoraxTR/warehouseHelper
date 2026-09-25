package usecase

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// ТГ-день: план дня 14:00 (RunSlotPlan) и подъём general 16:00 (RunRaise), см.
// slot.go. «БД» фейкового репозитория правит фейковый шов записи, поэтому
// проверяем и то, что ушло в сток, и то, что легло в историю рассылок.

// fakeSlotRepo — репозиторий ТГ-дня: к обвязке пересчёта (вход, маркеры дня)
// добавляется история рассылок и слот дня. План дня отдаёт базовый фейк
// (TodaySlot): его заполняет либо SaveDigest (позиции отправленной рассылки),
// либо тест подъёма 16:00 — напрямую.
type fakeSlotRepo struct {
	*fakeDiscountRepo

	digests   []discounts.DigestRecord // сохранённые рассылки
	items     [][]discounts.DigestItem // позиции по рассылкам
	sent      int                      // отметок отправки
	lastPairs map[discounts.LotKey]struct{}
	raised    []discounts.LotKey
	saveErr   error
}

func (r *fakeSlotRepo) SaveDigest(_ context.Context, d discounts.DigestRecord, items []discounts.DigestItem) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.digests = append(r.digests, d)
	r.items = append(r.items, items)
	// Отправленный план становится планом дня: 16:00 работает по той же
	// рассылке, что собрал 14:00.
	r.plan = slotItems(items)

	return nil
}

func (r *fakeSlotRepo) MarkDigestSent(context.Context, string, time.Time, time.Time) error {
	r.sent++

	return nil
}

func (r *fakeSlotRepo) LastDigestPairs(context.Context) (map[discounts.LotKey]struct{}, error) {
	if r.lastPairs == nil {
		return map[discounts.LotKey]struct{}{}, nil
	}

	return r.lastPairs, nil
}

func (r *fakeSlotRepo) MarkGeneralRaised(_ context.Context, pairs []discounts.LotKey, _ time.Time) error {
	r.raised = append(r.raised, pairs...)

	return nil
}

// slotItems — план дня (SlotItem) из позиций сохранённой рассылки (DigestItem):
// скидка плана, причина и контроль продаж добора из избытка.
func slotItems(items []discounts.DigestItem) []discounts.SlotItem {
	out := make([]discounts.SlotItem, 0, len(items))
	for _, it := range items {
		out = append(out, discounts.SlotItem{
			ProductID: it.ProductID, BestBefore: beginningOfDay(it.BestBefore),
			Percent:    it.Percent,
			Reason:     it.Reason,
			InitialQty: it.InitialQty,
			PlanQty:    it.PlanQty,
		})
	}

	return out
}

// fakeWarehouseNotifier — чат склада: список позиций слота.
type fakeWarehouseNotifier struct {
	texts []string
}

func (n *fakeWarehouseNotifier) NotifyWarehouse(text string) error {
	n.texts = append(n.texts, text)

	return nil
}

// slotHarness — обвязка ТГ-дня: юзкейс, репозиторий истории, шов записи и чат склада.
type slotHarness struct {
	uc     *UseCase
	repo   *fakeSlotRepo
	writer *fakeDiscountWriter
	turn   *fakeTurnover
	chat   *fakeWarehouseNotifier
	common *fakeCommonNotifier
	tasks  *fakeTaskOpener
	now    time.Time
	today  time.Time
}

func newSlotHarness(now time.Time, inputs ...discounts.Input) *slotHarness {
	base := newFakeDiscountRepo(inputs...)
	repo := &fakeSlotRepo{fakeDiscountRepo: base}
	writer := &fakeDiscountWriter{repo: base}
	turn := &fakeTurnover{}
	chat := &fakeWarehouseNotifier{}
	common := &fakeCommonNotifier{}
	tasks := &fakeTaskOpener{}
	uc := NewUseCase(repo, turn, writer, common, tasks, chat, func() time.Time { return now })

	return &slotHarness{uc: uc, repo: repo, writer: writer, turn: turn, chat: chat, common: common, tasks: tasks, now: now, today: beginningOfDay(now)}
}

// key — ключ лота пары теста (нормализация срока, как в расчёте).
func (h *slotHarness) key(pid string, bestBefore time.Time) discounts.LotKey {
	return discounts.LotKey{ProductID: pid, BestBefore: beginningOfDay(bestBefore)}
}

// planItem — позиция плана дня для фейка подъёма: лот, скидка плана и причина.
// Контроля продаж нет — так выглядят сроковые позиции слота.
func (h *slotHarness) planItem(pid string, bestBefore time.Time, reason string) discounts.SlotItem {
	return discounts.SlotItem{LotKey: h.key(pid, bestBefore), Percent: slotMainPercent, Reason: reason}
}

// surplusPlanItem — позиция добора из избытка: с её контролем продаж пара
// считается проданной, если остаток к 16:00 опустился до initial−plan или ниже.
func (h *slotHarness) surplusPlanItem(pid string, bestBefore time.Time, percent int16, initial, plan int64) discounts.SlotItem {
	return discounts.SlotItem{
		LotKey:     h.key(pid, bestBefore),
		Percent:    percent,
		Reason:     discounts.ReasonSurplus,
		InitialQty: &initial,
		PlanQty:    &plan,
	}
}

// writesOf — правки единственного батча записи (пустой батч в шов не уходит).
func (h *slotHarness) writesOf(t *testing.T) []discounts.DiscountWrite {
	t.Helper()
	if len(h.writer.batches) != 1 {
		t.Fatalf("батчей правок %d, want 1: %+v", len(h.writer.batches), h.writer.batches)
	}

	return h.writer.batches[0]
}

// В слот плана дня идут только позиции с ПОВЫШЕНИЕМ: ручная скидка ≥ 20 % (её
// значение уже стоит на сайте — ТГ-колонку ей не пишем) и ступень по сроку
// СТРОГО выше применённой на сайте (ей пишем ТОЛЬКО ТГ-колонку: сайт получит
// скидку в 16:00, после того как её увидят подписчики). Позиции с уже стоящей
// скидкой (ступень не выше применённой) в слот не попадают.
func TestRunSlotPlanPublishesRaisesAndManual(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),             // D=8 → ступень 20 %
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26), manualInput(30)), // ручная 30 %
	)

	// Ёмкость 2: добор не работает (0,75 ёмкости = 1 позиция, слот уже полон).
	if err := h.uc.RunSlotPlan(context.Background(), h.now, 2); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 1 {
		t.Fatalf("правки: %+v, want одна (только ТГ-колонка ступени)", writes)
	}
	w := writes[0]
	if w.ProductID != "p1" || w.Telegram == nil || *w.Telegram != slotMainPercent {
		t.Errorf("правка %+v, want p1 telegram=%d", w, slotMainPercent)
	}
	if w.General != nil {
		t.Errorf("general правки %v, want nil (сайт план не трогает)", *w.General)
	}
	if w.Source != "" {
		t.Errorf("метка источника %q, want пусто (её ведёт автоматика)", w.Source)
	}

	if len(h.repo.items) != 1 || len(h.repo.items[0]) != 2 {
		t.Fatalf("позиции истории: %+v, want две (ступень и ручная)", h.repo.items)
	}
	percents := map[string]int16{}
	reasons := map[string]string{}
	for _, it := range h.repo.items[0] {
		percents[it.ProductID] = it.Percent
		reasons[it.ProductID] = it.Reason
	}
	if percents["p1"] != slotMainPercent || percents["p2"] != 30 {
		t.Errorf("скидки плана %v, want p1=%d p2=30", percents, slotMainPercent)
	}
	if reasons["p1"] != discounts.ReasonExpiry || reasons["p2"] != discounts.ReasonManual {
		t.Errorf("причины плана %v, want p1=expiry p2=manual", reasons)
	}

	if len(h.chat.texts) != 1 || !strings.Contains(h.chat.texts[0], "Колбаса") || !strings.Contains(h.chat.texts[0], "Сыр") {
		t.Errorf("сообщение в чат склада: %q, want список с обеими позициями", h.chat.texts)
	}
}

// План дня закрывает день: рассылка уходит в чат склада (канал warehouse),
// отмечается отправленной, и оба шага дня (план и пересчёт по сроку) закрыты —
// подъём 16:00 работает по отправленной рассылке, повторный сбор не нужен.
func TestRunSlotPlanMarksDayAndSendsDigest(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	if len(h.repo.digests) != 1 || h.repo.digests[0].ChatKind != discounts.ChatWarehouse {
		t.Fatalf("рассылки: %+v, want одна в %q", h.repo.digests, discounts.ChatWarehouse)
	}
	if h.repo.sent != 1 {
		t.Errorf("отметок отправки %d, want 1", h.repo.sent)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagPlan)] {
		t.Error("маркер дня плана не поставлен")
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagExpiry)] {
		t.Error("маркер дня пересчёта по сроку не поставлен: его ставит план дня")
	}
}

// Антидубль по ЛОТУ: позиция прошлой отправленной рассылки в новый план не
// попадает.
func TestRunSlotPlanSkipsPreviousDigestLots(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26)),
	)
	h.repo.lastPairs = map[discounts.LotKey]struct{}{h.key("p1", day(9)): {}}

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 1 {
		t.Fatalf("правки: %+v, want только p2", writes)
	}
	if got := writes[0].ProductID; got != "p2" {
		t.Errorf("позиция слота %q, want p2 (p1 был в прошлой рассылке)", got)
	}
}

// Позиция с уже стоящей скидкой (ступень НЕ выше применённой) в слот не идёт:
// подписчику она ничего не даёт. На её место попадает позиция с повышением.
func TestRunSlotPlanSkipsAlreadyAppliedDiscount(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26), plainInput(20)), // ступень 20 = сайт: не повышение
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26)),                      // ступень 20 > 0: в слот
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 1 || writes[0].ProductID != "p2" {
		t.Fatalf("правки: %+v, want только p2 (у p1 скидка уже на сайте)", writes)
	}
	if len(h.repo.items) != 1 || h.repo.items[0][0].ProductID != "p2" {
		t.Errorf("позиции истории: %+v, want только p2", h.repo.items)
	}
}

// Пара вне условий слота (успевает ли продаться, не заморожена ли ручным нулём)
// в план не идёт, даже если по ней есть ступень.
func TestRunSlotPlanSkipsIneligibleLots(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(2), 100, shelfLifeInput(26)),            // D=1: не успеет продаться
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26), manualInput(0)), // ручная 0: пара заморожена
		lotInput("p3", "Хлеб", day(9), 40, shelfLifeInput(26)),                // ступень 20 > 0: в слот
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 1 || writes[0].ProductID != "p3" {
		t.Fatalf("правки: %+v, want только p3", writes)
	}
}

// Добор недобранного слота: позиции со ступенью ровно 10 % (она уже стоит на
// сайте — сама по себе не повышение) получают план 20 %: добор поднимает их до
// скидки дня.
func TestRunSlotPlanDoborFillsByTenPercent(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),              // 20 % → слот
		lotInput("p2", "Хлеб", day(13), 40, shelfLifeInput(40), plainInput(10)), // 10 % на сайте → добор
		lotInput("p3", "Сыр", day(13), 40, shelfLifeInput(40), plainInput(10)),  // 10 % на сайте → добор
	)

	// Ёмкость 4 (0,75 = 3 позиции): в слоте одна, добираем две.
	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 3 {
		t.Fatalf("правки добора: %+v, want три позиции", writes)
	}
	for _, w := range writes {
		if w.Telegram == nil || *w.Telegram != slotMainPercent {
			t.Errorf("правка %+v, want telegram=%d у всех позиций слота", w, slotMainPercent)
		}
		// General в правке — текущее значение сайта: шов переписывает обе
		// колонки, поэтому значение обязано дойти как есть. У повышения по
		// сроку сайт ещё пуст (скидка поднимется в 16:00), у доборных пар на
		// сайте уже 10 % — их план не меняет.
		switch w.ProductID {
		case "p1":
			if w.General != nil {
				t.Errorf("повышение по сроку: general %v, want nil (сайт без скидки)", *w.General)
			}
		default:
			if w.General == nil || *w.General != slotBasePercent {
				t.Errorf("добор %s: general %v, want %d (сайт сохранён как есть)", w.ProductID, w.General, slotBasePercent)
			}
		}
	}
	// Скидка сайта у доборных пар не изменилась: план поднимает только ТГ-колонку.
	for _, in := range h.repo.inputs {
		if in.ProductID != "p2" && in.ProductID != "p3" {
			continue
		}
		if in.GeneralPlain == nil || *in.GeneralPlain != slotBasePercent {
			t.Errorf("скидка сайта %s = %v, want %d (план сайт не трогает)", in.ProductID, in.GeneralPlain, slotBasePercent)
		}
	}
}

// Добор из избытка: каждая пара раскладки объёма продаж — отдельная позиция
// слота с планом 20 %, причиной surplus и контролем продаж (initialQty/planQty).
// В тексте рассылки ей печатается количество.
func TestRunSlotPlanDoborFromSurplusSalesPlan(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)), // 20 % → слот
		lotInput("p2", "Сыр", day(11), 100),                        // избыток: продать 51 шт
	)
	// Оборот 150 шт/мес → 5 шт/день; D=10 → продастся 50, избыток 50 + 1 = 51 шт.
	h.turn.rates = map[string]float64{"p2": 150}

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 2 {
		t.Fatalf("правки: %+v, want две (ступень и добор избытка)", writes)
	}

	if len(h.repo.items) != 1 || len(h.repo.items[0]) != 2 {
		t.Fatalf("позиции истории: %+v, want две", h.repo.items)
	}
	var surplus *discounts.DigestItem
	for i := range h.repo.items[0] {
		if h.repo.items[0][i].ProductID == "p2" {
			surplus = &h.repo.items[0][i]
		}
	}
	if surplus == nil {
		t.Fatal("позиция добора из избытка (p2) в истории не найдена")
	}
	if surplus.Reason != discounts.ReasonSurplus || surplus.Percent != slotMainPercent {
		t.Errorf("позиция избытка %+v, want reason=surplus percent=%d", surplus, slotMainPercent)
	}
	if surplus.InitialQty == nil || *surplus.InitialQty != 100 || surplus.PlanQty == nil || *surplus.PlanQty != 51 {
		t.Errorf("контроль продаж %v/%v, want initial=100 plan=51", surplus.InitialQty, surplus.PlanQty)
	}

	if len(h.chat.texts) != 1 {
		t.Fatalf("сообщения в чат склада: %q, want одно", h.chat.texts)
	}
	text := h.chat.texts[0]
	if !strings.Contains(text, "1. (ТГ) Колбаса (100 шт до 23.09) — 20%") {
		t.Errorf("строка сроковой позиции плана: %q", text)
	}
	if !strings.Contains(text, "2. (ТГ) Сыр (51 шт до 25.09) — 20% (коэф 2,0)") {
		t.Errorf("строка добора из избытка с планом продаж: %q", text)
	}
}

// Лишние повышения (не влезли в ёмкость) получают скидку сайта СРАЗУ и
// уведомление «поднять скидку до X%» в общий канал: подписчики видят их позже,
// а сайт должен получить скидку сегодня. В рассылку они не идут.
func TestRunSlotPlanAppliesExtraImmediately(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(5), 100, shelfLifeInput(26), plainInput(10)), // D=4 → ступень 40
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26), plainInput(10)),      // D=8 → ступень 20
	)
	ctx := context.Background()

	// Наполняем реестр до плана: уведомление о правке лишней позиции считается
	// от того, что было на сайте (иначе первое наполнение съело бы его).
	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.tasks.texts, h.tasks.tries = nil, nil

	// Ёмкость 1: в слот идёт БЛИЖНИЙ срок, более далёкая ступень — в лишние.
	if err := h.uc.RunSlotPlan(ctx, h.now, 1); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 2 {
		t.Fatalf("правки: %+v, want две (ТГ-колонка слота и general лишней)", writes)
	}
	byPID := map[string]discounts.DiscountWrite{}
	for _, w := range writes {
		byPID[w.ProductID] = w
	}
	// Позиция слота: ТГ-колонка получает план, скидка сайта остаётся прежней
	// (General в правке — значение сайта, шов переписывает обе колонки).
	if got := byPID["p1"]; got.Telegram == nil || *got.Telegram != 40 || got.General == nil || *got.General != 10 {
		t.Errorf("позиция слота: %+v, want telegram=40 и general=10 (сайт сохранён как есть)", got)
	}
	extra := byPID["p2"]
	if extra.General == nil || *extra.General != 20 {
		t.Errorf("лишняя позиция: %+v, want general=20 (скидка сайта сразу)", extra)
	}
	if extra.Telegram != nil {
		t.Errorf("лишняя позиция telegram %v, want nil (в рассылку не идёт)", extra.Telegram)
	}
	if extra.Source != discounts.ReasonExpiry {
		t.Errorf("метка источника лишней %q, want %q", extra.Source, discounts.ReasonExpiry)
	}

	want := []string{"Поднять скидку до 20% на Сыр (до 23.09)"}
	if !reflect.DeepEqual(h.tasks.texts, want) {
		t.Errorf("уведомления %q, want %q", h.tasks.texts, want)
	}
	// Сообщение складу — всё окно скидок с метками канала: позиция плана уходит
	// в ТГ-колонку (метка (ТГ)), лишняя остаётся скидкой сайта (свой источник).
	// Склад по метке видит, что рассылать, а что просто стоит на сайте.
	if len(h.chat.texts) != 1 {
		t.Fatalf("сообщения в чат склада: %q, want одно", h.chat.texts)
	}
	text := h.chat.texts[0]
	if !strings.Contains(text, "1. (ТГ) Колбаса (100 шт до 19.09) — 40%") {
		t.Errorf("строка плана с меткой ТГ: %q", text)
	}
	if !strings.Contains(text, "2. (Срок) Сыр (50 шт до 23.09) — 20%") {
		t.Errorf("строка лишней позиции с меткой источника: %q", text)
	}
}

// Публиковать нечего: сообщения и записи истории нет, но маркеры дня (плана и
// пересчёта по сроку) стоят — повтор не нужен.
func TestRunSlotPlanWithoutCandidatesIsSilent(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100), // срока нет — ступени нет
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 10); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	if len(h.writer.batches) != 0 || len(h.chat.texts) != 0 || len(h.repo.digests) != 0 {
		t.Errorf("пустой слот дал записи: правки %v, сообщения %v, рассылки %v",
			h.writer.batches, h.chat.texts, h.repo.digests)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagPlan)] {
		t.Error("маркер дня плана не поставлен")
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagExpiry)] {
		t.Error("маркер дня пересчёта по сроку не поставлен")
	}
}

// Нулевая ёмкость слота: публиковать нечего, но повышения дня (они все —
// «лишние») применяются на сайте сразу, а маркеры дня ставятся.
func TestRunSlotPlanZeroCapacityAppliesExtras(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(5), 100, shelfLifeInput(26)), // D=4 → ступень 40
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 0); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 1 || writes[0].General == nil || *writes[0].General != 40 {
		t.Fatalf("правки: %+v, want одна general=40", writes)
	}
	if len(h.chat.texts) != 0 || len(h.repo.digests) != 0 || h.repo.sent != 0 {
		t.Errorf("пустой слот отправил рассылку: сообщения %v, рассылки %v, отметок %d",
			h.chat.texts, h.repo.digests, h.repo.sent)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagPlan)] || !h.repo.flags[fakeFlagKey(h.today, discounts.FlagExpiry)] {
		t.Error("маркеры дня плана и пересчёта по сроку не поставлены")
	}
}

// Повторный запуск в тот же день ничего не шлёт и не пишет: маркер дня.
func TestRunSlotPlanSecondRunSameDayIsNoop(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
	)

	for i := range 2 {
		if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
			t.Fatalf("план дня (%d): %v", i, err)
		}
	}

	if len(h.writer.batches) != 1 || len(h.chat.texts) != 1 {
		t.Errorf("повтор дал лишнее: правки %v, сообщения %v", h.writer.batches, h.chat.texts)
	}
}

// Подъём 16:00 меняет значение канала сайта, поэтому о нём надо сообщить
// человеку — тем же diff реестра, что и в расчётном тике. Проверяем и обратное:
// на следующем часу ложного «поднимите скидку» быть не должно (снапшот реестра
// должен нести УЖЕ поднятое значение).
func TestRunRaiseNotifiesGrowth(t *testing.T) {
	// Срока у лота нет: значение канала сайта даёт только избыток (у ступени
	// лестницы приоритет выше, и подъём проверял бы не то).
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, telegramInput(slotMainPercent)))
	ctx := context.Background()

	// Наполняем реестр: медленные продажи дали избыток — движок поставил 10 %.
	h.turn.rates = map[string]float64{"p1": 30}
	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.tasks.texts, h.tasks.tries = nil, nil

	// План дня: позиции назначена скидка дня 20 %.
	h.repo.plan = []discounts.SlotItem{h.planItem("p1", day(9), discounts.ReasonExpiry)}
	if err := h.uc.RunRaise(ctx, h.now); err != nil {
		t.Fatalf("RunRaise: %v", err)
	}

	want := []string{"Поднять скидку до 20% на Колбаса (до 23.09)"}
	if !reflect.DeepEqual(h.tasks.texts, want) {
		t.Errorf("уведомления %q, want %q", h.tasks.texts, want)
	}

	// Следующий час: расчёт видит уже поднятое значение и молчит.
	h.tasks.texts, h.tasks.tries = nil, nil
	delete(h.repo.flags, fakeFlagKey(h.today, discounts.FlagSurplus))
	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus (после подъёма): %v", err)
	}
	if len(h.tasks.texts) != 0 {
		t.Errorf("после подъёма пришло %q, want тишину", h.tasks.texts)
	}
}

// Порядок утренних шагов расписания: дайджест строится по реестру, а реестр
// наполняют пересчёты — значит отчёт обязан идти ПОСЛЕ них. Иначе на свежем
// старте (или после сна) в общий чат уходил бы пустой дайджест.
func TestRunStepsDigestAfterRecalc(t *testing.T) {
	// Понедельник 10:00: утро уже наступило, ТГ-день и КТ-день — не сегодня,
	// поэтому в проходе только наполнение реестра и дайджест.
	h := newSlotHarness(day(7).Add(10*time.Hour),
		lotInput("p1", "Колбаса", day(20), 100, manualInput(20)),
	)

	h.uc.runSteps(context.Background(), Schedule{
		Morning:     9 * time.Hour,
		Plan:        14 * time.Hour,
		Raise:       16 * time.Hour,
		TelegramCap: 10,
	})

	var digest string
	for _, text := range h.common.texts {
		if strings.HasPrefix(text, "Дайджест по скидкам") {
			digest = text
		}
	}
	if digest == "" {
		t.Fatalf("дайджест не отправлен: %q", h.common.texts)
	}
	if !strings.Contains(digest, "Колбаса") {
		t.Errorf("дайджест без позиции реестра: %q", digest)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagDigestSent)] {
		t.Error("маркер дня дайджеста не поставлен")
	}
}

// Подъём 16:00: по позициям ОТПРАВЛЕННОГО плана general поднимается до скидки
// плана; значения ТГ-колонки и метки источника уезжают как есть.
func TestRunRaiseUpgradesGeneralFromPlan(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26), telegramInput(slotMainPercent)),
	)
	h.repo.plan = []discounts.SlotItem{h.planItem("p1", day(9), discounts.ReasonExpiry)}

	if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
		t.Fatalf("подъём general: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 1 {
		t.Fatalf("правки подъёма: %+v", writes)
	}
	w := writes[0]
	if w.General == nil || *w.General != slotMainPercent {
		t.Errorf("general правки %v, want %d", w.General, slotMainPercent)
	}
	if w.Telegram == nil || *w.Telegram != slotMainPercent {
		t.Errorf("telegram правки %v, want %d (значение плана как есть)", w.Telegram, slotMainPercent)
	}
	if len(h.repo.raised) != 1 {
		t.Errorf("отметок подъёма %d, want 1", len(h.repo.raised))
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagRaise)] {
		t.Error("маркер дня подъёма не поставлен")
	}
}

// Цель подъёма — максимум из плана и ТЕКУЩЕЙ ТГ-колонки: если менеджер поднял
// ТГ-скидку руками после 14:00, сайт поднимаем до неё (решение владельца).
func TestRunRaiseUsesHigherTelegramValue(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, telegramInput(30)),
	)
	h.repo.plan = []discounts.SlotItem{h.planItem("p1", day(9), discounts.ReasonExpiry)}

	if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
		t.Fatalf("подъём general: %v", err)
	}

	writes := h.writesOf(t)
	if len(writes) != 1 {
		t.Fatalf("правки подъёма: %+v", writes)
	}
	if got := writes[0].General; got == nil || *got != 30 {
		t.Errorf("general правки %v, want 30 (текущая ТГ-колонка выше плана)", got)
	}
}

// Автоматика не понижает: скидка выше плана остаётся, ручная скидка пары не
// переписывается вовсе.
func TestRunRaiseDoesNotLowerOrTouchManual(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26), plainInput(30), telegramInput(slotMainPercent)),
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26), manualInput(30), telegramInput(slotMainPercent)),
	)
	h.repo.plan = []discounts.SlotItem{
		h.planItem("p1", day(9), discounts.ReasonExpiry),
		h.planItem("p2", day(9), discounts.ReasonManual),
	}

	if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
		t.Fatalf("подъём general: %v", err)
	}

	if len(h.writer.batches) != 0 {
		t.Errorf("правки %+v, want пусто: скидка 30 выше плана, ручная не переписывается", h.writer.batches)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagRaise)] {
		t.Error("маркер дня подъёма не поставлен")
	}
}

// Контроль продаж добора из избытка: пока остаток пары не опустился до
// initialQty−planQty, скидка сайта поднимается; достигнутый план — пропуск
// (продавать по скидке больше нечего).
func TestRunRaiseHonoursSurplusSaleControl(t *testing.T) {
	tests := []struct {
		title     string
		qty       int64
		wantWrite bool
	}{
		{"остаток выше контроля — поднимаем", 50, true},
		{"остаток ровно на плане продаж — пропуск", 49, false},
		{"остаток ниже плана — пропуск", 40, false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			// Остаток 100 → продать 51 шт, порог контроля — 49.
			h := newSlotHarness(recalcNow(1),
				lotInput("p1", "Сыр", day(11), tt.qty, telegramInput(slotMainPercent)),
			)
			h.repo.plan = []discounts.SlotItem{
				h.surplusPlanItem("p1", day(11), slotMainPercent, 100, 51),
			}

			if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
				t.Fatalf("подъём general: %v", err)
			}

			got := len(h.writer.batches) == 1
			if got != tt.wantWrite {
				t.Fatalf("правки %+v, want запись=%v", h.writer.batches, tt.wantWrite)
			}
			if tt.wantWrite {
				if w := h.writesOf(t)[0]; w.General == nil || *w.General != slotMainPercent {
					t.Errorf("general правки %v, want %d", w.General, slotMainPercent)
				}
			}
		})
	}
}

// Распроданная пара в снапшоте остатков не появляется: в цикле подъёма плана по
// ней нет — ошибки и правок тоже.
func TestRunRaiseSkipsAbsentLot(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
	)
	h.repo.plan = []discounts.SlotItem{h.planItem("p9", day(9), discounts.ReasonExpiry)}

	if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
		t.Fatalf("подъём general: %v", err)
	}

	if len(h.writer.batches) != 0 || len(h.repo.raised) != 0 {
		t.Errorf("правки %+v, want пусто: пары нет в снапшоте остатков", h.writer.batches)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagRaise)] {
		t.Error("маркер дня подъёма не поставлен")
	}
}

// Плана дня не было (рассылка не собрана): поднимать нечего, ошибки нет, маркер
// дня стоит (16:00 не повторяется).
func TestRunRaiseWithoutPlanIsQuiet(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
	)

	if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
		t.Fatalf("подъём general: %v", err)
	}

	if len(h.writer.batches) != 0 {
		t.Errorf("правки %+v, want пусто", h.writer.batches)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagRaise)] {
		t.Error("маркер дня подъёма не поставлен")
	}
}

// Повторный запуск подъёма в тот же день — no-op: маркер дня.
func TestRunRaiseSecondRunSameDayIsNoop(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, telegramInput(slotMainPercent)),
	)
	h.repo.plan = []discounts.SlotItem{h.planItem("p1", day(9), discounts.ReasonExpiry)}

	for i := range 2 {
		if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
			t.Fatalf("подъём general (%d): %v", i, err)
		}
	}

	if len(h.writer.batches) != 1 {
		t.Errorf("повтор подъёма дал лишнее: правки %+v", h.writer.batches)
	}
}

// Связка шагов дня: план 14:00 сохраняет контроль продаж добора из избытка, и
// 16:00 поднимает скидку сайта только если пара ещё не распродана.
func TestRunSlotPlanThenRaiseHonoursSaleControl(t *testing.T) {
	tests := []struct {
		title     string
		qty       int64
		wantWrite bool
	}{
		{"пара не продана — поднимаем", 100, true},
		{"план продаж выполнен — пропуск", 40, false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			h := newSlotHarness(recalcNow(1), lotInput("p2", "Сыр", day(11), 100))
			h.turn.rates = map[string]float64{"p2": 150} // продать 51 шт
			ctx := context.Background()

			if err := h.uc.RunSlotPlan(ctx, h.now, 4); err != nil {
				t.Fatalf("план дня: %v", err)
			}
			before := len(h.writer.batches)

			// Продажи: остаток пары к 16:00.
			h.repo.inputs[0].Qty = tt.qty

			if err := h.uc.RunRaise(ctx, h.now); err != nil {
				t.Fatalf("подъём general: %v", err)
			}

			got := len(h.writer.batches) > before
			if got != tt.wantWrite {
				t.Fatalf("правки после плана %+v, want запись=%v", h.writer.batches[before:], tt.wantWrite)
			}
		})
	}
}

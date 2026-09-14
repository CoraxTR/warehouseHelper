package usecase

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// ТГ-день: план слота 14:00 и подъём general 16:00 (slot.go). «БД» фейкового
// репозитория правит фейковый шов записи, поэтому проверяем и то, что ушло в
// сток, и то, что легло в историю рассылок.

// fakeSlotRepo — репозиторий ТГ-дня: к обвязке пересчёта (вход, маркеры дня)
// добавляется история рассылок и слот дня. Методы истории перекрывают заглушки
// fakeDiscountRepo (в пересчёте они не используются).
type fakeSlotRepo struct {
	*fakeDiscountRepo

	digests   []discounts.DigestRecord // сохранённые рассылки
	items     [][]discounts.DigestItem // позиции по рассылкам
	sent      int                      // отметок отправки
	lastPairs map[discounts.LotKey]struct{}
	slot      map[discounts.LotKey]int16
	raised    []discounts.LotKey
	saveErr   error
}

func (r *fakeSlotRepo) SaveDigest(_ context.Context, d discounts.DigestRecord, items []discounts.DigestItem) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.digests = append(r.digests, d)
	r.items = append(r.items, items)

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

func (r *fakeSlotRepo) TodaySlot(context.Context, time.Time) (map[discounts.LotKey]int16, error) {
	if r.slot == nil {
		return map[discounts.LotKey]int16{}, nil
	}

	return r.slot, nil
}

func (r *fakeSlotRepo) MarkGeneralRaised(_ context.Context, pairs []discounts.LotKey, _ time.Time) error {
	r.raised = append(r.raised, pairs...)

	return nil
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
	uc := NewUseCase(repo, turn, writer, common, chat, func() time.Time { return now })

	return &slotHarness{uc: uc, repo: repo, writer: writer, turn: turn, chat: chat, common: common, now: now, today: beginningOfDay(now)}
}

// key — ключ лота пары теста (нормализация срока, как в расчёте).
func (h *slotHarness) key(pid string, bestBefore time.Time) discounts.LotKey {
	return discounts.LotKey{ProductID: pid, BestBefore: beginningOfDay(bestBefore)}
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
	h.common.texts, h.common.tries = nil, nil

	// План слота: позиции назначена скидка дня 20 %.
	h.repo.slot = map[discounts.LotKey]int16{h.key("p1", day(9)): slotMainPercent}
	if err := h.uc.RunRaise(ctx, h.now); err != nil {
		t.Fatalf("RunRaise: %v", err)
	}

	want := []string{"Колбаса (до 23.09): Необходимо поднять скидку до 20%"}
	if !reflect.DeepEqual(h.common.texts, want) {
		t.Errorf("уведомления %q, want %q", h.common.texts, want)
	}

	// Следующий час: расчёт видит уже поднятое значение и молчит.
	h.common.texts, h.common.tries = nil, nil
	delete(h.repo.flags, fakeFlagKey(h.today, discounts.FlagSurplus))
	if err := h.uc.RecalcSurplus(ctx, h.now); err != nil {
		t.Fatalf("RecalcSurplus (после подъёма): %v", err)
	}
	if len(h.common.texts) != 0 {
		t.Errorf("после подъёма пришло %q, want тишину", h.common.texts)
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

// TestRunSlotPlanPublishesLadderAndManual — в слот попадают позиции с ручной
// скидкой и со ступенью ≥ 20 %, сроком не меньше двух дней и не из прошлой
// рассылки. ТГ-колонку движок ведёт только у ступени: под ручной стоит решение
// менеджера, план её лишь называет.
func TestRunSlotPlanPublishesLadderAndManual(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),             // D=8 → 20 %
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26), manualInput(30)), // ручная 30 %
		lotInput("p3", "Хлеб", day(12), 40, shelfLifeInput(40)),                // D=11 → 10 % (не в слоте)
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план слота: %v", err)
	}

	batches := h.writer.batches
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("батчи правок: %+v, want одна правка (только ступень)", batches)
	}
	w := batches[0][0]
	if w.ProductID != "p1" || w.Telegram == nil || *w.Telegram != slotMainPercent {
		t.Errorf("правка %+v, want p1 telegram=%d", w, slotMainPercent)
	}
	if w.General != nil {
		t.Errorf("general правки %v, want nil (план не трогает сайт)", *w.General)
	}
	if w.Source != "" {
		t.Errorf("метка источника %q, want пусто (её ведёт автоматика)", w.Source)
	}

	if len(h.repo.items) != 1 || len(h.repo.items[0]) != 2 {
		t.Fatalf("позиции истории: %+v, want две (ступень и ручная)", h.repo.items)
	}
	percents := map[string]int16{}
	for _, it := range h.repo.items[0] {
		percents[it.ProductID] = it.Percent
	}
	if percents["p1"] != slotMainPercent || percents["p2"] != 30 {
		t.Errorf("скидки плана %v, want p1=%d p2=30", percents, slotMainPercent)
	}

	if h.repo.digests[0].ChatKind != discounts.ChatWarehouse {
		t.Errorf("канал рассылки %q, want %q", h.repo.digests[0].ChatKind, discounts.ChatWarehouse)
	}
	if h.repo.sent != 1 {
		t.Errorf("отметок отправки %d, want 1", h.repo.sent)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagPlan)] {
		t.Error("маркер дня плана не поставлен")
	}
	if len(h.chat.texts) != 1 || !strings.Contains(h.chat.texts[0], "Колбаса") {
		t.Errorf("сообщение в чат склада: %q, want список с «Колбаса»", h.chat.texts)
	}
}

// TestRunSlotPlanSkipsPreviousDigestLots — антидубль по ЛОТУ: позиция прошлой
// отправленной рассылки в новый слот не попадает.
func TestRunSlotPlanSkipsPreviousDigestLots(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26)),
	)
	h.repo.lastPairs = map[discounts.LotKey]struct{}{{
		ProductID:  "p1",
		BestBefore: beginningOfDay(day(9)),
	}: {}}

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план слота: %v", err)
	}

	if len(h.writer.batches) != 1 || len(h.writer.batches[0]) != 1 {
		t.Fatalf("правки: %+v, want только p2", h.writer.batches)
	}
	if got := h.writer.batches[0][0].ProductID; got != "p2" {
		t.Errorf("позиция слота %q, want p2 (p1 был в прошлой рассылке)", got)
	}
}

// TestRunSlotPlanDoborFillsByTenPercent — наполнение меньше половины ёмкости
// добирается позициями со ступенью 10 %: им в ТГ-колонку пишется 20 % (скидка
// дня с добором).
func TestRunSlotPlanDoborFillsByTenPercent(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)), // 20 %
		lotInput("p2", "Хлеб", day(12), 40, shelfLifeInput(40)),    // 10 % → добор до 20 %
		lotInput("p3", "Сыр", day(12), 40, shelfLifeInput(40)),     // 10 % → добор
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план слота: %v", err)
	}

	planned := map[string]int16{}
	for _, w := range h.writer.batches[0] {
		planned[w.ProductID] = *w.Telegram
	}
	if len(planned) != 3 {
		t.Fatalf("правки добора: %v, want три позиции", planned)
	}
	if planned["p1"] != slotMainPercent || planned["p2"] != slotBasePercent*2 || planned["p3"] != slotBasePercent*2 {
		t.Errorf("план %v, want всем %d%%", planned, slotMainPercent)
	}
}

// TestRunSlotPlanWithoutCandidatesIsSilent — публиковать нечего: сообщения и
// записи истории нет, маркер дня стоит (повтор не нужен).
func TestRunSlotPlanWithoutCandidatesIsSilent(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100), // срока нет — ступени нет
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 10); err != nil {
		t.Fatalf("план слота: %v", err)
	}

	if len(h.writer.batches) != 0 || len(h.chat.texts) != 0 || len(h.repo.digests) != 0 {
		t.Errorf("пустой слот дал записи: правки %v, сообщения %v, рассылки %v",
			h.writer.batches, h.chat.texts, h.repo.digests)
	}
	if !h.repo.flags[fakeFlagKey(h.today, discounts.FlagPlan)] {
		t.Error("маркер дня плана не поставлен")
	}
}

// TestRunSlotPlanSecondRunSameDayIsNoop — повторный запуск в тот же день ничего
// не шлёт и не пишет: маркер дня.
func TestRunSlotPlanSecondRunSameDayIsNoop(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26)),
	)

	for i := range 2 {
		if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
			t.Fatalf("план слота (%d): %v", i, err)
		}
	}

	if len(h.writer.batches) != 1 || len(h.chat.texts) != 1 {
		t.Errorf("повтор дал лишнее: правки %v, сообщения %v", h.writer.batches, h.chat.texts)
	}
}

// TestRunRaiseUpgradesGeneralFromPlan — в 16:00 general поднимается до скидки
// плана; ТГ-колонка и метка источника передаются как есть.
func TestRunRaiseUpgradesGeneralFromPlan(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26), telegramInput(slotMainPercent)),
	)
	h.repo.slot = map[discounts.LotKey]int16{{
		ProductID:  "p1",
		BestBefore: beginningOfDay(day(9)),
	}: slotMainPercent}

	if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
		t.Fatalf("подъём general: %v", err)
	}

	if len(h.writer.batches) != 1 || len(h.writer.batches[0]) != 1 {
		t.Fatalf("правки подъёма: %+v", h.writer.batches)
	}
	w := h.writer.batches[0][0]
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

// TestRunRaiseUsesHigherTelegramValue — если менеджер поднял ТГ-скидку руками
// после 14:00, сайт поднимаем до неё, а не до плана рассылки (решение владельца).
func TestRunRaiseUsesHigherTelegramValue(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, telegramInput(30)),
	)
	h.repo.slot = map[discounts.LotKey]int16{h.key("p1", day(9)): slotMainPercent}

	if err := h.uc.RunRaise(context.Background(), h.now); err != nil {
		t.Fatalf("подъём general: %v", err)
	}

	if len(h.writer.batches) != 1 || len(h.writer.batches[0]) != 1 {
		t.Fatalf("правки подъёма: %+v", h.writer.batches)
	}
	if got := h.writer.batches[0][0].General; got == nil || *got != 30 {
		t.Errorf("general правки %v, want 30 (текущая ТГ-колонка выше плана)", got)
	}
}

// TestRunRaiseDoesNotLowerOrTouchManual — автоматика не понижает: скидка выше
// плана остаётся, ручная скидка не переписывается.
func TestRunRaiseDoesNotLowerOrTouchManual(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26), plainInput(30), telegramInput(slotMainPercent)),
		lotInput("p2", "Сыр", day(9), 50, shelfLifeInput(26), manualInput(30), telegramInput(slotMainPercent)),
	)
	key := func(pid string) discounts.LotKey {
		return discounts.LotKey{ProductID: pid, BestBefore: beginningOfDay(day(9))}
	}
	h.repo.slot = map[discounts.LotKey]int16{key("p1"): slotMainPercent, key("p2"): slotMainPercent}

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

// TestRunRaiseWithoutSlotIsQuiet — слота сегодня не было: поднимать нечего,
// ошибки нет, маркер дня стоит (16:00 не повторяется).
func TestRunRaiseWithoutSlotIsQuiet(t *testing.T) {
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

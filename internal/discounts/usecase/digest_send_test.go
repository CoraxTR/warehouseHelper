package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// digestHarness — обвязка отправки дайджеста: фейковый репозиторий (маркеры дня)
// и фейковый канал (общий + ответ в конкретный чат). Состояние реестра тест
// кладёт сам — расчёта здесь нет.
type digestHarness struct {
	uc     *UseCase
	repo   *fakeDiscountRepo
	common *fakeCommonNotifier
	now    time.Time
}

// newDigestHarness — обвязка на момент now: часы юзкейса отдают тот же момент,
// что тест передаёт в SendDigest (дата отчёта — из реестра, маркер — из аргумента).
func newDigestHarness(now time.Time) *digestHarness {
	repo := newFakeDiscountRepo()
	common := &fakeCommonNotifier{}
	uc := NewUseCase(repo, nil, nil, common, nil, func() time.Time { return now })

	return &digestHarness{uc: uc, repo: repo, common: common, now: now}
}

// digestNow — утро дня расчёта (время рассылки 09:00).
func digestNow() time.Time { return testDay.Add(9 * time.Hour) }

// fillRegistry — состояние реестра с обеими секциями отчёта: ручная скидка и
// избыток (порядок секций и строк задаёт доменный сборщик).
func (h *digestHarness) fillRegistry() {
	h.uc.reg.Replace([]PairState{
		regPair("p-manual", "Творог", day(8), manualOpt(40)),
		regPair("p-surplus", "Хлеб", day(12), surplusOpt(2.5)),
	})
}

// digestFlag — стоит ли маркер дня дайджеста.
func (h *digestHarness) digestFlag() bool {
	return h.repo.flags[fakeFlagKey(h.now, discounts.FlagDigestSent)]
}

// goldenDigest — текст отчёта по состоянию fillRegistry, собранный независимо
// доменным сборщиком (то, что должно уйти наружу) с датой отчёта now.
func goldenDigest(now time.Time) string {
	d := discounts.BuildDigest([]discounts.Row{
		{ProductID: "p-manual", Name: "Творог", BestBefore: day(8), Percent: 40, Source: discounts.SourceManual, DaysLeft: 8},
		{ProductID: "p-surplus", Name: "Хлеб", BestBefore: day(12), Percent: discounts.SurplusPercent(), Source: discounts.SourceSurplus, Coeff: 2.5, DaysLeft: 12},
	})
	d.Date = now

	return d.Text()
}

// Рассылка 09:00 уходит один раз за день: текст — отчёт реестра, после отправки
// стоит маркер дня, повторный вызов в тот же день молчит.
func TestSendDigestSendsRegistryTextOncePerDay(t *testing.T) {
	now := digestNow()
	h := newDigestHarness(now)
	h.fillRegistry()

	if err := h.uc.SendDigest(context.Background(), now); err != nil {
		t.Fatalf("SendDigest: %v", err)
	}

	want := goldenDigest(now)
	if len(h.common.tries) != 1 {
		t.Fatalf("попыток отправки: %d, want 1", len(h.common.tries))
	}
	if got := h.common.tries[0]; got != want {
		t.Fatalf("текст общего канала:\n%q\nwant:\n%q", got, want)
	}
	// тот же текст отдаёт реестр под мутексом юзкейса (Digest().Text())
	if got := h.uc.Digest().Text(); got != want {
		t.Errorf("текст реестра и доменного сборщика разошлись:\n%q\n%q", got, want)
	}
	if !h.digestFlag() {
		t.Error("маркер дня дайджеста не поставлен после отправки")
	}

	// повтор в тот же день: флаг дня — второй отправки нет
	if err := h.uc.SendDigest(context.Background(), now); err != nil {
		t.Fatalf("повторный SendDigest: %v", err)
	}
	if len(h.common.tries) != 1 {
		t.Errorf("попыток отправки после повтора: %d, want 1", len(h.common.tries))
	}
}

// Маркер дня уже стоит (рассылка была до рестарта): повтор не отправляется,
// ошибки нет.
func TestSendDigestSkipsWhenFlagSet(t *testing.T) {
	now := digestNow()
	h := newDigestHarness(now)
	h.fillRegistry()
	if err := h.repo.MarkDayFlag(context.Background(), now, discounts.FlagDigestSent); err != nil {
		t.Fatalf("MarkDayFlag: %v", err)
	}

	if err := h.uc.SendDigest(context.Background(), now); err != nil {
		t.Fatalf("SendDigest: %v", err)
	}
	if len(h.common.tries) != 0 {
		t.Errorf("отправки при стоящем маркере: %q, want пусто", h.common.tries)
	}
}

// Ошибка отправки возвращается наружу, маркер дня не ставится: следующий тик
// повторит рассылку.
func TestSendDigestSendErrorReturnsAndKeepsFlagUnset(t *testing.T) {
	now := digestNow()
	h := newDigestHarness(now)
	h.fillRegistry()
	sendErr := errors.New("телеграм недоступен")
	h.common.err = sendErr

	err := h.uc.SendDigest(context.Background(), now)
	if !errors.Is(err, sendErr) {
		t.Fatalf("SendDigest error = %v, want %v", err, sendErr)
	}
	if h.digestFlag() {
		t.Error("маркер дня поставлен при ошибке отправки")
	}
}

// Ошибка чтения маркера дня: отправки нет, ошибка наружу.
func TestSendDigestFlagCheckError(t *testing.T) {
	now := digestNow()
	h := newDigestHarness(now)
	h.fillRegistry()
	repoErr := errors.New("БД недоступна")
	h.repo.flagErr = repoErr

	err := h.uc.SendDigest(context.Background(), now)
	if !errors.Is(err, repoErr) {
		t.Fatalf("SendDigest error = %v, want %v", err, repoErr)
	}
	if len(h.common.tries) != 0 {
		t.Errorf("отправки при ошибке маркера: %q, want пусто", h.common.tries)
	}
}

// Команда /discounts: тот же отчёт в чат отправителя, сколько угодно раз, маркер
// дня не трогается (стоящий маркер команде не мешает), общий канал не задет.
func TestReplyDigestSendsToChatAndIgnoresDayFlag(t *testing.T) {
	now := digestNow()
	h := newDigestHarness(now)
	h.fillRegistry()
	if err := h.repo.MarkDayFlag(context.Background(), now, discounts.FlagDigestSent); err != nil {
		t.Fatalf("MarkDayFlag: %v", err)
	}

	const chatID int64 = 4242
	for i := 1; i <= 2; i++ {
		if err := h.uc.ReplyDigest(context.Background(), chatID); err != nil {
			t.Fatalf("ReplyDigest #%d: %v", i, err)
		}
	}

	want := goldenDigest(now)
	if len(h.common.chats) != 2 {
		t.Fatalf("ответов в чат: %d, want 2", len(h.common.chats))
	}
	for i, msg := range h.common.chats {
		if msg.chatID != chatID {
			t.Errorf("ответ #%d ушёл в чат %d, want %d", i+1, msg.chatID, chatID)
		}
		if msg.text != want {
			t.Errorf("ответ #%d, текст:\n%q\nwant:\n%q", i+1, msg.text, want)
		}
	}
	if len(h.common.tries) != 0 {
		t.Errorf("команда задела общий канал: %q", h.common.tries)
	}
	if !h.digestFlag() {
		t.Error("команда сняла маркер дня дайджеста")
	}
}

// Пустой реестр (расчёта ещё не было; пара без скидки — тоже пусто для отчёта):
// короткий ответ без паники.
func TestReplyDigestEmptyRegistryShortAnswer(t *testing.T) {
	h := newDigestHarness(digestNow())
	const chatID int64 = 777

	if err := h.uc.ReplyDigest(context.Background(), chatID); err != nil {
		t.Fatalf("ReplyDigest на пустом реестре: %v", err)
	}
	if len(h.common.chats) != 1 || h.common.chats[0].text != noDataText {
		t.Fatalf("ответы: %+v, want один текст %q", h.common.chats, noDataText)
	}

	// пара без скидки: Desired() = nil — в окно и отчёт она не попадает
	h.uc.reg.Replace([]PairState{regPair("p-idle", "Без скидки", day(20))})
	h.common.chats = nil

	if err := h.uc.ReplyDigest(context.Background(), chatID); err != nil {
		t.Fatalf("ReplyDigest на паре без скидки: %v", err)
	}
	if len(h.common.chats) != 1 || h.common.chats[0].text != noDataText {
		t.Fatalf("ответы: %+v, want короткий ответ %q", h.common.chats, noDataText)
	}
}

// Ошибка ответа в чат возвращается наружу.
func TestReplyDigestSendError(t *testing.T) {
	h := newDigestHarness(digestNow())
	h.fillRegistry()
	sendErr := errors.New("телеграм недоступен")
	h.common.errDetails = sendErr

	err := h.uc.ReplyDigest(context.Background(), 555)
	if !errors.Is(err, sendErr) {
		t.Fatalf("ReplyDigest error = %v, want %v", err, sendErr)
	}
}

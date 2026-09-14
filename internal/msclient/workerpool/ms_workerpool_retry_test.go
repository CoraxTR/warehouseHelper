package workerpool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testBackoff — пауза между повторами в тестах. Прод берёт 500мс, но тест не
// должен спать секундами: ряд пауз подменяется через поле pool.backoff.
const testBackoff = time.Millisecond

// limiterTokens — запас токенов тестового лимитера: тесту важно ЧИСЛО попыток,
// а не рейт-лимит, поэтому ждать тик лимитера нельзя.
const limiterTokens = 64

// fakeMSError повторяет семантику client.MSAPIError.Permanent(): постоянные —
// 4xx, кроме 408 (таймаут) и 429 (лимит запросов), они временные.
//
// Свой тип, а не client.MSAPIError, потому что client импортирует workerpool:
// импорт в тест пакета дал бы цикл. Совпадение контракта с настоящим
// MSAPIError прибито тестом TestMSAPIErrorPermanentContract
// (msapierror_contract_test.go).
type fakeMSError struct {
	code int
}

func (e *fakeMSError) Error() string { return fmt.Sprintf("API returned %d", e.code) }

func (e *fakeMSError) Permanent() bool {
	return e.code >= 400 && e.code < 500 && e.code != 408 && e.code != 429
}

// newRunningPool собирает пул с ЖИВЫМИ воркерами: задача выполняется в воркере
// (там же и ретрай), поэтому проверяется поведение пула, а не отдельной обёртки.
// Пауза повтора уменьшена до миллисекунды, Stop зовётся на выходе теста.
func newRunningPool(t *testing.T, warehouse, other int) *MSWorkerPool {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	p := &MSWorkerPool{
		warehouseTasks: make(chan task, 16),
		otherTasks:     make(chan task, 16),
		ctx:            ctx,
		cancel:         cancel,
		backoff:        testBackoff,
	}

	for range warehouse {
		w := &MSWarehouseWorker{Name: "тест-склад", APIKey: "test-key", rateLimiter: testLimiter()}
		p.WarehouseWorkers = append(p.WarehouseWorkers, w)
		p.wg.Add(1)

		go p.warehouseWorkerLoop(w)
	}

	for range other {
		w := &MSOtherWorker{Name: "тест-другой", APIKey: "test-key", rateLimiter: testLimiter()}
		p.OtherWorkers = append(p.OtherWorkers, w)
		p.wg.Add(1)

		go p.otherWorkerLoop(w)
	}

	t.Cleanup(p.Stop)

	return p
}

// testLimiter — лимитер с заранее разложенными токенами (run не запускается).
func testLimiter() *MSOutRateLimiter {
	ch := make(chan struct{}, limiterTokens)
	for range limiterTokens {
		ch <- struct{}{}
	}

	return &MSOutRateLimiter{ch: ch}
}

// retrySubmitters — оба пути постановки задачи с повторами.
func retrySubmitters() map[string]func(*MSWorkerPool, JobFunc) <-chan result {
	return map[string]func(*MSWorkerPool, JobFunc) <-chan result{
		"SubmitWarehouse": (*MSWorkerPool).SubmitWarehouse,
		"SubmitOther":     (*MSWorkerPool).SubmitOther,
	}
}

// noRetrySubmitters — оба пути постановки задачи без повторов.
func noRetrySubmitters() map[string]func(*MSWorkerPool, JobFunc) <-chan result {
	return map[string]func(*MSWorkerPool, JobFunc) <-chan result{
		"SubmitWarehouseNoRetry": (*MSWorkerPool).SubmitWarehouseNoRetry,
		"SubmitOtherNoRetry":     (*MSWorkerPool).SubmitOtherNoRetry,
	}
}

// TestRetryOnTransient5xx — главный сценарий: МС отдал 5xx на первой попытке,
// задача повторена и выполнена со второй. Проверяется число ВЫЗОВОВ job, а не
// только итог: без ретрая тест дал бы ошибку, с ретраем — успех и 2 вызова.
func TestRetryOnTransient5xx(t *testing.T) {
	for name, submit := range retrySubmitters() {
		t.Run(name, func(t *testing.T) {
			p := newRunningPool(t, 1, 1)

			var calls atomic.Int64

			job := func(string) (any, error) {
				if calls.Add(1) == 1 {
					return nil, &fakeMSError{code: 500}
				}

				return "готово", nil
			}

			v := recvOne(t, submit(p, job))

			if v.Err != nil {
				t.Fatalf("Err = %v, ожидали успех со второй попытки", v.Err)
			}

			if got := calls.Load(); got != 2 {
				t.Errorf("job вызван %d раз, ожидали 2 (5xx → повтор → успех)", got)
			}

			if v.Value != "готово" {
				t.Errorf("Value = %v, ожидали непустой результат успешной попытки", v.Value)
			}
		})
	}
}

// TestNoRetryOnPermanent4xx — постоянный отказ МС (400): ответ не изменится,
// задача выполняется ровно один раз, ошибка уходит наверх как есть.
func TestNoRetryOnPermanent4xx(t *testing.T) {
	for name, submit := range retrySubmitters() {
		t.Run(name, func(t *testing.T) {
			p := newRunningPool(t, 1, 1)

			var calls atomic.Int64

			job := func(string) (any, error) {
				calls.Add(1)

				return nil, &fakeMSError{code: 400}
			}

			v := recvOne(t, submit(p, job))

			var apiErr *fakeMSError
			if !errors.As(v.Err, &apiErr) || apiErr.code != 400 {
				t.Errorf("Err = %v, ожидали ошибку с кодом 400 наверх", v.Err)
			}

			if got := calls.Load(); got != 1 {
				t.Errorf("job вызван %d раз, ожидали 1 (постоянная ошибка не повторяется)", got)
			}
		})
	}
}

// TestNoRetryOnCanceledContext — родительский контекст отменён (вызывающий ушёл):
// повторять нечего, ошибка сразу наверх и ровно одна попытка.
func TestNoRetryOnCanceledContext(t *testing.T) {
	for name, submit := range retrySubmitters() {
		t.Run(name, func(t *testing.T) {
			p := newRunningPool(t, 1, 1)

			parent, cancel := context.WithCancel(context.Background())
			cancel() // отменён ДО постановки задачи

			var calls atomic.Int64

			job := func(string) (any, error) {
				calls.Add(1)

				return nil, parent.Err() // context.Canceled, как вернул бы реальный job
			}

			v := recvOne(t, submit(p, job))

			if !errors.Is(v.Err, context.Canceled) {
				t.Errorf("Err = %v, ожидали context.Canceled", v.Err)
			}

			if got := calls.Load(); got != 1 {
				t.Errorf("job вызван %d раз, ожидали 1 (отмена не повторяется)", got)
			}
		})
	}
}

// TestNoRetrySubmitOnTransient5xx — задача «без повтора» (неидемпотентный POST
// создания отгрузки): даже временный 5xx не повторяется, иначе в учёте МС
// появится вторая отгрузка.
func TestNoRetrySubmitOnTransient5xx(t *testing.T) {
	for name, submit := range noRetrySubmitters() {
		t.Run(name, func(t *testing.T) {
			p := newRunningPool(t, 1, 1)

			var calls atomic.Int64

			job := func(string) (any, error) {
				calls.Add(1)

				return nil, &fakeMSError{code: 500}
			}

			v := recvOne(t, submit(p, job))

			var apiErr *fakeMSError
			if !errors.As(v.Err, &apiErr) || apiErr.code != 500 {
				t.Errorf("Err = %v, ожидали ошибку 500 наверх", v.Err)
			}

			if got := calls.Load(); got != 1 {
				t.Errorf("job вызван %d раз, ожидали 1 (задача помечена как без повтора)", got)
			}
		})
	}
}

// TestRetryExhaustedReturnsLastError — все попытки исчерпаны: наверх уходит
// ПОСЛЕДНЯЯ ошибка (а не первая и не отмена), попыток ровно столько, сколько
// задаёт политика.
func TestRetryExhaustedReturnsLastError(t *testing.T) {
	for name, submit := range retrySubmitters() {
		t.Run(name, func(t *testing.T) {
			p := newRunningPool(t, 1, 1)

			var calls atomic.Int64

			job := func(string) (any, error) {
				n := calls.Add(1)

				return nil, fmt.Errorf("попытка %d: %w", n, &fakeMSError{code: 503})
			}

			v := recvOne(t, submit(p, job))

			if got := calls.Load(); got != defaultAttempts {
				t.Errorf("job вызван %d раз, ожидали %d (число попыток политики)", got, defaultAttempts)
			}

			if v.Err == nil {
				t.Fatal("Err = nil, ожидали последнюю ошибку наверх")
			}

			want := fmt.Sprintf("попытка %d", defaultAttempts)
			if !strings.Contains(v.Err.Error(), want) {
				t.Errorf("Err = %q, ожидали последнюю ошибку (содержит %q)", v.Err, want)
			}
		})
	}
}

// TestRetryAttemptsOverride — число попыток берётся из поля пула (0 = политика
// по умолчанию): тесты не зависят от прод-значения, а прод остаётся с 3.
func TestRetryAttemptsOverride(t *testing.T) {
	p := newRunningPool(t, 1, 1)
	p.attempts = 5

	var calls atomic.Int64

	job := func(string) (any, error) {
		calls.Add(1)

		return nil, &fakeMSError{code: 503}
	}

	_ = recvOne(t, p.SubmitOther(job))

	if got := calls.Load(); got != 5 {
		t.Errorf("job вызван %d раз, ожидали 5 (поле pool.attempts)", got)
	}
}

// TestAttemptLimitZeroMeansDefault — пул, собранный литералом (как в тестах и
// в newTestPool), без полей attempts/backoff работает по прод-политике.
func TestAttemptLimitZeroMeansDefault(t *testing.T) {
	p := &MSWorkerPool{}

	if got := p.attemptLimit(); got != defaultAttempts {
		t.Errorf("attemptLimit() = %d, ожидали %d", got, defaultAttempts)
	}

	if got := p.backoffFor(1); got != defaultBackoff {
		t.Errorf("backoffFor(1) = %v, ожидали %v", got, defaultBackoff)
	}

	if got := p.backoffFor(2); got != 2*defaultBackoff {
		t.Errorf("backoffFor(2) = %v, ожидали %v (удвоение)", got, 2*defaultBackoff)
	}
}

// TestBackoffForDoubles — ряд пауз тот же, что в pdfexport: 500мс → 1с → 2с.
func TestBackoffForDoubles(t *testing.T) {
	p := &MSWorkerPool{backoff: defaultBackoff}

	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}

	for i, w := range want {
		if got := p.backoffFor(i + 1); got != w {
			t.Errorf("backoffFor(%d) = %v, ожидали %v", i+1, got, w)
		}
	}
}

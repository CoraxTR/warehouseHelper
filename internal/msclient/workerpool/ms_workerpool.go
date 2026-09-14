package workerpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"warehouseHelper/internal/config"
)

const (
	// defaultAttempts — сколько всего попыток выполнить задачу (1 + повторы).
	// Ряд повторяется один в один из internal/pdfexport (там ретрай скачивания
	// бланка): политика у МойСклад одна на все запросы, второй ряд пауз
	// заводить нельзя. Общий хелпер — кандидат на вынос в отдельный пакет.
	defaultAttempts = 3
	// defaultBackoff — пауза перед первой повторной попыткой (далее удваивается):
	// 500мс → 1с → 2с.
	defaultBackoff = 500 * time.Millisecond
)

// ErrPoolStopped — пул остановлен и задачу не принял.
//
// Ошибка, а НЕ пустой результат: закрытый канал без значения вызывающий читает
// как zero-value result{} с Err == nil, то есть считает задачу выполненной.
// Для вызовов вроде SetOrderAsShippedToRefGo это значит «заказ помечен в МС»,
// хотя в МС не ушло ничего — и в логе чисто. Нет задачи — должна быть ошибка.
var ErrPoolStopped = errors.New("воркерпул МС остановлен")

type JobFunc func(apikey string) (any, error)

type result struct {
	Value any
	Err   error
}

type task struct {
	job   JobFunc
	resCh chan<- result
	// noRetry — задачу НЕ повторять даже при временном сбое МС. Нужно для
	// неидемпотентных POST: повтор после ответа сервера (или после таймаута,
	// когда ответ мог прийти и потеряться) создаёт второй объект в учёте МС.
	noRetry bool
}

type MSWorkerPool struct {
	WarehouseWorkers []*MSWarehouseWorker
	OtherWorkers     []*MSOtherWorker
	warehouseTasks   chan task
	otherTasks       chan task
	wg               sync.WaitGroup
	ctx              context.Context //nolint:containedctx //we need to cancel all workers when stopping the pool
	cancel           context.CancelFunc
	once             sync.Once

	// attempts — сколько попыток делать (0 = defaultAttempts), backoff — база
	// пауз (0 = defaultBackoff). Поля, а не константы: тесты подменяют backoff
	// миллисекундой, чтобы не спать секундами. Ноль = прод-политика, поэтому
	// пул, собранный в тестах литералом (newTestPool), тоже работает по ней.
	attempts int
	backoff  time.Duration

	// mu защищает stopped и сериализует отправку задачи с закрытием каналов в
	// Stop. Без него горутина проходит проверку остановки и доезжает до
	// отправки уже после close: panic "send on closed channel" в незатреканной
	// горутине роняет процесс при ШТАТНОЙ остановке.
	mu      sync.RWMutex
	stopped bool
}

type MSWarehouseWorker struct {
	APIKey      string
	Name        string
	rateLimiter *MSOutRateLimiter
}

type MSOtherWorker struct {
	APIKey      string
	Name        string
	rateLimiter *MSOutRateLimiter
}

func validateKey(ctx context.Context, config *config.MSConfig, apikey string) bool {
	// Проверочный GET на организацию: href собирается из base URL + id,
	// чтобы валидация не зависела от хранимых href'ов.
	orgURL := config.URLstart + "organization/" + config.Refs.OrgID

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orgURL, http.NoBody)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to create request: %s", err))

		return false
	}

	req.Header.Set("Authorization", config.AuthHeader+" "+apikey)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to make request: %s", err))

		return false
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			slog.Error(fmt.Sprintf("failed to close response body: %s", err))
		}
	}()

	return resp.StatusCode == http.StatusOK
}

func NewMSWorkerPool(config *config.MSConfig) *MSWorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	pool := &MSWorkerPool{
		WarehouseWorkers: make([]*MSWarehouseWorker, 0, len(config.WarehouseAPIKEYS)),
		OtherWorkers:     make([]*MSOtherWorker, 0, len(config.OthersAPIKEYS)),
		warehouseTasks:   make(chan task, len(config.WarehouseAPIKEYS)*2),
		otherTasks:       make(chan task, len(config.OthersAPIKEYS)*2),
		ctx:              ctx,
		cancel:           cancel,
		once:             sync.Once{},
		attempts:         defaultAttempts,
		backoff:          defaultBackoff,
	}

	for _, v := range config.WarehouseAPIKEYS {
		w := &MSWarehouseWorker{APIKey: v.APIKey, Name: v.Name, rateLimiter: NewMSOutRateLimiter(config)}

		ok := validateKey(ctx, config, v.APIKey)
		if !ok {
			slog.Info(fmt.Sprintf("%s: Мне дали неправильный API-ключ!", v.Name))

			continue
		}

		slog.Info(fmt.Sprintf("%s: API-ключ прошёл проверку, мне можно давать задачи", v.Name))

		pool.WarehouseWorkers = append(pool.WarehouseWorkers, w)
		pool.wg.Add(1)

		go pool.warehouseWorkerLoop(w)
	}

	for _, v := range config.OthersAPIKEYS {
		w := &MSOtherWorker{APIKey: v.APIKey, Name: v.Name, rateLimiter: NewMSOutRateLimiter(config)}

		ok := validateKey(ctx, config, v.APIKey)
		if !ok {
			slog.Info(fmt.Sprintf("%s: Мне дали неправильный API-ключ!", v.Name))

			continue
		}

		slog.Info(fmt.Sprintf("%s: API-ключ прошёл проверку, мне можно давать задачи", v.Name))

		pool.OtherWorkers = append(pool.OtherWorkers, w)
		pool.wg.Add(1)

		go pool.otherWorkerLoop(w)
	}

	return pool
}

// SubmitWarehouse ставит задачу в очередь складских воркеров.
//
// RLock держится до конца отправки: Stop берёт Lock и потому не может закрыть
// канал под работающей отправкой — именно эта гонка давала panic при остановке.
// Дедлока нет: на момент отправки воркеры ещё живы (cancel в Stop идёт позже),
// канал разгружается.
//
// Проверки ctx.Done больше нет: ctx отменяет только Stop, а он выставляет
// stopped под тем же мутексом — состояние остановки читается через stopped.
func (p *MSWorkerPool) SubmitWarehouse(job JobFunc) <-chan result {
	return p.submit(p.warehouseTasks, len(p.WarehouseWorkers), job, false)
}

// SubmitOther — то же для прочих воркеров (см. SubmitWarehouse).
func (p *MSWorkerPool) SubmitOther(job JobFunc) <-chan result {
	return p.submit(p.otherTasks, len(p.OtherWorkers), job, false)
}

// SubmitWarehouseNoRetry — складская задача БЕЗ повторов при временных сбоях.
//
// Для неидемпотентных запросов: если МС ответил 5xx или не ответил вовсе
// (таймаут), запрос мог выполниться на стороне сервера — повтор создаст
// дубль в учёте, а разбирать его оператору вручную.
func (p *MSWorkerPool) SubmitWarehouseNoRetry(job JobFunc) <-chan result {
	return p.submit(p.warehouseTasks, len(p.WarehouseWorkers), job, true)
}

// SubmitOtherNoRetry — то же для прочих воркеров (см. SubmitWarehouseNoRetry).
func (p *MSWorkerPool) SubmitOtherNoRetry(job JobFunc) <-chan result {
	return p.submit(p.otherTasks, len(p.OtherWorkers), job, true)
}

// submit — общий путь постановки задачи: проверка остановки, проверка наличия
// воркеров и отправка в очередь нужного пула.
func (p *MSWorkerPool) submit(tasks chan task, workers int, job JobFunc, noRetry bool) <-chan result {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.stopped {
		return errResult(ErrPoolStopped)
	}

	// Воркеров нет (все ключи не прошли проверку в NewMSWorkerPool) — очередь
	// никто не разберёт: см. ErrNoWorkers.
	if workers == 0 {
		return errResult(ErrNoWorkers)
	}

	resCh := make(chan result, 1)
	tasks <- task{job: job, resCh: resCh, noRetry: noRetry}

	return resCh
}

// ErrNoWorkers — задачи ставить некуда: ни один API-ключ МС не прошёл
// проверку, живых воркеров нет. Именно ошибка, а не ожидание: очередь без
// потребителя держит отправителя в блокирующей отправке под RLock, из-за чего
// Stop не может взять Lock и остановка приложения виснет — а «Ctrl+C всегда
// выходит» и есть смысл graceful shutdown.
var ErrNoWorkers = errors.New("нет воркеров МС: ни один ключ не прошёл проверку")

// errResult — ответ на задачу, которую пул не принял: одно значение и
// закрытие, как у любой выполненной задачи. Иначе вызывающий либо прочитал бы
// остановку как успех, либо ждал бы resCh вечно.
func errResult(err error) <-chan result {
	ch := make(chan result, 1)
	ch <- result{Err: err}
	close(ch)

	return ch
}

// Stop останавливает пул: новые задачи не принимаются, очередь разбирается,
// воркеры дожидаются. Идемпотентен (once) — двойной Shutdown не паникует.
func (p *MSWorkerPool) Stop() {
	p.once.Do(func() {
		// Закрытие каналов и stopped=true — одной критической секцией под Lock:
		// Lock дождётся отправок, уже держащих RLock, поэтому close не может
		// пересечься с «отправкой после проверки stopped» (та самая гонка).
		// Каналы закрываем, а не только отменяем ctx: закрытие выводит воркеров
		// из select/range даже если задача больше не придёт.
		p.mu.Lock()
		p.stopped = true
		close(p.warehouseTasks)
		close(p.otherTasks)
		p.mu.Unlock()

		// Отмена после закрытия: воркеры, сидящие в уже взятой задаче, выходят
		// по ctx, а wg.Wait не даёт процессу уйти посреди запроса к МС.
		p.cancel()
		p.wg.Wait()

		// Drain: задача, проскочившая до закрытия, но не взятая воркером
		// (воркеры вышли по ctx.Done), иначе оставила бы вызывающего ждать resCh
		// вечно. Каналы закрыты выше — range завершится сам, как только очередь
		// опустеет. Отвечаем ошибкой, а не пустым результатом: в МС не ушло
		// ничего.
		for t := range p.warehouseTasks {
			t.resCh <- result{Err: ErrPoolStopped}
			close(t.resCh)
		}

		for t := range p.otherTasks {
			t.resCh <- result{Err: ErrPoolStopped}
			close(t.resCh)
		}
	})
}

func (p *MSWorkerPool) warehouseWorkerLoop(worker *MSWarehouseWorker) {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		case task, ok := <-p.warehouseTasks:
			if !ok {
				return
			}

			res, err := p.runJob(worker.rateLimiter, worker.APIKey, task)
			task.resCh <- result{Value: res, Err: err}

			close(task.resCh)
		case task, ok := <-p.otherTasks:
			if !ok {
				return
			}

			res, err := p.runJob(worker.rateLimiter, worker.APIKey, task)
			task.resCh <- result{Value: res, Err: err}

			close(task.resCh)
		}
	}
}

func (p *MSWorkerPool) otherWorkerLoop(worker *MSOtherWorker) {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		case task, ok := <-p.otherTasks:
			if !ok {
				return
			}

			res, err := p.runJob(worker.rateLimiter, worker.APIKey, task)
			task.resCh <- result{Value: res, Err: err}

			close(task.resCh)
		}
	}
}

// runJob выполняет задачу с повторами временных сбоев МойСклад.
//
// Политика — как в internal/pdfexport (ретрай скачивания бланка): не более
// attemptLimit() попыток с паузами 500мс → 1с → 2с. Пытаемся только то, что
// имеет смысл: 5xx, сетевые сбои, таймауты (в т.ч. context.DeadlineExceeded
// внутреннего таймаута задачи — его job'ы ставят сами).
//
// НЕ повторяем:
//   - постоянные отказы МС (Permanent(): 4xx, кроме 408 и 429) — ответ не
//     изменится;
//   - отменённый родительский контекст (context.Canceled) — вызывающий ушёл,
//     повторять за него нечего;
//   - остановку пула (p.ctx) — приложение выключается, ждать паузы нельзя.
//
// Рейт-лимит соблюдается на каждой попытке: повтор — тоже запрос к МС, и
// он проходит через тот же воркер с его лимитером (5 req/s — не обойти).
func (p *MSWorkerPool) runJob(limiter *MSOutRateLimiter, apiKey string, task task) (any, error) {
	attempts := p.attemptLimit()
	if task.noRetry {
		attempts = 1
	}

	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		limiter.Wait()

		res, err := task.job(apiKey)
		if err == nil {
			return res, nil
		}

		lastErr = err

		if p.ctx.Err() != nil || errors.Is(err, context.Canceled) || isPermanent(err) || attempt == attempts {
			break
		}

		// Токены и тела запросов в лог не попадают: только номер попытки и
		// текст ошибки (правило проекта — секреты не логируем).
		slog.Warn("запрос к МС не удался, повторяю", "попытка", attempt, "следующая", attempt+1, "err", err)

		select {
		case <-time.After(p.backoffFor(attempt)):
		case <-p.ctx.Done():
			// Пул остановлен: паузу не дожидаемся, наверх отдаём последнюю
			// ошибку МС — она информативнее отмены контекста.
			return nil, lastErr
		}
	}

	return nil, lastErr
}

// attemptLimit — сколько всего попыток делать (0 в поле = defaultAttempts).
func (p *MSWorkerPool) attemptLimit() int {
	if p.attempts <= 0 {
		return defaultAttempts
	}

	return p.attempts
}

// backoffFor — пауза перед повтором: backoff, затем удвоение (500мс, 1с, ...).
// Ряд один в один с pdfexport.Service.backoffFor.
func (p *MSWorkerPool) backoffFor(attempt int) time.Duration {
	base := p.backoff
	if base <= 0 {
		base = defaultBackoff
	}

	return base * time.Duration(1<<(attempt-1))
}

// permanentError — ошибка МС, повтор которой бессмысленен (4xx, кроме 408/429).
// Реализуется *client.MSAPIError.
type permanentError interface {
	Permanent() bool
}

// isPermanent сообщает, помечена ли ошибка как постоянная. Аналог
// pdfexport.isPermanent: тот же контракт, проверяемый на client.MSAPIError.
func isPermanent(err error) bool {
	var perm permanentError

	return errors.As(err, &perm) && perm.Permanent()
}

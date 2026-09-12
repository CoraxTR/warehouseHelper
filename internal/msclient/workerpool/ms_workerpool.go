package workerpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"warehouseHelper/internal/config"
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
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.stopped {
		return stoppedResult()
	}

	resCh := make(chan result, 1)
	p.warehouseTasks <- task{job: job, resCh: resCh}

	return resCh
}

// SubmitOther — то же для прочих воркеров (см. SubmitWarehouse).
func (p *MSWorkerPool) SubmitOther(job JobFunc) <-chan result {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.stopped {
		return stoppedResult()
	}

	resCh := make(chan result, 1)
	p.otherTasks <- task{job: job, resCh: resCh}

	return resCh
}

// stoppedResult — ответ на задачу, которую пул не принял: одно значение и
// закрытие, как у любой выполненной задачи. Иначе вызывающий либо прочитал бы
// остановку как успех, либо ждал бы resCh вечно.
func stoppedResult() <-chan result {
	ch := make(chan result, 1)
	ch <- result{Err: ErrPoolStopped}
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

			worker.rateLimiter.Wait()

			res, err := task.job(worker.APIKey)
			task.resCh <- result{Value: res, Err: err}

			close(task.resCh)
		case task, ok := <-p.otherTasks:
			if !ok {
				return
			}

			worker.rateLimiter.Wait()

			res, err := task.job(worker.APIKey)
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

			worker.rateLimiter.Wait()

			res, err := task.job(worker.APIKey)
			task.resCh <- result{Value: res, Err: err}

			close(task.resCh)
		}
	}
}

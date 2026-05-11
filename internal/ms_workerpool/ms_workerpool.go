package ms_workerpool

import (
	"context"
	"sync"
	"warehouseHelper/internal/config"
)

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
	ctx              context.Context
	cancel           context.CancelFunc
	once             sync.Once
}

type MSWarehouseWorker struct {
	APIKey      string
	rateLimiter *MSOutRateLimiter
}

type MSOtherWorker struct {
	APIKey      string
	rateLimiter *MSOutRateLimiter
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
		w := &MSWarehouseWorker{APIKey: v, rateLimiter: NewMSOutRateLimiter(config)}
		pool.WarehouseWorkers = append(pool.WarehouseWorkers, w)
		pool.wg.Add(1)

		go pool.warehouseWorkerLoop(w)
	}

	for _, v := range config.OthersAPIKEYS {
		w := &MSOtherWorker{APIKey: v, rateLimiter: NewMSOutRateLimiter(config)}
		pool.OtherWorkers = append(pool.OtherWorkers, w)
		pool.wg.Add(1)

		go pool.otherWorkerLoop(w)
	}

	return pool
}

func (p *MSWorkerPool) SubmitWarehouse(job JobFunc) <-chan result {
	select {
	case <-p.ctx.Done():
		ch := make(chan result)
		close(ch)

		return ch
	default:
		resCh := make(chan result, 1)
		p.warehouseTasks <- task{job: job, resCh: resCh}

		return resCh
	}
}

func (p *MSWorkerPool) SubmitOther(job JobFunc) <-chan result {
	select {
	case <-p.ctx.Done():
		ch := make(chan result)
		close(ch)

		return ch
	default:
		resCh := make(chan result, 1)
		p.otherTasks <- task{job: job, resCh: resCh}

		return resCh
	}
}

func (p *MSWorkerPool) Stop() {
	p.once.Do(func() {
		p.cancel()
		close(p.warehouseTasks)
		close(p.otherTasks)
		p.wg.Wait()
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

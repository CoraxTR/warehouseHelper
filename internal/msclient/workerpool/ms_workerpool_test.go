package workerpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// newTestPool собирает пул напрямую, минуя NewMSWorkerPool: прод-конструктор
// проверяет каждый API-ключ проверочным GET'ом в МС, а тесту нужен только
// протокол Submit/Stop. Воркеры намеренно НЕ запускаются — так воспроизводится
// состояние «воркер вышел по ctx.Done, а задача осталась в буфере», которое и
// лечит drain в Stop. Буферы большие: задачи из очереди никто не разбирает, и
// отправка не должна блокироваться (иначе тест проверял бы не то).
func newTestPool(warehouseCap, otherCap int) *MSWorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	return &MSWorkerPool{
		// Заглушки воркеров: в живом пуле воркер есть ровно тогда, когда его
		// ключ прошёл проверку, а без хотя бы одного воркера Submit отвечает
		// ErrNoWorkers (см. TestSubmitWithoutWorkers) — здесь проверяется
		// протокол Submit/Stop, а не отсутствие ключей.
		WarehouseWorkers: []*MSWarehouseWorker{{}},
		OtherWorkers:     []*MSOtherWorker{{}},
		warehouseTasks:   make(chan task, warehouseCap),
		otherTasks:       make(chan task, otherCap),
		ctx:              ctx,
		cancel:           cancel,
	}
}

// TestSubmitWithoutWorkers — если ни один ключ МС не прошёл проверку, воркеров
// нет и очередь разбирать некому. Submit обязан ответить ошибкой немедленно:
// блокирующая отправка в очередь без потребителя держит RLock, Stop не может
// взять Lock — остановка приложения виснет навсегда, вместо того чтобы выйти.
func TestSubmitWithoutWorkers(t *testing.T) {
	p := newTestPool(0, 0) // ёмкость 0: без guard'а отправка заблокировалась бы
	p.WarehouseWorkers = nil
	p.OtherWorkers = nil

	for name, ch := range map[string]<-chan result{
		"SubmitWarehouse": p.SubmitWarehouse(noopJob),
		"SubmitOther":     p.SubmitOther(noopJob),
	} {
		t.Run(name, func(t *testing.T) {
			v := recvOne(t, ch) // таймаут внутри recvOne = тест на зависание
			if !errors.Is(v.Err, ErrNoWorkers) {
				t.Errorf("Err = %v, ожидали ErrNoWorkers", v.Err)
			}
			assertClosed(t, ch)
		})
	}

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop завис: отправка держала RLock")
	}
}

// recvOne читает обещанное единственное значение. Контракт «ровно одно значение
// и закрытие» — иначе вызывающий либо повиснет, либо примет остановку за успех.
// Ждём с таймаутом: сломанный drain должен падать тестом, а не висеть до общего
// таймаута go test.
func recvOne(t *testing.T, ch <-chan result) result {
	t.Helper()

	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("канал закрыт без результата: вызывающий принял бы остановку за успех")
		}

		return v
	case <-time.After(5 * time.Second):
		t.Fatal("результата нет: вызывающий повис бы на чтении resCh навсегда")

		return result{}
	}
}

// assertClosed проверяет, что после единственного значения канал закрыт.
func assertClosed(t *testing.T, ch <-chan result) {
	t.Helper()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("канал отдал второе значение: контракт — одно значение, затем закрытие")
		}
	case <-time.After(time.Second):
		t.Error("канал не закрыт после результата: вызывающий повиснет на втором чтении")
	}
}

func noopJob(string) (any, error) { return nil, nil }

// TestSubmitAfterStop — после Stop задача не принимается, и это ОШИБКА, а не
// пустой результат: закрытый канал с zero-value result{} вызывающий читает как
// успех, хотя в МС ничего не ушло.
func TestSubmitAfterStop(t *testing.T) {
	p := newTestPool(4, 4)
	p.Stop()

	for name, ch := range map[string]<-chan result{
		"SubmitWarehouse": p.SubmitWarehouse(noopJob),
		"SubmitOther":     p.SubmitOther(noopJob),
	} {
		t.Run(name, func(t *testing.T) {
			v := recvOne(t, ch)
			if !errors.Is(v.Err, ErrPoolStopped) {
				t.Errorf("Err = %v, ожидали ErrPoolStopped", v.Err)
			}
			if v.Value != nil {
				t.Errorf("Value = %v, ожидали nil: в МС ничего не ушло", v.Value)
			}
			assertClosed(t, ch)
		})
	}

	// Повторный Stop — no-op (once): двойной Shutdown не паникует.
	p.Stop()
}

// TestStopDrainsQueuedTask — задача, поставленная до Stop и не взятая воркером,
// получает ошибку от drain; сам Stop не виснет.
func TestStopDrainsQueuedTask(t *testing.T) {
	p := newTestPool(4, 4)

	warehouse := p.SubmitWarehouse(noopJob)
	other := p.SubmitOther(noopJob)

	done := make(chan struct{})

	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop завис: drain не разобрал очередь")
	}

	for name, ch := range map[string]<-chan result{"warehouse": warehouse, "other": other} {
		t.Run(name, func(t *testing.T) {
			v := recvOne(t, ch)
			if !errors.Is(v.Err, ErrPoolStopped) {
				t.Errorf("Err = %v, ожидали ErrPoolStopped", v.Err)
			}
			assertClosed(t, ch)
		})
	}
}

// TestSubmitRaceWithStop — главный регресс: SubmitWarehouse параллельно со Stop.
// До правки горутина проходила проверку и доезжала до отправки уже после close →
// panic "send on closed channel" в незатреканной горутине → краш процесса при
// ШТАТНОЙ остановке. Проверяем и обратную сторону: ни один вызывающий не висит.
func TestSubmitRaceWithStop(t *testing.T) {
	const (
		rounds     = 50
		submitters = 8
	)

	for range rounds {
		p := newTestPool(submitters, submitters)

		chans := make(chan (<-chan result), submitters)

		var subWG sync.WaitGroup
		for range submitters {
			subWG.Go(func() {
				chans <- p.SubmitWarehouse(noopJob)
			})
		}

		stopDone := make(chan struct{})

		go func() {
			p.Stop()
			close(stopDone)
		}()

		subWG.Wait()
		close(chans)

		select {
		case <-stopDone:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop завис: отправка и закрытие канала заклинили друг друга")
		}

		for ch := range chans {
			v := recvOne(t, ch)
			// Воркеры-заглушки задачи не разбирают: любая задача либо
			// отвергнута, либо разобрана drain'ом.
			if !errors.Is(v.Err, ErrPoolStopped) {
				t.Errorf("Err = %v, ожидали ErrPoolStopped", v.Err)
			}
			assertClosed(t, ch)
		}
	}
}

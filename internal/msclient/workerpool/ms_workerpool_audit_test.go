package workerpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// newAuditOnlyPool — пул, в котором живёт ТОЛЬКО воркер группы аудита.
//
// Общий воркер — заглушка без горутины: SubmitOther должен пройти проверку
// «воркеров нет» (ErrNoWorkers), а разбирает otherTasks живой аудитный воркер —
// именно его приоритет «сначала others, потом audit» и проверяется. Задач
// аудита в этих тестах никто, кроме аудитного воркера, взять не может.
func newAuditOnlyPool(t *testing.T) *MSWorkerPool {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	p := &MSWorkerPool{
		OtherWorkers:   []*MSOtherWorker{{}},
		warehouseTasks: make(chan task, 8),
		otherTasks:     make(chan task, 8),
		auditTasks:     make(chan task, 8),
		ctx:            ctx,
		cancel:         cancel,
		backoff:        time.Millisecond,
	}

	w := &MSAuditWorker{Name: "тест-аудит", APIKey: "key-aud", rateLimiter: testLimiter()}
	p.AuditWorkers = append(p.AuditWorkers, w)
	p.wg.Add(1)

	go p.auditWorkerLoop(w)

	t.Cleanup(p.Stop)

	return p
}

// TestAuditWaitsForOthers — семантика приоритета: аудитный воркер сначала
// разбирает общий канал. Пока общий канал не пуст, задача аудита ждёт, даже
// если воркер освободился.
//
// Порядок проверяется детерминированно: воркер занят «долгой» общей задачей,
// пока в otherTasks и auditTasks уже лежат следующие задачи. После Release
// воркер обязан взять other-2 (приоритет), и только потом audit.
func TestAuditWaitsForOthers(t *testing.T) {
	p := newAuditOnlyPool(t)

	release := make(chan struct{})
	started := make(chan struct{})

	var (
		mu    sync.Mutex
		order []string
	)

	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	other1 := p.SubmitOther(func(string) (any, error) {
		record("other-1")
		close(started)
		<-release

		return "ok", nil
	})

	<-started // other-1 выполняется и держит воркер

	other2 := p.SubmitOther(func(string) (any, error) {
		record("other-2")

		return "ok", nil
	})

	auditCh := p.SubmitAudit(func(apiKey string) (any, error) {
		record("audit")

		return apiKey, nil
	})

	// Воркер занят other-1: аудит выполняться не должен (это же гарантирует,
	// что auditCh ниже читается уже после освобождения воркера).
	select {
	case v := <-auditCh:
		t.Fatalf("аудит выполнился до освобождения воркера: %+v", v)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)

	if v := recvOne(t, other1); v.Err != nil {
		t.Fatalf("other-1: Err = %v", v.Err)
	}

	if v := recvOne(t, other2); v.Err != nil {
		t.Fatalf("other-2: Err = %v", v.Err)
	}

	auditRes := recvOne(t, auditCh)
	if auditRes.Err != nil {
		t.Fatalf("audit: Err = %v", auditRes.Err)
	}

	// Аудит обязан идти под ключом своей группы — его передаёт аудитный воркер.
	if auditRes.Value != "key-aud" {
		t.Errorf("audit: apiKey = %v, ожидали key-aud (ключ аудитного воркера)", auditRes.Value)
	}

	mu.Lock()
	defer mu.Unlock()

	want := []string{"other-1", "other-2", "audit"}
	if len(order) != len(want) {
		t.Fatalf("порядок = %v, ожидали %v", order, want)
	}

	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("порядок = %v, ожидали %v (аудит не должен обгонять others)", order, want)
		}
	}
}

// TestAuditRunsWhenOthersEmpty — обратная сторона приоритета: общий канал пуст,
// аудиттный воркер не простаивает и берёт задачу аудита.
func TestAuditRunsWhenOthersEmpty(t *testing.T) {
	p := newAuditOnlyPool(t)

	v := recvOne(t, p.SubmitAudit(func(apiKey string) (any, error) {
		return apiKey, nil
	}))

	if v.Err != nil {
		t.Fatalf("Err = %v, ожидали успех", v.Err)
	}

	if v.Value != "key-aud" {
		t.Errorf("Value = %v, ожидали key-aud", v.Value)
	}
}

// TestSubmitAuditWithoutWorkers — MSAPI_KEYS_AUDIT пуст или все ключи битые:
// пул жив (собирается и останавливается), а SubmitAudit отвечает понятной
// ошибкой вместо ErrNoWorkers и не виснет в очереди без потребителя.
func TestSubmitAuditWithoutWorkers(t *testing.T) {
	p := newTestPool(4, 4)
	p.AuditWorkers = nil

	for name, ch := range map[string]<-chan result{
		"SubmitAudit":        p.SubmitAudit(noopJob),
		"SubmitAuditNoRetry": p.SubmitAuditNoRetry(noopJob),
	} {
		t.Run(name, func(t *testing.T) {
			v := recvOne(t, ch)
			if !errors.Is(v.Err, ErrNoAuditWorkers) {
				t.Errorf("Err = %v, ожидали ErrNoAuditWorkers", v.Err)
			}

			assertClosed(t, ch)
		})
	}

	// Пул останавливается штатно: ошибка аудита не ломает остальные группы.
	done := make(chan struct{})

	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop завис после SubmitAudit без аудит-воркеров")
	}
}

// TestSubmitAuditAfterStop — после Stop аудитная задача тоже получает
// ErrPoolStopped, а не пустой результат (иначе вызывающий принял бы остановку
// за успешно прочитанный аудит).
func TestSubmitAuditAfterStop(t *testing.T) {
	p := newTestPool(4, 4)
	p.Stop()

	v := recvOne(t, p.SubmitAudit(noopJob))
	if !errors.Is(v.Err, ErrPoolStopped) {
		t.Errorf("Err = %v, ожидали ErrPoolStopped", v.Err)
	}

	if v.Value != nil {
		t.Errorf("Value = %v, ожидали nil", v.Value)
	}

	// Повторная постановка после Stop: одно значение, затем закрытие канала.
	ch := p.SubmitAudit(noopJob)
	if v := recvOne(t, ch); !errors.Is(v.Err, ErrPoolStopped) {
		t.Errorf("повторная постановка: Err = %v, ожидали ErrPoolStopped", v.Err)
	}
	assertClosed(t, ch)
}

// TestStopWithQueuedAuditTask — главный риск новой группы: задача лежит в
// auditTasks, воркер её не взял (в тестовом пуле горутин нет), Stop обязан
// разобрать очередь и не паниковать на закрытии канала.
func TestStopWithQueuedAuditTask(t *testing.T) {
	p := newTestPool(4, 4)

	audit := p.SubmitAudit(noopJob)

	done := make(chan struct{})

	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop завис: drain не разобрал auditTasks")
	}

	v := recvOne(t, audit)
	if !errors.Is(v.Err, ErrPoolStopped) {
		t.Errorf("Err = %v, ожидали ErrPoolStopped", v.Err)
	}

	assertClosed(t, audit)

	// Идемпотентность: повторный Stop не паникует на уже закрытых каналах.
	p.Stop()
}

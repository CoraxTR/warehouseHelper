package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// startTestServer поднимает реальный http.Server на свободном порту.
// Serve (а не ListenAndServe) — чтобы порт выдала ОС и тест не зависел от
// занятых портов: тот же путь, что у сервера приложения.
func startTestServer(t *testing.T, handler http.Handler) (*http.Server, string) {
	t.Helper()

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушающий сокет: %v", err)
	}

	go func() {
		_ = srv.Serve(ln) //nolint:errcheck // Serve всегда возвращает ошибку при закрытии — не наш случай
	}()

	// Адрес даёт ОС: тесты не зависят от занятости конкретного порта.
	return srv, "http://" + ln.Addr().String()
}

// TestShutdownHTTPServer_WaitsForActiveRequest — главное свойство остановки:
// запрос, который уже выполняется, дорабатывает и получает ответ.
//
// Запас между сном хендлера и таймаутом — намеренно ×10: при близких
// значениях на загруженной машине тест флакал (таймаут срабатывал раньше
// ответа, и он «падал» на исправном коде).
func TestShutdownHTTPServer_WaitsForActiveRequest(t *testing.T) {
	started := make(chan struct{})

	srv, url := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started) // запрос один: тест шлёт ровно один
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { _ = srv.Close() })

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)

	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			errCh <- err

			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err

			return
		}
		respCh <- resp
	}()

	<-started

	if err := shutdownHTTPServer(srv, 5*time.Second); err != nil {
		t.Fatalf("остановка сервера: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("запрос не доработал: %v", err)
	case resp := <-respCh:
		defer resp.Body.Close()

		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Errorf("чтение ответа: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("код ответа = %d, ожидали %d", resp.StatusCode, http.StatusOK)
		}
	case <-time.After(time.Second):
		t.Fatal("запрос не завершился: Shutdown не дождался активного запроса")
	}
}

// TestShutdownHTTPServer_Timeout — если запрос не укладывается в таймаут,
// функция возвращает ошибку и НЕ висит: процесс должен уметь завершиться.
//
// Таймаут здесь маленький (100 мс) осознанно: хендлер держим открытым до конца
// теста, поэтому «не уложились» гарантировано, и проверяем мы не ожидание, а
// что функция возвращает ErrStopIncomplete/DeadlineExceeded, а не висит.
func TestShutdownHTTPServer_Timeout(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	inHandler := make(chan struct{})

	srv, url := startTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(inHandler)
		<-release
	}))
	t.Cleanup(func() {
		unblock()
		_ = srv.Close()
	})

	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
	}()

	<-inHandler // запрос точно выполняется — иначе таймаут ничего не проверил бы

	start := time.Now()
	err := shutdownHTTPServer(srv, 100*time.Millisecond)

	if err == nil {
		t.Fatal("ожидали ошибку таймаута, получили nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ошибка = %v, ожидали context.DeadlineExceeded", err)
	}
	// И признак «остановка неполная»: по нему main отличает брошенную работу от
	// отказа запуска и не роняет службу в глазах NSSM.
	if !errors.Is(err, ErrStopIncomplete) {
		t.Errorf("ошибка = %v, ожидали ErrStopIncomplete в цепочке", err)
	}
	// Функция не должна ждать вечно: таймаут + небольшой запас на закрытие.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("остановка заняла %s — функция зависла на таймауте", elapsed)
	}
}

// TestWaitBackground — ждём завершения и не ждём больше таймаута.
func TestWaitBackground(t *testing.T) {
	tests := []struct {
		name     string
		hold     time.Duration
		timeout  time.Duration
		wantDone bool
	}{
		{name: "дожидается завершения", hold: 50 * time.Millisecond, timeout: time.Second, wantDone: true},
		{name: "возвращает false по таймауту", hold: time.Second, timeout: 50 * time.Millisecond, wantDone: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wg sync.WaitGroup
			wg.Go(func() {
				time.Sleep(tt.hold)
			})

			if got := waitBackground(&wg, tt.timeout); got != tt.wantDone {
				t.Errorf("waitBackground = %v, ожидали %v", got, tt.wantDone)
			}

			// Не оставляем горутину-наблюдателя: дожидаемся её в конце теста.
			wg.Wait()
		})
	}
}

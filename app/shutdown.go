package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Таймауты остановки. Почему именно такие:
//
//   - httpShutdownTimeout — активным запросам даём доработать. Среди них есть
//     долгие: выгрузка каталога/заказов из МС, печать бланков пачкой за день
//     (склейка PDF), экспорт Excel. Они ходят во внешний API и пишут файл;
//     оборвать их — отдать пользователю битый файл. 30 секунд покрывают
//     самый долгий из наблюдаемых сценариев.
//   - backgroundShutdownTimeout — фоновые тикеры (размеры таблиц, daystate,
//     напоминания жалоб, returns, reservewatch) уважают ctx и выходят на
//     ближайшем select. 15 секунд — с запасом на тик, попавший в запрос к БД/МС.
//
// ВАЖНО: сумма этих таймаутов должна быть МЕНЬШЕ таймаута остановки службы
// (NSSM AppStopTimeout или аналог в менеджере служб): иначе процесс убьют
// раньше, чем мы допишем «остановлено штатно», и в логе не останется причины.
const (
	httpShutdownTimeout       = 30 * time.Second
	backgroundShutdownTimeout = 15 * time.Second
)

// shutdownHTTPServer останавливает http-сервер: новые соединения больше не
// принимаются, активные запросы дорабатывают (до timeout).
//
// context.DeadlineExceeded — ожидаемый исход «дождались не всё»: какой-то
// запрос не успел за таймаут, сервер закрывает соединения. Это WARN, но
// ошибку отдаём наверх — вызывающий решает, считать ли остановку штатной.
func shutdownHTTPServer(srv *http.Server, timeout time.Duration) error {
	if srv == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := srv.Shutdown(ctx)
	switch {
	case err == nil:
		return nil
	// ListenAndServe/Close могут вернуть ErrServerClosed (сервер уже не
	// принимает) — для остановки это не ошибка.
	case errors.Is(err, http.ErrServerClosed):
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		slog.Warn(fmt.Sprintf("http: дождались не всё — активные запросы не уложились в %s, соединения закрыты", timeout))
		return err
	default:
		slog.Error(fmt.Sprintf("http: остановка сервера: %v", err))
		return err
	}
}

// waitBackground ждёт завершения фоновых горутин; false — не дождались за
// timeout (горутина висит в запросе к БД/МС). Возвращаем bool, а не ошибку:
// это не сбой остановки, а решение «идём дальше и не держим процесс».
//
// Горутина-наблюдатель утечёт, если wg.Wait() так и не вернётся, — но процесс
// в этот момент уже завершается, а держать ожидание дольше таймаута нельзя.
func waitBackground(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true

	case <-time.After(timeout):
		slog.Warn(fmt.Sprintf("фон: задачи не завершились за %s — продолжаем остановку", timeout))
		return false
	}
}

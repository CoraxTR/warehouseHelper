// Сервис печати бланков заказов: скачивает бланки источником, кэширует их
// в temp-директории и сливает пачку в один PDF.
//
// Провал скачивания одного бланка НЕ роняет пачку: провал логируется и
// повторяется (attempts попыток с растущей паузой), а id непокорённых бланков
// возвращаются вызывающему — «молча не отдать бланк» нельзя, оператор должен
// видеть, чего не хватает в слитом файле. Если не скачалось ничего — ошибка.
package pdfexport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"warehouseHelper/internal/tempdir"
)

const (
	// defaultAttempts — сколько всего попыток скачивания бланка (1 + повторы).
	defaultAttempts = 3
	// defaultBackoff — пауза перед первой повторной попыткой (далее удваивается).
	defaultBackoff = 500 * time.Millisecond
	// cacheFilePerm — права на файл кэша бланка (как у прежнего кэша refgo).
	cacheFilePerm = 0o600
)

// errEmptyPDF — источник ответил успешно, но тело пустое (битый бланк).
var errEmptyPDF = errors.New("источник вернул пустой бланк")

// Fetcher — источник бланка заказа по его id. Реализация — *client.MSAPIClient
// (бланк = печатная форма шаблона МС, POST entity/customerorder/{id}/export/).
type Fetcher interface {
	FetchOrderPDF(ctx context.Context, id string) ([]byte, error)
}

// Merger — запись/слияние PDF. Реализация — *Exporter того же пакета.
type Merger interface {
	ExportOrderPDF(data []byte) (string, error)
	ExportMergedPDF(data [][]byte) (string, error)
}

// Service — печать бланков заказов (кэш temp/<id>.pdf + слияние в один PDF).
type Service struct {
	fetcher  Fetcher
	merger   Merger
	attempts int
	backoff  time.Duration
	dir      string
}

// NewService создаёт сервис печати бланков с клиентом-источником и слиятелем.
func NewService(fetcher Fetcher, merger Merger) *Service {
	return &Service{
		fetcher:  fetcher,
		merger:   merger,
		attempts: defaultAttempts,
		backoff:  defaultBackoff,
		dir:      tempdir.Dir,
	}
}

// GetOrderPDF — бланк одного заказа: файл из кэша либо скачивание с повторами.
func (s *Service) GetOrderPDF(ctx context.Context, id string) (string, error) {
	data, err := s.orderPDF(ctx, id)
	if err != nil {
		return "", fmt.Errorf("бланк заказа %s: %w", id, err)
	}

	savePath, err := s.merger.ExportOrderPDF(data)
	if err != nil {
		return "", fmt.Errorf("запись бланка заказа %s: %w", id, err)
	}

	return savePath, nil
}

// GetMultipleOrdersPDF — пачка бланков одним PDF-файлом (порядок ids =
// порядок страниц). Возвращает путь к файлу и id бланков, которые скачать не
// удалось (они пропущены в файле, провалы в логе). Ошибка — только если не
// скачалось НИЧЕГО (сливать нечего) либо упало слияние/запись.
func (s *Service) GetMultipleOrdersPDF(ctx context.Context, ids []string) (path string, skipped []string, err error) {
	data := make([][]byte, len(ids))
	failed := make([]bool, len(ids))
	skipped = make([]string, 0)
	var wg sync.WaitGroup

	for i, id := range ids {
		wg.Go(func() {
			got, err := s.orderPDF(ctx, id)
			if err != nil {
				slog.Error("бланк заказа пропущен в слитом файле", "order_id", id, "err", err)
				failed[i] = true

				return
			}

			data[i] = got
		})
	}

	wg.Wait()

	// Сжимаем выборку: пропущенные бланки уезжают вызывающему отдельным списком.
	merged := make([][]byte, 0, len(data))
	for i, d := range data {
		if failed[i] {
			skipped = append(skipped, ids[i])

			continue
		}

		merged = append(merged, d)
	}

	if len(merged) == 0 {
		return "", skipped, fmt.Errorf("не удалось получить ни одного бланка (запрошено %d)", len(ids))
	}

	path, mergeErr := s.merger.ExportMergedPDF(merged)
	if mergeErr != nil {
		return "", skipped, fmt.Errorf("слияние бланков: %w", mergeErr)
	}

	slog.Info("бланки слиты", "получено", len(merged), "пропущено", len(skipped), "файл", path)

	return path, skipped, nil
}

// orderPDF отдаёт данные бланка: сперва кэш temp/<id>.pdf, при промахе —
// скачивание с повторами (успешный ответ пишется в кэш).
func (s *Service) orderPDF(ctx context.Context, id string) ([]byte, error) {
	path := filepath.Join(s.dir, id+".pdf")

	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		return data, nil
	}

	if err == nil {
		// Пустой файл — след прерванной загрузки: удаляем и качаем заново,
		// иначе он уедет пустым в слияние и уронит его.
		s.removeCached(path, id)
	}

	data, err = s.fetchWithRetry(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(path, data, cacheFilePerm); err != nil {
		// Кэш — оптимизация: не смогли записать, бланк всё равно отдаём.
		slog.Error("не удалось закэшировать бланк заказа", "order_id", id, "err", err)
	}

	return data, nil
}

// fetchWithRetry качает бланк, повторяя попытку при временных ошибках
// (сеть, 5xx, пустой ответ). Постоянные ошибки источника (4xx) и отменённый
// контекст не повторяются: первый — смысла нет, второй — оператор ушёл.
func (s *Service) fetchWithRetry(ctx context.Context, id string) ([]byte, error) {
	var lastErr error

	for attempt := 1; attempt <= s.attempts; attempt++ {
		data, err := s.fetcher.FetchOrderPDF(ctx, id)
		if err == nil && len(data) == 0 {
			err = errEmptyPDF
		}

		if err == nil {
			slog.Info("бланк заказа получен", "order_id", id, "попытка", attempt)

			return data, nil
		}

		lastErr = err

		// Отмена (родительская или внутри клиента) повтора не заслуживает:
		// оператор ушёл со страницы. Постоянный отказ источника — тоже.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || isPermanent(err) || attempt == s.attempts {
			break
		}

		slog.Warn("бланк заказа не получен, повторяю", "order_id", id, "попытка", attempt, "err", err)

		select {
		case <-time.After(s.backoffFor(attempt)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, lastErr
}

// backoffFor — пауза перед повтором: backoff, затем удвоение (500мс, 1с, ...).
func (s *Service) backoffFor(attempt int) time.Duration {
	return s.backoff * time.Duration(1<<(attempt-1))
}

// removeCached удаляет файл кэша, игнорируя его отсутствие.
func (s *Service) removeCached(path, id string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("не удалось удалить битый кэш бланка", "order_id", id, "err", err)
	}
}

// permanentError — ошибка источника, повтор которой бессмысленен (МС отверг
// запрос: 4xx). Реализуется client.MSAPIError.
type permanentError interface {
	Permanent() bool
}

// isPermanent сообщает, помечена ли ошибка как постоянная.
func isPermanent(err error) bool {
	var perm permanentError

	return errors.As(err, &perm) && perm.Permanent()
}

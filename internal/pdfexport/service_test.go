// Тесты сервиса печати бланков: кэш, слияние, повторы при временных сбоях и
// «не молча» — id неполученных бланков возвращаются вызывающему.
package pdfexport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const (
	testIDFirst  = "order-1"
	testIDSecond = "order-2"
	testIDThird  = "order-3"

	mergedName   = "merged.pdf"
	exportedName = "exported.pdf"
)

// fakeFetcher — заглушка источника бланков: по id отдаёт данные, ошибку или
// падает первые N попыток (имитация временного сбоя).
type fakeFetcher struct {
	data      map[string][]byte
	errs      map[string]error
	failFirst map[string]int

	mu    sync.Mutex
	calls map[string]int
}

func (f *fakeFetcher) FetchOrderPDF(_ context.Context, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.calls == nil {
		f.calls = map[string]int{}
	}

	f.calls[id]++

	if n, ok := f.failFirst[id]; ok && f.calls[id] <= n {
		return nil, errTransient
	}

	if err, ok := f.errs[id]; ok {
		return nil, err
	}

	return f.data[id], nil
}

// errTransient — временный сбой (сеть, 5xx): повтор осмыслен.
var errTransient = errors.New("временный сбой источника")

// errPermanent — постоянный отказ источника (4xx МС): повтор бессмыслен.
type fakePermanentError struct{}

func (fakePermanentError) Error() string { return "источник отверг запрос" }

func (fakePermanentError) Permanent() bool { return true }

// fakeMerger — заглушка записи/слияния: запоминает вход слияния.
type fakeMerger struct {
	merged   [][]byte
	mergeErr error
}

func (m *fakeMerger) ExportOrderPDF(_ []byte) (string, error) {
	return filepath.Join("temp", exportedName), nil
}

func (m *fakeMerger) ExportMergedPDF(data [][]byte) (string, error) {
	if m.mergeErr != nil {
		return "", m.mergeErr
	}

	m.merged = data

	return filepath.Join("temp", mergedName), nil
}

// newTestService собирает сервис с отдельной temp-директорией и без пауз между
// попытками (тесты не должны спать).
func newTestService(t *testing.T, fetcher Fetcher, merger Merger) *Service {
	t.Helper()

	svc := NewService(fetcher, merger)
	svc.dir = t.TempDir()
	svc.backoff = 0

	return svc
}

func TestGetMultipleOrdersPDF_MergesInRequestOrder(t *testing.T) {
	fetcher := &fakeFetcher{data: map[string][]byte{
		testIDFirst:  []byte("pdf-1"),
		testIDSecond: []byte("pdf-2"),
		testIDThird:  []byte("pdf-3"),
	}}
	merger := &fakeMerger{}
	svc := newTestService(t, fetcher, merger)

	path, skipped, err := svc.GetMultipleOrdersPDF(context.Background(),
		[]string{testIDThird, testIDFirst, testIDSecond})
	if err != nil {
		t.Fatalf("GetMultipleOrdersPDF() error = %v", err)
	}

	if path != filepath.Join("temp", mergedName) {
		t.Errorf("path = %q, want %q", path, filepath.Join("temp", mergedName))
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want пусто", skipped)
	}
	if len(merger.merged) != 3 {
		t.Fatalf("в слияние ушло %d файлов, want 3", len(merger.merged))
	}
	for i, want := range []string{"pdf-3", "pdf-1", "pdf-2"} {
		if string(merger.merged[i]) != want {
			t.Errorf("порядок слияния нарушен: [%d] = %q, want %q", i, merger.merged[i], want)
		}
	}
}

func TestGetMultipleOrdersPDF_SkipsFailedAndReportsThem(t *testing.T) {
	// Бланк второго заказа не получен — в файл он не попадает, но и не
	// пропадает молча: id уезжает вызывающему.
	fetcher := &fakeFetcher{
		data: map[string][]byte{testIDFirst: []byte("pdf-1"), testIDThird: []byte("pdf-3")},
		errs: map[string]error{testIDSecond: fakePermanentError{}},
	}
	merger := &fakeMerger{}
	svc := newTestService(t, fetcher, merger)

	path, skipped, err := svc.GetMultipleOrdersPDF(context.Background(),
		[]string{testIDFirst, testIDSecond, testIDThird})
	if err != nil {
		t.Fatalf("GetMultipleOrdersPDF() error = %v", err)
	}
	if path == "" {
		t.Error("path пуст, ожидался файл слияния")
	}
	if len(skipped) != 1 || skipped[0] != testIDSecond {
		t.Fatalf("skipped = %v, want [%s]", skipped, testIDSecond)
	}
	if len(merger.merged) != 2 {
		t.Fatalf("в слияние ушло %d файлов, want 2 (без провалившегося)", len(merger.merged))
	}
}

func TestGetMultipleOrdersPDF_AllFailedIsError(t *testing.T) {
	fetcher := &fakeFetcher{errs: map[string]error{
		testIDFirst:  fakePermanentError{},
		testIDSecond: fakePermanentError{},
	}}
	merger := &fakeMerger{}
	svc := newTestService(t, fetcher, merger)

	path, skipped, err := svc.GetMultipleOrdersPDF(context.Background(),
		[]string{testIDFirst, testIDSecond})
	if err == nil {
		t.Fatal("ожидалась ошибка: ни одного бланка не получено")
	}
	if path != "" {
		t.Errorf("path = %q, want пусто", path)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %v, want оба id", skipped)
	}
	if merger.merged != nil {
		t.Error("слияние не должно вызываться без данных")
	}
}

func TestGetMultipleOrdersPDF_RetriesTransientFailure(t *testing.T) {
	// Первые две попытки падают (сеть), третья отдаёт бланк.
	fetcher := &fakeFetcher{
		data:      map[string][]byte{testIDFirst: []byte("pdf-1")},
		failFirst: map[string]int{testIDFirst: 2},
	}
	svc := newTestService(t, fetcher, &fakeMerger{})

	_, skipped, err := svc.GetMultipleOrdersPDF(context.Background(), []string{testIDFirst})
	if err != nil {
		t.Fatalf("GetMultipleOrdersPDF() error = %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want пусто (повтор удался)", skipped)
	}
	if got := fetcher.calls[testIDFirst]; got != 3 {
		t.Errorf("попыток скачивания = %d, want 3 (2 провала + успех)", got)
	}
}

func TestGetMultipleOrdersPDF_DoesNotRetryPermanentFailure(t *testing.T) {
	fetcher := &fakeFetcher{errs: map[string]error{testIDFirst: fakePermanentError{}}}
	svc := newTestService(t, fetcher, &fakeMerger{})

	if _, _, err := svc.GetMultipleOrdersPDF(context.Background(), []string{testIDFirst}); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if got := fetcher.calls[testIDFirst]; got != 1 {
		t.Errorf("попыток скачивания = %d, want 1 (постоянный отказ не повторяем)", got)
	}
}

func TestGetMultipleOrdersPDF_DoesNotRetryCancelledFetch(t *testing.T) {
	fetcher := &fakeFetcher{errs: map[string]error{testIDFirst: context.Canceled}}
	svc := newTestService(t, fetcher, &fakeMerger{})

	if _, _, err := svc.GetMultipleOrdersPDF(context.Background(), []string{testIDFirst}); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if got := fetcher.calls[testIDFirst]; got != 1 {
		t.Errorf("попыток скачивания = %d, want 1 (отмену не повторяем)", got)
	}
}

func TestGetMultipleOrdersPDF_EmptyCachedFileRefetched(t *testing.T) {
	// Пустой файл в кэше — след прерванной загрузки: удаляем и качаем заново,
	// иначе он уедет пустым в слияние и уронит его.
	svc := newTestService(t, &fakeFetcher{data: map[string][]byte{testIDFirst: []byte("pdf-1")}}, &fakeMerger{})

	cached := filepath.Join(svc.dir, testIDFirst+".pdf")
	if err := os.WriteFile(cached, nil, 0o600); err != nil {
		t.Fatalf("не удалось создать пустой кэш: %v", err)
	}

	if _, _, err := svc.GetMultipleOrdersPDF(context.Background(), []string{testIDFirst}); err != nil {
		t.Fatalf("GetMultipleOrdersPDF() error = %v", err)
	}

	data, err := os.ReadFile(cached)
	if err != nil {
		t.Fatalf("кэш должен быть перезаписан: %v", err)
	}
	if len(data) == 0 {
		t.Error("файл кэша остался пустым после перекачивания")
	}
}

func TestGetOrderPDF_UsesCacheWithoutFetch(t *testing.T) {
	fetcher := &fakeFetcher{}
	svc := newTestService(t, fetcher, &fakeMerger{})

	cached := filepath.Join(svc.dir, testIDFirst+".pdf")
	if err := os.WriteFile(cached, []byte("from-cache"), 0o600); err != nil {
		t.Fatalf("не удалось создать кэш: %v", err)
	}

	path, err := svc.GetOrderPDF(context.Background(), testIDFirst)
	if err != nil {
		t.Fatalf("GetOrderPDF() error = %v", err)
	}
	if path != filepath.Join("temp", exportedName) {
		t.Errorf("path = %q, want %q", path, filepath.Join("temp", exportedName))
	}
	if got := fetcher.calls[testIDFirst]; got != 0 {
		t.Errorf("обращений к источнику = %d, want 0 (данные из кэша)", got)
	}
}

func TestGetOrderPDF_NoCacheFileOnFailedFetch(t *testing.T) {
	fetcher := &fakeFetcher{errs: map[string]error{testIDFirst: fakePermanentError{}}}
	svc := newTestService(t, fetcher, &fakeMerger{})

	if _, err := svc.GetOrderPDF(context.Background(), testIDFirst); err == nil {
		t.Fatal("ожидалась ошибка")
	}

	if _, err := os.Stat(filepath.Join(svc.dir, testIDFirst+".pdf")); !os.IsNotExist(err) {
		t.Errorf("файл не должен создаваться при провале скачивания, stat err = %v", err)
	}
}

func TestGetMultipleOrdersPDF_MergeErrorPropagates(t *testing.T) {
	wantErr := errors.New("слияние не удалось")
	fetcher := &fakeFetcher{data: map[string][]byte{testIDFirst: []byte("pdf-1")}}
	svc := newTestService(t, fetcher, &fakeMerger{mergeErr: wantErr})

	path, skipped, err := svc.GetMultipleOrdersPDF(context.Background(), []string{testIDFirst})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if path != "" {
		t.Errorf("path = %q, want пусто", path)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want пусто", skipped)
	}
}

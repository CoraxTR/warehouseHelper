package usecase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warehouseHelper/internal/tempdir"
)

// fakePDFFetcher — заглушка PDFFetcher: отдаёт фиксированные данные или ошибку.
type fakePDFFetcher struct {
	data  []byte
	err   error
	calls int
}

func (f *fakePDFFetcher) FetchOrderPDF(_ context.Context, _ string) ([]byte, error) {
	f.calls++
	return f.data, f.err
}

type fakePDFExporter struct{}

func (e *fakePDFExporter) ExportOrderPDF(_ []byte) (string, error) {
	return filepath.Join(tempdir.Dir, "exported.pdf"), nil
}

// ExportMergedPDF имитирует поведение pdfcpu: пустой вход — ошибка merge.
func (e *fakePDFExporter) ExportMergedPDF(data [][]byte) (string, error) {
	for i, b := range data {
		if len(b) == 0 {
			return "", fmt.Errorf("file %d could not be opened because it is empty", i)
		}
	}
	return filepath.Join(tempdir.Dir, "merged.pdf"), nil
}

type fakePreloader struct{}

func (p *fakePreloader) StopPreloading() {}

func TestMain(m *testing.M) {
	if err := os.MkdirAll(tempdir.Dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}

	m.Run()
	_ = os.RemoveAll(tempdir.Dir)
}

// pdfCachePath повторяет логику построения пути кэша из юзкейса.
func pdfCachePath() string {
	return filepath.Join(tempdir.Dir, testID+".pdf")
}

const testID = "abc-123"

func newTestPDFUseCase(fetcher *fakePDFFetcher) *ExportOrderPDFUseCase {
	return NewExportOrderPDFUseCase(fetcher, &fakePDFExporter{}, &fakePreloader{})
}

func TestGetMultipleOrdersPDF_EmptyCachedFileRefetched(t *testing.T) {
	// Пустой файл в кэше (след прерванной загрузки) не должен попасть в merge:
	// его нужно удалить и перекачать заново.
	emptyFile := pdfCachePath()
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatalf("failed to create empty cached file: %v", err)
	}

	pdfData := []byte("%PDF-1.4\nfake pdf content")
	fetcher := &fakePDFFetcher{data: pdfData}
	uc := newTestPDFUseCase(fetcher)

	path, err := uc.GetMultipleOrdersPDF(context.Background(), []string{testID})
	if err != nil {
		t.Fatalf("GetMultipleOrdersPDF() error = %v", err)
	}
	if !strings.Contains(path, "merged") {
		t.Errorf("expected merged pdf path, got %q", path)
	}
	if fetcher.calls != 1 {
		t.Errorf("expected refetch (1 call), got %d calls", fetcher.calls)
	}

	cached, err := os.ReadFile(emptyFile)
	if err != nil {
		t.Fatalf("cached file should be written after refetch: %v", err)
	}
	if len(cached) == 0 {
		t.Error("cached file is still empty after refetch")
	}
}

func TestGetMultipleOrdersPDF_FetchErrorRemovesEmptyFile(t *testing.T) {
	// Если загрузка прервана (отмена контекста), пустой файл должен быть удалён,
	// а не остаться в кэше — иначе следующий merge упадёт на нём.
	emptyFile := pdfCachePath()
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatalf("failed to create empty cached file: %v", err)
	}

	fetcher := &fakePDFFetcher{err: context.Canceled}
	uc := newTestPDFUseCase(fetcher)

	_, err := uc.GetMultipleOrdersPDF(context.Background(), []string{testID})
	if err == nil {
		t.Fatal("expected error from merge with missing pdf data")
	}

	if _, statErr := os.Stat(emptyFile); !os.IsNotExist(statErr) {
		t.Errorf("empty cached file should be removed after failed fetch, stat err = %v", statErr)
	}
}

func TestGetMultipleOrdersPDF_NoEmptyFileOnCancel(t *testing.T) {
	// Фетчер вернул ошибку (как делает FetchOrderPDF при отмене контекста) —
	// файл не должен создаваться вовсе.
	filePath := pdfCachePath()

	fetcher := &fakePDFFetcher{err: context.Canceled}
	uc := newTestPDFUseCase(fetcher)

	if _, err := uc.GetMultipleOrdersPDF(context.Background(), []string{testID}); err == nil {
		t.Fatal("expected error")
	}

	if _, statErr := os.Stat(filePath); !os.IsNotExist(statErr) {
		t.Errorf("no file should be created on failed fetch, stat err = %v", statErr)
	}
}

func TestGetOrderPDF_EmptyCachedFileRefetched(t *testing.T) {
	emptyFile := pdfCachePath()
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatalf("failed to create empty cached file: %v", err)
	}

	pdfData := []byte("%PDF-1.4\nfake pdf content")
	fetcher := &fakePDFFetcher{data: pdfData}
	uc := newTestPDFUseCase(fetcher)

	path, err := uc.GetOrderPDF(context.Background(), testID)
	if err != nil {
		t.Fatalf("GetOrderPDF() error = %v", err)
	}
	if path != filepath.Join(tempdir.Dir, "exported.pdf") {
		t.Errorf("expected exported pdf path %q, got %q", filepath.Join(tempdir.Dir, "exported.pdf"), path)
	}
	if fetcher.calls != 1 {
		t.Errorf("expected refetch (1 call), got %d calls", fetcher.calls)
	}

	cached, err := os.ReadFile(emptyFile)
	if err != nil {
		t.Fatalf("cached file should be written after refetch: %v", err)
	}
	if len(cached) == 0 {
		t.Error("cached file is still empty after refetch")
	}
}

func TestGetOrderPDF_FetchErrorRemovesEmptyFile(t *testing.T) {
	emptyFile := pdfCachePath()
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatalf("failed to create empty cached file: %v", err)
	}

	fetcher := &fakePDFFetcher{err: context.Canceled}
	uc := newTestPDFUseCase(fetcher)

	if _, err := uc.GetOrderPDF(context.Background(), testID); err == nil {
		t.Fatal("expected error on cancelled fetch")
	}

	if _, statErr := os.Stat(emptyFile); !os.IsNotExist(statErr) {
		t.Errorf("empty cached file should be removed after failed fetch, stat err = %v", statErr)
	}
}

func TestGetOrderPDF_NoFileCreatedOnCancel(t *testing.T) {
	filePath := pdfCachePath()

	fetcher := &fakePDFFetcher{err: context.Canceled}
	uc := newTestPDFUseCase(fetcher)

	if _, err := uc.GetOrderPDF(context.Background(), testID); err == nil {
		t.Fatal("expected error on cancelled fetch")
	}

	if _, statErr := os.Stat(filePath); !os.IsNotExist(statErr) {
		t.Errorf("no file should be created on failed fetch, stat err = %v", statErr)
	}
}

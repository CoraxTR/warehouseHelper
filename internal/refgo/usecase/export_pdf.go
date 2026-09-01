package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"log/slog"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/tempdir"
)

// (trackPkg объявлена в первом файле пакета)

type PDFFetcher interface {
	FetchOrderPDF(ctx context.Context, id string) ([]byte, error)
}

type PDFExporter interface {
	ExportOrderPDF(data []byte) (string, error)
	ExportMergedPDF(data [][]byte) (string, error)
}

type PDFPreloader interface {
	StopPreloading()
}

type ExportOrderPDFUseCase struct {
	fetcher   PDFFetcher
	exporter  PDFExporter
	preloader PDFPreloader
}

func NewExportOrderPDFUseCase(fetcher PDFFetcher, exporter PDFExporter, preloader PDFPreloader) *ExportOrderPDFUseCase {
	return &ExportOrderPDFUseCase{
		fetcher:   fetcher,
		exporter:  exporter,
		preloader: preloader,
	}
}

func (uc *ExportOrderPDFUseCase) GetOrderPDF(ctx context.Context, id string) (string, error) {
	defer metrics.Track(trackPkg, "GetOrderPDF")()
	uc.preloader.StopPreloading()

	filePath := filepath.Join(tempdir.Dir, id+".pdf")

	data, err := os.ReadFile(filePath)
	if err == nil {
		if len(data) > 0 {
			return filePath, nil
		}

		// Пустой файл — след прерванной загрузки; удаляем и качаем заново.
		removeCachedPDF(filePath, id)
	}

	pdfData, err := uc.fetcher.FetchOrderPDF(ctx, id)
	if err != nil {
		return "", fmt.Errorf("failed to fetch PDF: %w", err)
	}

	if len(pdfData) == 0 {
		return "", fmt.Errorf("failed to fetch PDF for order %s: empty response", id)
	}

	err = os.WriteFile(filePath, pdfData, 0o600)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to save PDF for order %s: %v", id, err))
	}

	savePath, err := uc.exporter.ExportOrderPDF(pdfData)
	if err != nil {
		return "", fmt.Errorf("failed to export PDF: %w", err)
	}

	return savePath, nil
}

func (uc *ExportOrderPDFUseCase) GetMultipleOrdersPDF(ctx context.Context, ids []string) (string, error) {
	defer metrics.Track(trackPkg, "GetMultipleOrdersPDF")()
	uc.preloader.StopPreloading()

	pdfData := make([][]byte, len(ids))
	wg := sync.WaitGroup{}

	var counter int64

	for i, id := range ids {
		wg.Go(func() {
			doneCounter := atomic.AddInt64(&counter, 1)
			filePath := filepath.Join(tempdir.Dir, id+".pdf")

			data, err := os.ReadFile(filePath)
			if err == nil && len(data) > 0 {
				slog.Info(fmt.Sprintf("Fetched Order PDF %v/%v", doneCounter, len(ids)))

				pdfData[i] = data

				return
			}
			if err == nil {
				// Пустой файл — след прерванной загрузки; удаляем, чтобы
				// он не попал пустым в merge, и качаем заново.
				removeCachedPDF(filePath, id)
			}

			data, err = uc.fetcher.FetchOrderPDF(ctx, id)
			if err != nil {
				slog.Error(fmt.Sprintf("failed to fetch PDF: %s", err))

				return
			}

			if len(data) == 0 {
				slog.Info(fmt.Sprintf("fetched empty PDF data for order %s", id))

				return
			}

			err = os.WriteFile(filePath, data, 0o600)
			if err != nil {
				slog.Error(fmt.Sprintf("Failed to save PDF for order %s: %v", id, err))
			}

			slog.Info(fmt.Sprintf("Fetched Order PDF %v/%v", doneCounter, len(ids)))

			pdfData[i] = data
		})
	}

	wg.Wait()

	savePath, err := uc.exporter.ExportMergedPDF(pdfData)
	if err != nil {
		return "", fmt.Errorf("failed to export merged PDF: %w", err)
	}

	return savePath, nil
}

// removeCachedPDF удаляет файл из temp-кэша, игнорируя его отсутствие.
func removeCachedPDF(path, id string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error(fmt.Sprintf("Failed to remove cached PDF %s: %v", id, err))
	}
}

package usecase

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"warehouseHelper/internal/tempdir"
)

type PDFFetcher interface {
	FetchOrderPDF(ctx context.Context, href string) ([]byte, error)
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

func (uc *ExportOrderPDFUseCase) GetOrderPDF(ctx context.Context, href string) (string, error) {
	uc.preloader.StopPreloading()

	safeName := filepath.Base(strings.TrimSuffix(href, "/"))
	filePath := filepath.Join(tempdir.Dir, safeName+".pdf")

	data, err := os.ReadFile(filePath)
	if err == nil {
		if len(data) > 0 {
			return filePath, nil
		}

		// Пустой файл — след прерванной загрузки; удаляем и качаем заново.
		removeCachedPDF(filePath, href)
	}

	pdfData, err := uc.fetcher.FetchOrderPDF(ctx, href)
	if err != nil {
		return "", fmt.Errorf("failed to fetch PDF: %w", err)
	}

	if len(pdfData) == 0 {
		return "", fmt.Errorf("failed to fetch PDF for order %s: empty response", href)
	}

	err = os.WriteFile(filePath, pdfData, 0o600)
	if err != nil {
		log.Printf("Failed to save PDF for order %s: %v", href, err)
	}

	savePath, err := uc.exporter.ExportOrderPDF(pdfData)
	if err != nil {
		return "", fmt.Errorf("failed to export PDF: %w", err)
	}

	return savePath, nil
}

func (uc *ExportOrderPDFUseCase) GetMultipleOrdersPDF(ctx context.Context, hrefs []string) (string, error) {
	uc.preloader.StopPreloading()

	pdfData := make([][]byte, len(hrefs))
	wg := sync.WaitGroup{}

	var counter int64

	for i, href := range hrefs {
		wg.Go(func() {
			doneCounter := atomic.AddInt64(&counter, 1)
			safeName := filepath.Base(strings.TrimSuffix(href, "/"))
			filePath := filepath.Join(tempdir.Dir, safeName+".pdf")

			data, err := os.ReadFile(filePath)
			if err == nil && len(data) > 0 {
				log.Printf("Fetched Order PDF %v/%v", doneCounter, len(hrefs))

				pdfData[i] = data

				return
			}
			if err == nil {
				// Пустой файл — след прерванной загрузки; удаляем, чтобы
				// он не попал пустым в merge, и качаем заново.
				removeCachedPDF(filePath, href)
			}

			data, err = uc.fetcher.FetchOrderPDF(ctx, href)
			if err != nil {
				log.Printf("failed to fetch PDF: %s", err)

				return
			}

			if len(data) == 0 {
				log.Printf("fetched empty PDF data for order %s", href)

				return
			}

			err = os.WriteFile(filePath, data, 0o600)
			if err != nil {
				log.Printf("Failed to save PDF for order %s: %v", href, err)
			}

			log.Printf("Fetched Order PDF %v/%v", doneCounter, len(hrefs))

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
func removeCachedPDF(path, href string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Failed to remove cached PDF %s: %v", href, err)
	}
}

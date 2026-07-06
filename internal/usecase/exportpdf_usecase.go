package usecase

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	filePath := filepath.Join("..", "temp", safeName+".pdf")

	_, err := os.Stat(filePath)
	if err == nil {
		return filePath, nil
	}

	pdfData, err := uc.fetcher.FetchOrderPDF(ctx, href)
	if err != nil {
		return "", fmt.Errorf("failed to fetch PDF: %w", err)
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
			filePath := filepath.Join("..", "temp", safeName+".pdf")

			_, err := os.Stat(filePath)
			if err == nil {
				data, err := os.ReadFile(filePath)
				if err != nil {
					log.Printf("Failed to read PDF for order %s: %v", href, err)

					return
				}

				log.Printf("Fetched Order PDF %v/%v", doneCounter, len(hrefs))

				pdfData[i] = data

				return
			}

			data, err := uc.fetcher.FetchOrderPDF(ctx, href)
			if err != nil {
				log.Printf("failed to fetch PDF: %s", err)

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

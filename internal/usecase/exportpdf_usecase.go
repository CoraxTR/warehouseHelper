package usecase

import (
	"context"
	"fmt"
	"log"
)

type PDFFetcher interface {
	FetchOrderPDF(ctx context.Context, href string) ([]byte, error)
}

type PDFExporter interface {
	ExportOrderPDF(data []byte) (string, error)
	ExportMergedPDF(data [][]byte) (string, error)
}

type ExportOrderPDFUseCase struct {
	fetcher  PDFFetcher
	exporter PDFExporter
}

func NewExportOrderPDFUseCase(fetcher PDFFetcher, exporter PDFExporter) *ExportOrderPDFUseCase {
	return &ExportOrderPDFUseCase{
		fetcher:  fetcher,
		exporter: exporter,
	}
}

func (uc *ExportOrderPDFUseCase) GetOrderPDF(ctx context.Context, href string) (string, error) {
	pdfData, err := uc.fetcher.FetchOrderPDF(ctx, href)
	if err != nil {
		return "", fmt.Errorf("failed to fetch PDF: %w", err)
	}
	savePath, err := uc.exporter.ExportOrderPDF(pdfData)
	if err != nil {
		return "", fmt.Errorf("failed to export PDF: %w", err)
	}
	return savePath, nil
}

func (uc *ExportOrderPDFUseCase) GetMultipleOrdersPDF(ctx context.Context, hrefs []string) (string, error) {
	pdfData := make([][]byte, len(hrefs))
	for i, href := range hrefs {
		data, err := uc.fetcher.FetchOrderPDF(ctx, href)
		if err != nil {
			return "", fmt.Errorf("failed to fetch PDF for %s: %w", href, err)
		}
		log.Printf("Fetched Order PDF %v/%v", i, len(hrefs))
		pdfData[i] = data
	}
	savePath, err := uc.exporter.ExportMergedPDF(pdfData)
	if err != nil {
		return "", fmt.Errorf("failed to export merged PDF: %w", err)
	}
	return savePath, nil
}

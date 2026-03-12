package usecase

import (
	"context"
	"fmt"
)

type PDFFetcher interface {
	FetchOrderPDF(ctx context.Context, href string) ([]byte, error)
}

type PDFExporter interface {
	ExportOrderPDF(data []byte) (string, error)
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

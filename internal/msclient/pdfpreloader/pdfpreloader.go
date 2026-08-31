package pdfpreloader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/tempdir"
)

type PDFPreloader struct {
	msclient *client.MSAPIClient
	stopchan chan struct{}
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
}

func NewPDFPreloader(c *client.MSAPIClient) *PDFPreloader {
	err := os.MkdirAll(tempdir.Dir, 0o750)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to create temp dir: %v", err))
	}

	return &PDFPreloader{
		msclient: c,
		stopchan: make(chan struct{}, 1),
	}
}

func (p *PDFPreloader) StartPreloading(orders []*domain.InternalOrder) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cancel != nil {
		p.cancel()
		p.wg.Wait()
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	p.wg.Go(func() {
		for {
			select {
			case <-p.stopchan:
				cancel()

				return
			case <-ctx.Done():
				return
			}
		}
	})

	for _, order := range orders {
		p.wg.Add(1)

		go func(ctx context.Context, o *domain.InternalOrder) {
			defer p.wg.Done()

			select {
			case <-ctx.Done():
				return
			default:
			}

			safeName := o.GetID()
			filePath := filepath.Join(tempdir.Dir, safeName+".pdf")

			info, err := os.Stat(filePath)
			if err == nil && info.Size() > 0 {
				return
			}
			if err == nil {
				// Файл от прерванной загрузки остался пустым — удаляем,
				// чтобы при следующем проходе скачать его заново.
				removeFile(filePath)
			}

			data, err := p.msclient.FetchOrderPDF(ctx, o.GetID())
			if err != nil {
				slog.Error(fmt.Sprintf("Failed to fetch PDF for order %s: %v", o.GetID(), err))
				// Загрузка прервана (например, отменой контекста) — файл мог
				// остаться пустым или частичным; удаляем, чтобы он не попал
				// пустым в merge мульти-PDF.
				removeFile(filePath)

				return
			}

			if len(data) == 0 {
				slog.Info(fmt.Sprintf("Fetched empty PDF data for order %s", o.GetID()))
				removeFile(filePath)

				return
			}

			err = os.WriteFile(filePath, data, 0o600)
			if err != nil {
				slog.Error(fmt.Sprintf("Failed to save PDF for order %s: %v", o.GetID(), err))
			}
		}(ctx, order)
	}
}

func (p *PDFPreloader) StopPreloading() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cancel != nil {
		select {
		case p.stopchan <- struct{}{}:
		default:
		}
	}
}

// removeFile удаляет файл по пути, игнорируя его отсутствие.
func removeFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error(fmt.Sprintf("Failed to remove file %s: %v", path, err))
	}
}

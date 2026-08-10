package pdfpreloader

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/repository/msapiclient"
	"warehouseHelper/internal/tempdir"
)

type PDFPreloader struct {
	msapiclient *msapiclient.MSAPIClient
	stopchan    chan struct{}
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex
}

func NewPDFPreloader(client *msapiclient.MSAPIClient) *PDFPreloader {
	err := os.MkdirAll(tempdir.Dir, 0o750)
	if err != nil {
		log.Printf("Failed to create temp dir: %v", err)
	}

	return &PDFPreloader{
		msapiclient: client,
		stopchan:    make(chan struct{}, 1),
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

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		for {
			select {
			case <-p.stopchan:
				cancel()

				return
			case <-ctx.Done():
				return
			}
		}
	}()

	for _, order := range orders {
		p.wg.Add(1)

		go func(ctx context.Context, o *domain.InternalOrder) {
			defer p.wg.Done()

			select {
			case <-ctx.Done():
				return
			default:
			}

			safeName := filepath.Base(strings.TrimSuffix(o.GetHREF(), "/"))
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

			data, err := p.msapiclient.FetchOrderPDF(ctx, o.GetHREF())
			if err != nil {
				log.Printf("Failed to fetch PDF for order %s: %v", o.GetHREF(), err)
				// Загрузка прервана (например, отменой контекста) — файл мог
				// остаться пустым или частичным; удаляем, чтобы он не попал
				// пустым в merge мульти-PDF.
				removeFile(filePath)

				return
			}

			if len(data) == 0 {
				log.Printf("Fetched empty PDF data for order %s", o.GetHREF())
				removeFile(filePath)

				return
			}

			err = os.WriteFile(filePath, data, 0o600)
			if err != nil {
				log.Printf("Failed to save PDF for order %s: %v", o.GetHREF(), err)
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
		log.Printf("Failed to remove file %s: %v", path, err)
	}
}

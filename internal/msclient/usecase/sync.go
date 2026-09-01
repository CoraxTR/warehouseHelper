package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/msclient/pdfpreloader"
)

// (trackPkg объявлена в первом файле пакета)

type OrdersRepository interface {
	InsertOrders(ctx context.Context, orders []*domain.InternalOrder) error
}

type SyncUseCase struct {
	MSAPIClinet  *client.MSAPIClient
	DBClient     OrdersRepository
	Converter    *client.MSConverter
	Config       *config.RefGoConfig
	PDFPreloader *pdfpreloader.PDFPreloader
}

func NewSyncUsecase(client *client.MSAPIClient, repo OrdersRepository, converter *client.MSConverter,
	cfg *config.RefGoConfig, pdf *pdfpreloader.PDFPreloader) *SyncUseCase {
	return &SyncUseCase{
		MSAPIClinet:  client,
		DBClient:     repo,
		Converter:    converter,
		Config:       cfg,
		PDFPreloader: pdf,
	}
}

func (uc *SyncUseCase) SyncDeliverableOrders(ctx context.Context) {
	done := metrics.Track(trackPkg, "SyncDeliverableOrders")
	defer done()
	refGoCounter := uc.Config.RGNextOrder

	orders, err := uc.MSAPIClinet.FetchDeliverableOrders(ctx)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to fetch deliverable orders: %v", err))

		return
	}

	suitableOrders := make([]*client.MSOrder, 0, len(orders)/2)
	for _, o := range orders {
		if o.SuitableForDelivery() {
			suitableOrders = append(suitableOrders, o)
		}
	}

	wg := sync.WaitGroup{}
	countermu := sync.Mutex{}
	appendmu := sync.Mutex{}

	internalOrders := make([]*domain.InternalOrder, 0, len(suitableOrders))

	for _, o := range suitableOrders {
		//nolint:revive //false positive, we can't use wg.Go for goroutines with variables
		wg.Add(1)

		go func(order *client.MSOrder, ctx context.Context) {
			defer wg.Done()

			internalOrder := uc.Converter.ToDomain(order)

			if internalOrder.GetRefGoNumber() == "" {
				countermu.Lock()

				currentRefNumber := refGoCounter
				refGoCounter++

				countermu.Unlock()

				err := uc.MSAPIClinet.SetRefGoNumberOnly(ctx, internalOrder.GetID(), strconv.Itoa(int(currentRefNumber)))
				if err != nil {
					slog.Error(fmt.Sprintf("Failed to set RefGoNumber for order %s: %v", internalOrder.GetName(), err))
				}

				internalOrder.SetRefGoNumber(strconv.Itoa(int(currentRefNumber)))
				slog.Info(fmt.Sprintf("Assigned RefGoNumber: %v to order: %s", currentRefNumber, internalOrder.GetName()))
			}

			internalOrder.Validate()

			appendmu.Lock()

			internalOrders = append(internalOrders, internalOrder)

			appendmu.Unlock()
		}(o, ctx)
	}

	wg.Wait()

	uc.MSAPIClinet.Cache.AddOrdersToCache(internalOrders)
	uc.PDFPreloader.StartPreloading(internalOrders) //nolint:contextcheck //we have ctx cancellation by PDFPreloader.StopPreloading

	err = uc.DBClient.InsertOrders(ctx, internalOrders)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to insert orders into database: %v", err))
	}

	err = uc.Config.ChangeRefGoLatest(refGoCounter)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to update RefGoLatest to %d: %v", refGoCounter, err))
	}
}

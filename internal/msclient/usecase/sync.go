package usecase

import (
	"context"
	"log"
	"strconv"
	"sync"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/msclient/pdfpreloader"
)

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
	refGoCounter := uc.Config.RGNextOrder

	orders, err := uc.MSAPIClinet.FetchDeliverableOrders(ctx)
	if err != nil {
		log.Printf("Failed to fetch deliverable orders: %v", err)

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

				err := uc.MSAPIClinet.SetRefGoNumberOnly(ctx, internalOrder.GetHREF(), strconv.Itoa(int(currentRefNumber)))
				if err != nil {
					log.Printf("Failed to set RefGoNumber for order %s: %v", internalOrder.GetName(), err)
				}

				internalOrder.SetRefGoNumber(strconv.Itoa(int(currentRefNumber)))
				log.Printf("Assigned RefGoNumber: %v to order: %s", currentRefNumber, internalOrder.GetName())
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
		log.Printf("Failed to insert orders into database: %v", err)
	}

	err = uc.Config.ChangeRefGoLatest(refGoCounter)
	if err != nil {
		log.Printf("Failed to update RefGoLatest to %d: %v", refGoCounter, err)
	}
}

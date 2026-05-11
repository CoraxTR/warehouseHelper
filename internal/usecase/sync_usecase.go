package usecase

import (
	"context"
	"log"
	"strconv"
	"sync"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/repository/msapiclient"
)

type OrdersRepository interface {
	InsertOrders(ctx context.Context, orders []*domain.InternalOrder) error
}

type SyncUseCase struct {
	MSAPIClinet *msapiclient.MSAPIClient
	DBClient    OrdersRepository
	Converter   *msapiclient.MSConverter
	Config      *config.RefGoConfig
}

func NewSyncUsecase(client *msapiclient.MSAPIClient, repo OrdersRepository, converter *msapiclient.MSConverter, cfg *config.RefGoConfig) *SyncUseCase {
	return &SyncUseCase{
		MSAPIClinet: client,
		DBClient:    repo,
		Converter:   converter,
		Config:      cfg,
	}
}

func (uc *SyncUseCase) SyncDeliverableOrders(ctx context.Context) {
	refGoCounter := uc.Config.RGNextOrder

	orders, err := uc.MSAPIClinet.FetchDeliverableOrders(ctx)
	if err != nil {
		log.Printf("Failed to fetch deliverable orders: %v", err)

		return
	}

	suitableOrders := make([]*msapiclient.MSOrder, 0, len(orders)/2)
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
		wg.Add(1)

		go func(order *msapiclient.MSOrder, ctx context.Context) {
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

	err = uc.DBClient.InsertOrders(ctx, internalOrders)
	if err != nil {
		log.Printf("Failed to insert orders into database: %v", err)
	}

	err = uc.Config.ChangeRefGoLatest(refGoCounter)
	if err != nil {
		log.Printf("Failed to update RefGoLatest to %d: %v", refGoCounter, err)
	}
}

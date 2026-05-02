package usecase

import (
	"context"
	"log"
	"strconv"
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

	orders := uc.MSAPIClinet.FetchDeliverableOrders(ctx)

	suitableOrders := make([]*msapiclient.MSOrder, 0, len(orders)/2)
	for _, o := range orders {
		if o.SuitableForDelivery() {
			suitableOrders = append(suitableOrders, o)
		}
	}

	internalOrders := make([]*domain.InternalOrder, 0, len(suitableOrders))

	for _, o := range suitableOrders {
		internalOrder := uc.Converter.ToDomain(o)

		if internalOrder.GetRefGoNumber() == "" {
			err := uc.MSAPIClinet.SetRefGoNumberOnly(ctx, internalOrder.GetHREF(), strconv.Itoa(refGoCounter))
			if err != nil {
				log.Printf("Failed to set RefGoNumber for order %s: %v", internalOrder.GetName(), err)

				continue
			}

			internalOrder.SetRefGoNumber(strconv.Itoa(refGoCounter))
			log.Printf("Assigned RefGoNumber: %v to order: %s", refGoCounter, internalOrder.GetName())

			refGoCounter++
		}

		internalOrder.Validate()
		internalOrders = append(internalOrders, internalOrder)
	}

	err := uc.DBClient.InsertOrders(ctx, internalOrders)
	if err != nil {
		log.Printf("Failed to insert orders into database: %v", err)
	}

	err = uc.Config.ChangeRefGoLatest(refGoCounter)
	if err != nil {
		log.Printf("Failed to update RefGoLatest to %d: %v", refGoCounter, err)
	}
}

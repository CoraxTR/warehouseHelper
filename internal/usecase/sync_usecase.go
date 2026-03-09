package usecase

import (
	"context"
	"log"
	"strconv"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/repository/msapiclient"
)

type DB interface {
	InsertOrders(ctx context.Context, orders []*domain.InternalOrder) error
}

type SyncUseCase struct {
	MSAPIClinet *msapiclient.MoySkladAPIClient
	DBClient    DB
	Converter   *msapiclient.MoySkladConverter
	Config      *config.RefGoConfig
}

func NewSyncUsecase() {

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
		internalOrder.SetRefGoNumber(strconv.Itoa(refGoCounter))
		internalOrder.Validate()
		internalOrders = append(internalOrders, internalOrder)
		refGoCounter++
	}

	err := uc.DBClient.InsertOrders(ctx, internalOrders)
	if err != nil {
		log.Printf("Failed to insert orders into database: %v", err)
	}

	err = config.ChangeRefGoLatest(refGoCounter)
	if err != nil {
		log.Printf("Failed to update RefGoLatest to %d: %v", refGoCounter, err)
	}
}

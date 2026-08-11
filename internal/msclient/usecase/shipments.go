package usecase

import (
	"context"
	"fmt"
	"log"

	"warehouseHelper/internal/msclient/client"
)

// WarehouseNotifier — отправка уведомлений в чат склада.
type WarehouseNotifier interface {
	NotifyWarehouse(text string) error
}

// OrderShipmentClient — операции МойСклад, нужные для обеспечения отгрузки заказа.
// Реализуется *client.MSAPIClient.
type OrderShipmentClient interface {
	FetchOrderShipmentState(ctx context.Context, href string) (*client.MSOrderShipmentState, error)
	FetchOrderPositions(ctx context.Context, positionsHref string) ([]client.MSPosition, error)
	CreateDemand(ctx context.Context, orderHref, agentHref string, positions []client.MSPosition) error
}

// OrderShipmentEnsurer — гарантирует наличие корректной отгрузки у заказа.
// Если отгрузок нет — создаёт её из шаблона заказа; если сумма отгрузок
// не совпадает с суммой заказа — уведомляет склад (правка вручную).
type OrderShipmentEnsurer struct {
	client   OrderShipmentClient
	notifier WarehouseNotifier
}

func NewOrderShipmentEnsurer(client OrderShipmentClient, notifier WarehouseNotifier) *OrderShipmentEnsurer {
	return &OrderShipmentEnsurer{
		client:   client,
		notifier: notifier,
	}
}

// EnsureOrderShipment проверяет состояние отгрузки заказа по href и, при
// необходимости, создаёт отгрузку или уведомляет склад о несоответствии.
func (uc *OrderShipmentEnsurer) EnsureOrderShipment(ctx context.Context, href string) error {
	state, err := uc.client.FetchOrderShipmentState(ctx, href)
	if err != nil {
		return fmt.Errorf("failed to fetch shipment state: %w", err)
	}

	// Отгрузок нет — создаём новую из позиций заказа.
	if len(state.Demands) == 0 {
		return uc.createDemand(ctx, href, state)
	}

	// Отгрузка есть, но сумма не сходится с суммой заказа — на ручную правку.
	if state.ShippedSum != state.Sum {
		uc.notifyWarehouse(fmt.Sprintf("В заказе %s нужно поправить отгрузку", state.Name))
	}

	return nil
}

// createDemand создаёт отгрузку для заказа: позиции берутся из заказа
// (assortment + quantity + price), организация и склад — из конфига клиента.
func (uc *OrderShipmentEnsurer) createDemand(ctx context.Context, href string, state *client.MSOrderShipmentState) error {
	positions, err := uc.client.FetchOrderPositions(ctx, href+"/positions")
	if err != nil {
		uc.notifyCreateFailed(state.Name)

		return fmt.Errorf("failed to fetch order positions: %w", err)
	}

	if err := uc.client.CreateDemand(ctx, href, state.Agent.Meta.HREF, positions); err != nil {
		uc.notifyCreateFailed(state.Name)

		return fmt.Errorf("failed to create demand: %w", err)
	}

	return nil
}

func (uc *OrderShipmentEnsurer) notifyCreateFailed(orderName string) {
	uc.notifyWarehouse(fmt.Sprintf("Не удалось создать отгрузку в заказ: %s", orderName))
}

func (uc *OrderShipmentEnsurer) notifyWarehouse(text string) {
	if err := uc.notifier.NotifyWarehouse(text); err != nil {
		log.Printf("failed to notify warehouse: %v", err)
	}
}

package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"warehouseHelper/internal/msclient/client"
)

// WarehouseNotifier — отправка уведомлений в чат склада.
type WarehouseNotifier interface {
	NotifyWarehouse(text string) error
}

// OrderShipmentClient — операции МойСклад, нужные для обеспечения отгрузки.
// Реализуется *client.MSAPIClient.
type OrderShipmentClient interface {
	FetchOrderShipmentState(ctx context.Context, href string) (*client.MSOrderShipmentState, error)
	FetchDemandNewTemplate(ctx context.Context, href string) (json.RawMessage, error)
	CreateDemand(ctx context.Context, template json.RawMessage) error
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

	// Отгрузок нет — создаём новую из шаблона заказа.
	if len(state.Demands) == 0 {
		return uc.createDemand(ctx, href, state.Name)
	}

	// Отгрузка есть, но сумма не сходится с суммой заказа — на ручную правку.
	if state.ShippedSum != state.Sum {
		uc.notifyWarehouse(fmt.Sprintf("В заказе %s нужно поправить отгрузку", state.Name))
	}

	return nil
}

func (uc *OrderShipmentEnsurer) createDemand(ctx context.Context, href, orderName string) error {
	template, err := uc.client.FetchDemandNewTemplate(ctx, href)
	if err != nil {
		uc.notifyCreateFailed(orderName, err)

		return fmt.Errorf("failed to fetch demand template: %w", err)
	}

	if err := uc.client.CreateDemand(ctx, template); err != nil {
		uc.notifyCreateFailed(orderName, err)

		return fmt.Errorf("failed to create demand: %w", err)
	}

	return nil
}

// notifyCreateFailed шлёт в чат склада сообщение о неудаче создания отгрузки
// и, если МС вернул ошибку (errors[].error), добавляет её текст.
func (uc *OrderShipmentEnsurer) notifyCreateFailed(orderName string, cause error) {
	msg := fmt.Sprintf("Не удалось создать отгрузку в заказ: %s", orderName)

	var apiErr *client.MSAPIError
	if errors.As(cause, &apiErr) && len(apiErr.Errors) > 0 {
		msg += fmt.Sprintf(". Ошибка: %s", strings.Join(apiErr.Errors, "; "))
	}

	uc.notifyWarehouse(msg)
}

func (uc *OrderShipmentEnsurer) notifyWarehouse(text string) {
	if err := uc.notifier.NotifyWarehouse(text); err != nil {
		log.Printf("failed to notify warehouse: %v", err)
	}
}

package app

import (
	"context"
	"fmt"

	"warehouseHelper/internal/telegram"
)

// productNamer — имя товара каталога по id; реализует goods (ProductName).
type productNamer interface {
	ProductName(ctx context.Context, productID string) (string, error)
}

// stockStatusNotifier — уведомления о смене наличия в общий Telegram-канал
// (TG_COMMON_CHAT_ID): имя товара берёт из каталога, сообщение шлёт
// NotifyCommon. Реализует daystate.StockStatusNotifier (связка в di.go).
type stockStatusNotifier struct {
	notifier *telegram.Notifier
	namer    productNamer
}

// NewStockStatusNotifier собирает адаптер уведомлений о наличии.
func NewStockStatusNotifier(notifier *telegram.Notifier, namer productNamer) *stockStatusNotifier {
	return &stockStatusNotifier{notifier: notifier, namer: namer}
}

// SoldOut — «<имя> - товар закончился» в общий канал.
func (n *stockStatusNotifier) SoldOut(ctx context.Context, productID string) error {
	name, err := n.namer.ProductName(ctx, productID)
	if err != nil {
		return fmt.Errorf("имя товара %s: %w", productID, err)
	}
	if err := n.notifier.NotifyCommon(ctx, name+" - товар закончился"); err != nil {
		return fmt.Errorf("уведомление о закончившемся товаре: %w", err)
	}
	return nil
}

// BackInStock — «<имя> - товар появился» в общий канал.
func (n *stockStatusNotifier) BackInStock(ctx context.Context, productID string) error {
	name, err := n.namer.ProductName(ctx, productID)
	if err != nil {
		return fmt.Errorf("имя товара %s: %w", productID, err)
	}
	if err := n.notifier.NotifyCommon(ctx, name+" - товар появился"); err != nil {
		return fmt.Errorf("уведомление о появившемся товаре: %w", err)
	}
	return nil
}

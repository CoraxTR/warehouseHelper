package app

import (
	"context"
	"fmt"

	"warehouseHelper/internal/domain"
)

// productNamer — имя товара каталога по id; реализует goods (ProductName).
type productNamer interface {
	ProductName(ctx context.Context, productID string) (string, error)
}

// taskOpener — шов модуля «Внутренние задачи» (реализация — tasks/usecase):
// уведомление общего канала становится задачей ленты с одноразовой кнопкой
// отметки «кто выполнил».
type taskOpener interface {
	Open(ctx context.Context, kind domain.TaskKind, text string) error
}

// stockStatusNotifier — уведомления о смене наличия в общий Telegram-канал
// (TG_COMMON_CHAT_ID): имя товара берёт из каталога, уведомление открывает
// задачей модуля «Внутренние задачи». Реализует daystate.StockStatusNotifier
// (связка в di.go).
type stockStatusNotifier struct {
	tasks taskOpener
	namer productNamer
}

// NewStockStatusNotifier собирает адаптер уведомлений о наличии.
func NewStockStatusNotifier(tasks taskOpener, namer productNamer) *stockStatusNotifier {
	return &stockStatusNotifier{tasks: tasks, namer: namer}
}

// SoldOut — «Убрать с сайта: <имя>» в общий канал: товар закончился,
// позицию надо снять с сайта (решение владельца 25.09.2026).
func (n *stockStatusNotifier) SoldOut(ctx context.Context, productID string) error {
	name, err := n.namer.ProductName(ctx, productID)
	if err != nil {
		return fmt.Errorf("имя товара %s: %w", productID, err)
	}
	if err := n.tasks.Open(ctx, domain.TaskKindStockOut, "Убрать с сайта: "+name); err != nil {
		return fmt.Errorf("задача «убрать с сайта»: %w", err)
	}
	return nil
}

// BackInStock — «Вернуть на сайт: <имя>» в общий канал: товар появился,
// позицию надо вернуть на сайт (решение владельца 25.09.2026; прежнее
// «товар появился» читалось как возврат на сайт — см. daystate-stock-notifications).
func (n *stockStatusNotifier) BackInStock(ctx context.Context, productID string) error {
	name, err := n.namer.ProductName(ctx, productID)
	if err != nil {
		return fmt.Errorf("имя товара %s: %w", productID, err)
	}
	if err := n.tasks.Open(ctx, domain.TaskKindStockIn, "Вернуть на сайт: "+name); err != nil {
		return fmt.Errorf("задача «вернуть на сайт»: %w", err)
	}
	return nil
}

package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/msorders"
)

// PickingJournal — шов журнала подбора (таблица order_picking): модуль подбора
// владеет и таблицей, и её чтением. Реализует *postgres.PGClient (методы с
// теми же именами и типами домена msorders), связка — app/di.go.
//
// Журнал хранит ФИНАЛЬНУЮ версию подбора: строка = подобранная единица (кусок
// или штука) с датами выработки и срока годности. Переподбор заменяет строки
// позиции, добор дозаписывает, возврат в «Сроки» убирает, расформирование
// заказа чистит целиком.
type PickingJournal interface {
	// ReplaceOrderPicking — замена журнала по позициям заказа (строки позиций
	// удаляются, вместо них пишутся единицы подбора).
	ReplaceOrderPicking(ctx context.Context, w msorders.PickingReplace) error
	// AppendOrderPicking — дозапись единиц (добор строки: часть единиц строки
	// подобрана раньше и уже лежит в журнале).
	AppendOrderPicking(ctx context.Context, units []msorders.PickingUnit) error
	// RemoveOrderPickingUnits — убрать вернувшиеся в «Сроки» единицы
	// (совпадение по коду, сроку и весу; единиц может не найтись).
	RemoveOrderPickingUnits(ctx context.Context, r msorders.PickingReturn) error
	// ClearOrderPicking — очистка журнала заказа: positionIDs пусто — весь
	// заказ (отмена/расформирование), иначе только перечисленные позиции
	// (позиция удалена из заказа, ручное закрытие строки).
	ClearOrderPicking(ctx context.Context, orderID string, positionIDs []string) error
	// ClearOrderPickingProducts — очистка журнала по товарам заказа (позиции
	// удалены из заказа, известны только uuid товаров из диффа аудита МС).
	ClearOrderPickingProducts(ctx context.Context, orderID string, productIDs []string) error
	// OrderPickingByOrder — строки журнала заказа (для ответа на /sroki).
	OrderPickingByOrder(ctx context.Context, orderID string) ([]msorders.PickingUnit, error)
	// CleanupOrderPicking — удалить строки старше olderThan, вернуть число
	// удалённых (ретеншен журнала: номер заказа повторяется каждый год).
	CleanupOrderPicking(ctx context.Context, olderThan time.Time) (int64, error)
}

// ChatSender — шов отправки текста в произвольный чат Telegram (ответ на
// команду в чат отправителя, как /discounts у модуля скидок). Реализует
// *telegram.Notifier (метод SendDetails), связка — app/di.go.
type ChatSender interface {
	SendDetails(ctx context.Context, chatID int64, text string) error
}

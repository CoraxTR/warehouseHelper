// Пакет usecase — наблюдатель резервов заказов (модуль reservewatch).
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/reservewatch"
)

// Швы модуля — интерфейсы на стороне потребителя; реализации связывает di.go:
// orders — *client.MSAPIClient, repo/catalog — *repository/postgres.PGClient,
// notify — *telegram.Notifier.

// Orders — живой МойСклад: лист заказов окна (статусы + плановая отгрузка)
// и позиции заказа с expand=assortment (имя товара в строке).
type Orders interface {
	FetchReserveWatchOrders(ctx context.Context, windowDays int, stateIDs []string) ([]client.MSOrder, error)
	FetchOrderPositions(ctx context.Context, orderID string) ([]client.MSPosition, error)
}

// Repo — активные уведомления модуля (таблица reserve_notices).
type Repo interface {
	ListActiveReserveNotices(ctx context.Context) ([]reservewatch.Notice, error)
	InsertReserveNotice(ctx context.Context, n reservewatch.Notice) (bool, error)
	DeleteReserveNotice(ctx context.Context, orderID string, kind reservewatch.Kind) error
}

// Catalog — каталог товаров (чтение products): код склада и тип учёта
// по uuid товара МС (как в returns: внутренний код = «складской товар»).
type Catalog interface {
	ReserveWatchProductsByMSIDs(ctx context.Context, ids []string) (map[string]reservewatch.CatalogProduct, error)
}

// Notifier — уведомления в чат склада (Telegram).
type Notifier interface {
	SendWarehouseButton(ctx context.Context, text, buttonText, buttonURL string) (chatID, messageID int64, err error)
	DeleteWarehouseMessage(ctx context.Context, messageID int64) error
}

// Config — параметры модуля (собираются в di.go из конфига приложения).
type Config struct {
	StateIDs     []string      // id статусов заказов окна (metadata/states)
	PublicURL    string        // адрес приложения: база URL-кнопки «Подобрать»
	WindowDays   int           // окно по плановой отгрузке: от начала суток N дней назад (0 → 7)
	PollInterval time.Duration // период проверки (0 → минута)
}

type UseCase struct {
	cfg     Config
	orders  Orders
	repo    Repo
	catalog Catalog
	notify  Notifier
}

func NewUseCase(cfg Config, orders Orders, repo Repo, catalog Catalog, notify Notifier) *UseCase {
	if cfg.WindowDays <= 0 {
		cfg.WindowDays = 7
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	return &UseCase{cfg: cfg, orders: orders, repo: repo, catalog: catalog, notify: notify}
}

// Run — наблюдатель резервов. Каждый тик: лист заказов окна, сверка резерва
// позиций с quantity, уведомления о проблемах в чат склада (по одному на
// проблему: повторно — только после исчезновения и возврата), удаление
// сообщений выздоровевших заказов. Тик синхронен: следующий сработает после
// завершения текущего (окно пагинацией любого размера). Ошибка тика не
// роняет поллер — логируется, следующий тик повторит (дедуп по PK гасит
// повторы).
func (uc *UseCase) Run(ctx context.Context) error {
	if len(uc.cfg.StateIDs) == 0 {
		slog.Warn("reservewatch: статусы не заданы — модуль не запущен")
		return nil
	}
	slog.Info("reservewatch: наблюдатель резервов запущен",
		"interval", uc.cfg.PollInterval.String(), "window_days", uc.cfg.WindowDays, "states", len(uc.cfg.StateIDs))

	ticker := time.NewTicker(uc.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := uc.tick(ctx); err != nil && ctx.Err() == nil {
			slog.Error("reservewatch tick failed", "err", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// tick — один проход наблюдателя: проблемы резервов текущего окна сверяются
// с активными уведомлениями; новые отправляются, исчезнувшие закрываются.
func (uc *UseCase) tick(ctx context.Context) error {
	orders, err := uc.orders.FetchReserveWatchOrders(ctx, uc.cfg.WindowDays, uc.cfg.StateIDs)
	if err != nil {
		return fmt.Errorf("reservewatch fetch orders: %w", err)
	}
	slog.Info("reservewatch: окно заказов", "orders", len(orders))

	problems, err := uc.collectProblems(ctx, orders)
	if err != nil {
		return err
	}

	active, err := uc.repo.ListActiveReserveNotices(ctx)
	if err != nil {
		return fmt.Errorf("reservewatch list active: %w", err)
	}

	// Живые уведомления: те, чья проблема снова в окне, остаются (молчим);
	// остальные — проблема исчезла — удаляем из чата и из таблицы.
	activeKey := make(map[string]reservewatch.Notice, len(active))
	for _, n := range active {
		activeKey[noticeKey(n.OrderID, n.Kind)] = n
	}

	for _, order := range orders {
		for _, kind := range []reservewatch.Kind{reservewatch.KindMissing, reservewatch.KindWrong} {
			items, ok := problems[order.ID][kind]
			if !ok {
				continue
			}

			key := noticeKey(order.ID, kind)
			if _, ok := activeKey[key]; ok {
				delete(activeKey, key) // уже уведомлены, проблема та же — молчим
				continue
			}

			if err := uc.sendProblem(ctx, order.ID, order.Name, kind, items); err != nil {
				slog.Error("reservewatch send problem", "order", order.Name, "kind", kind, "err", err)
			}
		}
	}

	for key, n := range activeKey {
		orderID, kind := splitNoticeKey(key)
		if err := uc.closeProblem(ctx, orderID, kind, n); err != nil {
			slog.Error("reservewatch close problem", "order", orderID, "kind", kind, "err", err)
		}
	}

	return nil
}

// collectProblems — проблемы резерва заказов окна: для каждого заказа позиции
// (товар каталога с кодом склада; весовые сверяются в граммах) классифицируются
// по виду проблемы. Каталог читается одним батчем на все заказы тика.
func (uc *UseCase) collectProblems(ctx context.Context, orders []client.MSOrder) (map[string]map[reservewatch.Kind][]problemItem, error) {
	problems := make(map[string]map[reservewatch.Kind][]problemItem, len(orders))

	// Один батч каталога на тик: uuid товаров всех позиций всех заказов.
	productIDs := make([]string, 0)
	seen := make(map[string]struct{})
	type positionRef struct {
		orderID   string
		productID string
		pos       client.MSPosition
	}
	var refs []positionRef

	for _, order := range orders {
		positions, err := uc.orders.FetchOrderPositions(ctx, order.ID)
		if err != nil {
			return nil, fmt.Errorf("reservewatch fetch positions %s: %w", order.Name, err)
		}

		for _, p := range positions {
			id := lastPathSegment(p.Assortment.Meta.HREF)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				productIDs = append(productIDs, id)
			}
			refs = append(refs, positionRef{orderID: order.ID, productID: id, pos: p})
		}
	}

	catalog, err := uc.catalog.ReserveWatchProductsByMSIDs(ctx, productIDs)
	if err != nil {
		return nil, fmt.Errorf("reservewatch catalog: %w", err)
	}

	for _, ref := range refs {
		p, ok := catalog[ref.productID]
		if !ok || p.InternalCode == "" {
			continue // не складской товар (нет в каталоге / без кода склада)
		}

		unit := qtyPieces
		if p.Weighted {
			unit = qtyGrams
		}

		q := qtyInt(ref.pos.Quantity, unit)
		r := qtyInt(ref.pos.Reserve, unit)
		if q == 0 {
			continue // пустая строка (qty=0) проблемы не создаёт
		}

		var kind reservewatch.Kind
		switch {
		case r == 0:
			kind = reservewatch.KindMissing
		case q != r:
			kind = reservewatch.KindWrong
		default:
			continue // отложено ровно — здорово
		}

		item := problemItem{
			name:     ref.pos.Assortment.Name,
			quantity: q,
			reserve:  r,
			unit:     unit,
		}
		if problems[ref.orderID] == nil {
			problems[ref.orderID] = make(map[reservewatch.Kind][]problemItem)
		}
		problems[ref.orderID][kind] = append(problems[ref.orderID][kind], item)
	}

	return problems, nil
}

// problemItem — позиция с проблемой резерва (для текста сообщения).
// quantity/reserve — в единицах сверки unit (граммы для весовых, штуки иначе).
type problemItem struct {
	name     string
	quantity int64
	reserve  int64
	unit     qtyUnit
}

// qtyUnit — единица сверки количества: весовой товар сводится в граммы
// (кг позиции ×1000 — вес этикетки в граммах), штучный — в единицы.
type qtyUnit uint8

const (
	qtyGrams  qtyUnit = iota // весовой: сравнение/накопление в граммах
	qtyPieces                // штучный: по количеству единиц
)

func qtyInt(v float64, u qtyUnit) int64 {
	if u == qtyGrams {
		return int64(math.Round(v * 1000))
	}
	return int64(math.Round(v))
}

// sendProblem — уведомление о проблеме в чат склада с URL-кнопкой
// «Подобрать» (страница подбора заказа /ms/orders/{id}). Запись вставляется
// только после успешной отправки; уведомления не настроены (нет чата) —
// сообщение не отправляется, запись не создаётся (тик повторяет попытку
// дёшево — no-op).
func (uc *UseCase) sendProblem(ctx context.Context, orderID, orderName string, kind reservewatch.Kind, items []problemItem) error {
	text := uc.messageText(orderName, kind, items)
	chatID, messageID, err := uc.notify.SendWarehouseButton(ctx, text, "Подобрать", uc.pickURL(orderID))
	if err != nil {
		return fmt.Errorf("reservewatch send: %w", err)
	}
	if chatID == 0 || messageID == 0 {
		return nil // уведомления не настроены
	}

	inserted, err := uc.repo.InsertReserveNotice(ctx, reservewatch.Notice{OrderID: orderID, Kind: kind, MessageID: messageID})
	if err != nil {
		return fmt.Errorf("reservewatch insert notice %s: %w", orderName, err)
	}
	if !inserted {
		// Дубль на границе тиков — другой тик уже уведомил: убираем лишнее.
		if delErr := uc.notify.DeleteWarehouseMessage(ctx, messageID); delErr != nil {
			slog.Error("reservewatch cleanup duplicate message", "err", delErr)
		}
		return nil
	}

	slog.Info("reservewatch: уведомление отправлено",
		"order", orderName, "kind", kind, "items", len(items), "message", messageID)
	return nil
}

// closeProblem — проблема исчезла (позиции исправлены или заказ вышел из
// окна): убираем сообщение из чата склада и строку reserve_notices.
func (uc *UseCase) closeProblem(ctx context.Context, orderID string, kind reservewatch.Kind, n reservewatch.Notice) error {
	if err := uc.notify.DeleteWarehouseMessage(ctx, n.MessageID); err != nil {
		return fmt.Errorf("reservewatch delete message: %w", err)
	}
	if err := uc.repo.DeleteReserveNotice(ctx, orderID, kind); err != nil {
		return fmt.Errorf("reservewatch delete notice: %w", err)
	}
	slog.Info("reservewatch: проблема закрыта", "order", orderID, "kind", kind)
	return nil
}

// messageText — текст уведомления: вид проблемы + перечень позиций с
// количествами (весовые — кг с 3 знаками, штучные — в штуках).
func (uc *UseCase) messageText(orderName string, kind reservewatch.Kind, items []problemItem) string {
	var sb strings.Builder
	switch kind {
	case reservewatch.KindMissing:
		fmt.Fprintf(&sb, "В заказ %s нужно отложить:", orderName)
	case reservewatch.KindWrong:
		fmt.Fprintf(&sb, "В заказе %s позиции отложены неверно:", orderName)
	default:
		fmt.Fprintf(&sb, "Заказ %s: проблема резерва:", orderName)
	}

	for _, it := range items {
		sb.WriteString("\n— ")
		sb.WriteString(it.name)
		sb.WriteString(" — ")
		switch kind {
		case reservewatch.KindMissing:
			sb.WriteString(formatQty(it.quantity, it.unit))
		case reservewatch.KindWrong:
			sb.WriteString("нужно ")
			sb.WriteString(formatQty(it.quantity, it.unit))
			sb.WriteString(", отложено ")
			sb.WriteString(formatQty(it.reserve, it.unit))
		default:
			sb.WriteString(formatQty(it.quantity, it.unit))
		}
	}
	return sb.String()
}

// formatQty — количество для текста уведомления: весовой — «0.657 кг»
// (3 знака), штучный — «2 шт».
func formatQty(v int64, unit qtyUnit) string {
	if unit == qtyGrams {
		return fmt.Sprintf("%.3f кг", float64(v)/1000)
	}
	return fmt.Sprintf("%d шт", v)
}

// pickURL — адрес страницы подбора заказа (детальная страница с id заказа)
// для URL-кнопки «Подобрать».
func (uc *UseCase) pickURL(orderID string) string {
	base := uc.cfg.PublicURL
	for base != "" && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + "/ms/orders/" + orderID
}

// noticeKey — ключ активного уведомления (пара order_id + kind).
func noticeKey(orderID string, kind reservewatch.Kind) string {
	return orderID + "\x00" + string(kind)
}

func splitNoticeKey(key string) (string, reservewatch.Kind) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 2 {
		return key, ""
	}
	return parts[0], reservewatch.Kind(parts[1])
}

// lastPathSegment — последний сегмент href (id сущности).
func lastPathSegment(href string) string {
	if href == "" {
		return ""
	}
	trimmed := strings.TrimRight(href, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

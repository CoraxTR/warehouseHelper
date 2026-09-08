package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/returns"
	"warehouseHelper/internal/stock"
)

// Швы модуля — интерфейсы на стороне потребителя; реализации связывает di.go:
// repo — *postgres.PGClient, audit — *client.MSAPIClient, catalog — PGClient
// (чтение products), stock — *stockusecase.StockUseCase, notify — *telegram.Notifier.

// AuditAPI — журнал действий МойСклад.
type AuditAPI interface {
	FetchAuditPage(ctx context.Context, since time.Time, offset int) ([]client.AuditRow, int, error)
	FetchAuditDetail(ctx context.Context, auditID string) ([]client.AuditEventRow, error)
	FetchOrderPositions(ctx context.Context, orderID string) ([]client.MSPosition, error)
}

// Repo — таблицы модуля return_events / return_cursor.
type Repo interface {
	GetCursor(ctx context.Context) (time.Time, bool, error)
	SetCursor(ctx context.Context, moment time.Time) error
	InsertEvent(ctx context.Context, ev *returns.ReturnEvent) (bool, error)
	MarkSent(ctx context.Context, id string, chatID, messageID int64) error
	MarkDone(ctx context.Context, id string, manual bool) error
	GetEvent(ctx context.Context, id string) (*returns.ReturnEvent, error)
	ListActive(ctx context.Context) ([]returns.ReturnEvent, error)
}

// Catalog — каталог товаров (чтение): код склада и тип учёта по uuid МС.
type Catalog interface {
	ProductsByMSIDs(ctx context.Context, ids []string) (map[string]returns.CatalogProduct, error)
}

// Stock — возврат в остатки (шов stock): подтверждение приёма = nil-ошибка.
type Stock interface {
	AcceptStock(ctx context.Context, lots []stock.LotIn) error
}

// Notifier — уведомления в чат склада (Telegram).
type Notifier interface {
	SendWarehouseReturn(ctx context.Context, text, buttonURL string) (chatID, messageID int64, err error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
}

// Config — параметры модуля (собираются в di.go из конфига приложения).
type Config struct {
	CancelledStateID string        // id статуса «Отменён»; пусто — отмена не детектится (warn при старте)
	SkipSources      []string      // source-источники событий audit, которые поллер пропускает (наши API-правки)
	PublicURL        string        // адрес приложения: база URL-кнопки «Расформировать»
	PollInterval     time.Duration // период опроса аудита (0 → минута)
}

// tickBudget — бюджет одного тика: при большом накоплении событий (после сна
// машины) хвост догоняется следующими тиками, курсор двигается постранично.
const tickBudget = 40 * time.Second

type UseCase struct {
	cfg     Config
	audit   AuditAPI
	repo    Repo
	catalog Catalog
	stock   Stock
	notify  Notifier
}

func NewUseCase(cfg Config, audit AuditAPI, repo Repo, catalog Catalog, stock Stock, notify Notifier) *UseCase {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	return &UseCase{cfg: cfg, audit: audit, repo: repo, catalog: catalog, stock: stock, notify: notify}
}

// Run — поллер аудита. Каждый тик: опрос журнала с курсора (return_cursor),
// обработка customerorder-событий, повторная отправка зависших new-событий.
// Ошибка тика не роняет поллер (логируется, следующий тик повторит с того же
// курсора — потерянных событий нет, дедуп по id гасит повторы).
func (uc *UseCase) Run(ctx context.Context) error {
	if uc.cfg.CancelledStateID == "" {
		slog.Warn("returns: MSAPI_CANCELLED_STATE_ID не задан — переводы заказов в «Отменён» не отслеживаются")
	}

	ticker := time.NewTicker(uc.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := uc.tick(ctx); err != nil && ctx.Err() == nil {
			slog.Error("returns poll tick failed", "err", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// tick — один проход поллера. Курсор двигается только после полной обработки
// страницы (на момент последней строки) — оборванный тик повторяет страницу,
// уже обработанные события отсеиваются дедупом по id.
func (uc *UseCase) tick(ctx context.Context) error {
	cursor, ok, err := uc.repo.GetCursor(ctx)
	if err != nil {
		return fmt.Errorf("returns cursor: %w", err)
	}
	if !ok {
		// Первый запуск: смотрим только будущее (момент = now), прошлое не сканируем.
		return uc.repo.SetCursor(ctx, time.Now().UTC())
	}

	deadline := time.Now().Add(tickBudget)
	offset := 0
	page := 0

	for !time.Now().After(deadline) {
		rows, size, err := uc.audit.FetchAuditPage(ctx, cursor, offset)
		if err != nil {
			return fmt.Errorf("returns fetch audit page: %w", err)
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			if err := uc.processRow(ctx, row); err != nil {
				return err // курсор не двигаем — страница повторится, дедуп защитит
			}
		}

		last, err := client.ParseAuditMoment(rows[len(rows)-1].Moment)
		if err != nil {
			return fmt.Errorf("returns parse audit moment %q: %w", rows[len(rows)-1].Moment, err)
		}
		if err := uc.repo.SetCursor(ctx, last); err != nil {
			return fmt.Errorf("returns save cursor: %w", err)
		}

		offset += len(rows)
		page++
		if offset >= size || page >= 100 {
			break
		}
	}

	return uc.retryNew(ctx)
}

// processRow — одна строка листа аудита: отбор customerorder, пропуск наших
// API-источников, раскрытие events, определение вида события, отправка
// уведомления. Уже обработанные id (дедуп) пропускаются.
func (uc *UseCase) processRow(ctx context.Context, row client.AuditRow) error {
	if row.EntityType != "customerorder" || row.EventType != "update" {
		return nil
	}
	if uc.skippedSource(row.Source) {
		return nil // наши собственные изменения (source=remap-1.2)
	}

	if _, err := uc.repo.GetEvent(ctx, row.ID); err == nil {
		return nil // уже отслеживается
	} else if !errors.Is(err, returns.ErrEventNotFound) {
		return fmt.Errorf("returns get event %s: %w", row.ID, err)
	}

	moment, err := client.ParseAuditMoment(row.Moment)
	if err != nil {
		return fmt.Errorf("returns parse audit moment %q: %w", row.Moment, err)
	}

	detail, err := uc.audit.FetchAuditDetail(ctx, row.ID)
	if err != nil {
		return fmt.Errorf("returns fetch audit detail %s: %w", row.ID, err)
	}

	out := parseDetail(detail, uc.cfg.CancelledStateID)

	var kind returns.EventKind
	var orderID, orderName string
	for _, d := range detail {
		if d.Name != "" {
			orderName = d.Name
		}
		if href := d.Entity.Meta.HREF; href != "" {
			orderID = lastPathSegment(href)
		}
	}

	switch {
	case len(out.removals) > 0:
		kind = returns.KindRemoved // приоритет: удаление побеждает отмену в том же событии
	case out.cancelled:
		kind = returns.KindCancelled
	default:
		return nil // нет целевого паттерна (правка полей/резервов и т.п.)
	}

	ev := &returns.ReturnEvent{
		ID:        row.ID,
		Kind:      kind,
		OrderID:   orderID,
		OrderName: orderName,
		Moment:    moment,
		Status:    returns.StatusNew,
	}

	// Отложенные позиции с internal_code есть? (пустая отмена / удаление без
	// резерва — уведомлять нечего). Одно раскрытие МС на событие: expected
	// уходят в sendEventMessage (текст сообщения строится из них).
	expected, err := uc.buildExpected(ctx, ev)
	if err != nil {
		if errors.Is(err, returns.ErrNothingToReturn) {
			return nil
		}
		return err
	}

	inserted, err := uc.repo.InsertEvent(ctx, ev)
	if err != nil {
		return fmt.Errorf("returns insert event %s: %w", row.ID, err)
	}
	if !inserted {
		return nil // дубль на границе окна — другой тик уже обработал
	}

	if err := uc.sendEventMessage(ctx, ev, expected); err != nil {
		// Событие остаётся в статусе new — retryNew дослает в следующих тиках.
		slog.Error("returns send notification failed", "event", row.ID, "err", err)
	}
	return nil
}

// sendEventMessage — текст уведомления (по ожиданиям события) и отправка
// в чат склада с URL-кнопкой «Расформировать».
func (uc *UseCase) sendEventMessage(ctx context.Context, ev *returns.ReturnEvent, expected []returns.Expected) error {
	chatID, messageID, err := uc.notify.SendWarehouseReturn(ctx,
		uc.messageText(ev, expected), uc.returnURL(ev.ID))
	if err != nil {
		return err
	}
	if chatID == 0 || messageID == 0 {
		return nil // уведомления не настроены (нет чата склада) — событие видно на странице
	}

	if err := uc.repo.MarkSent(ctx, ev.ID, chatID, messageID); err != nil {
		return fmt.Errorf("returns mark sent %s: %w", ev.ID, err)
	}
	return nil
}

// retryNew — повторная отправка зависших уведомлений (событие вставлено,
// но сообщение не ушло: падение между InsertEvent и MarkSent, сбой сети,
// TG был недоступен). Вызывается в конце каждого тика.
func (uc *UseCase) retryNew(ctx context.Context) error {
	active, err := uc.repo.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("returns list active: %w", err)
	}

	for i := range active {
		ev := active[i]
		if ev.Status != returns.StatusNew {
			continue
		}

		expected, err := uc.buildExpected(ctx, &ev)
		if err != nil {
			if errors.Is(err, returns.ErrNothingToReturn) {
				// Событие «опустело» (заказ изменился после создания) — закрываем.
				if doneErr := uc.repo.MarkDone(ctx, ev.ID, false); doneErr != nil {
					slog.Error("returns close emptied event", "event", ev.ID, "err", doneErr)
				}
				continue
			}
			slog.Error("returns retry build expected", "event", ev.ID, "err", err)
			continue
		}

		if err := uc.sendEventMessage(ctx, &ev, expected); err != nil {
			slog.Error("returns retry notification", "event", ev.ID, "err", err)
		}
	}
	return nil
}

// messageText — текст уведомления в чат склада.
func (uc *UseCase) messageText(ev *returns.ReturnEvent, expected []returns.Expected) string {
	if ev.Kind == returns.KindCancelled {
		return fmt.Sprintf("Заказ %s был переведён в статус «Отменён»", ev.OrderName)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Из заказа %s удалили:", ev.OrderName)
	for _, e := range expected {
		sb.WriteString("\n— ")
		sb.WriteString(e.Name)
		sb.WriteByte(' ')
		sb.WriteString(formatQty(e))
	}
	return sb.String()
}

// formatQty — количество строки ожидания для текста/страницы:
// весовой — «0.657 кг» (3 знака), штучный — «2 шт».
func formatQty(e returns.Expected) string {
	if e.Weighted {
		return fmt.Sprintf("%.3f кг", float64(e.ExpectedQty)/1000)
	}
	return fmt.Sprintf("%d шт", e.ExpectedQty)
}

// returnURL — адрес страницы «Возврат в продажу» для URL-кнопки.
func (uc *UseCase) returnURL(eventID string) string {
	base := uc.cfg.PublicURL
	for base != "" && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + "/goods/return?e=" + eventID
}

// skippedSource — событие от нашего API (source из списка пропуска)?
// nil (старые события без source) не пропускается.
func (uc *UseCase) skippedSource(source *string) bool {
	if source == nil {
		return false
	}
	return slices.Contains(uc.cfg.SkipSources, *source)
}

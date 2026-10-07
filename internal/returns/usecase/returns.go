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

// Catalog — каталог товаров (чтение): код склада и тип учёта по uuid МС
// (состав возврата события) или по internal_code (ручной возврат по сканам).
type Catalog interface {
	ProductsByMSIDs(ctx context.Context, ids []string) (map[string]returns.CatalogProduct, error)
	ProductsByInternalCodes(ctx context.Context, codes []string) (map[string]returns.CatalogProduct, error)
}

// Orders — живой заказ МС: текущий статус (сверка «всё ещё отменён» при
// открытии расформирования) и снятие резерва (reserve → 0) при приёме возврата.
// Реализация — *client.MSAPIClient: чтение SubmitOther, правка SubmitWarehouse.
type Orders interface {
	FetchOrderState(ctx context.Context, orderID string) (string, error)
	ClearOrderReserves(ctx context.Context, orderID string) error
}

// Stock — остатки (шов stock): ручной возврат кусков принимает (AcceptStock),
// вывод из продажи — списывает куски из сроков (PickStock). Списание PickStock
// заодно дёргает шов «лоты товара изменились» (stock.LotChangeListener) —
// пересчёт скидок модуля discounts идёт следом, отдельной механики не нужно.
type Stock interface {
	AcceptStock(ctx context.Context, lots []stock.LotIn) error
	PickStock(ctx context.Context, lots []stock.PickLotIn) error
}

// Notifier — уведомления в чат склада (Telegram).
type Notifier interface {
	SendWarehouseReturn(ctx context.Context, text, buttonURL string) (chatID, messageID int64, err error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	// NotifyWarehouse — обычный текст в чат склада (уведомление о пересчёте
	// сроков при ручном закрытии возврата).
	NotifyWarehouse(text string) error
}

// ShelfLife — шов журнала подбора заказов (модуль msorders): при
// расформировании даты подобранных единиц снимаются — заказ отменён целиком
// (productIDs пусто) или из заказа убраны позиции (передаются uuid товаров
// удалённых позиций: id самих позиций в событии аудита нет). Реализует
// *msorders.UseCase (см. app/di.go).
type ShelfLife interface {
	ClearShelfLife(ctx context.Context, orderID string, productIDs []string) error
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

// noScansReason — отказ страниц, собирающих операцию из сканов («Создать
// коробку», «Ручной возврат», «Вывод из продажи»): пустой батч — нечего делать.
const noScansReason = "нет сканов"

type UseCase struct {
	cfg     Config
	audit   AuditAPI
	repo    Repo
	catalog Catalog
	stock   Stock
	notify  Notifier
	orders  Orders
	// shelfLife — шов журнала подбора (модуль msorders): расформирование
	// снимает даты подобранных единиц. Ставится сеттером при сборке (di.go);
	// без шва расформирование работает как раньше.
	shelfLife ShelfLife
}

func NewUseCase(cfg Config, audit AuditAPI, repo Repo, catalog Catalog, stock Stock, notify Notifier, orders Orders) *UseCase {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	return &UseCase{cfg: cfg, audit: audit, repo: repo, catalog: catalog, stock: stock, notify: notify, orders: orders}
}

// SetShelfLife — шов журнала подбора заказов (таблица order_picking модуля
// msorders). Ставится при сборке (di.go) до запуска поллера и обработки
// страницы возврата.
func (uc *UseCase) SetShelfLife(s ShelfLife) {
	uc.shelfLife = s
}

// Run — поллер аудита. Каждый тик: опрос журнала с курсора (return_cursor),
// обработка customerorder-событий, повторная отправка зависших new-событий.
// Ошибка тика не роняет поллер (логируется, следующий тик повторит с того же
// курсора — потерянных событий нет, дедуп по id гасит повторы).
func (uc *UseCase) Run(ctx context.Context) error {
	if uc.cfg.CancelledStateID == "" {
		slog.Warn("returns: MSAPI_CANCELLED_STATE_ID не задан — переводы заказов в «Отменён» не отслеживаются")
	}
	slog.Info("returns: наблюдатель аудита запущен", "interval", uc.cfg.PollInterval.String())

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
	// Лист audit МС сортируется по убыванию (свежие сверху — проверено на
	// живом API): окно [курсор..now] листается offset-ами с НЕИЗМЕННЫМ since.
	// Курсор двигается ТОЛЬКО когда окно долистано до конца (край = момент
	// последней обработанной строки). Если двигать курсор после каждой
	// страницы, при DESC-сортировке он «перепрыгнет» вперёд и всё, что глубже
	// первой страницы, потеряется навсегда (баг, пойманный на проде 09.09).
	// Оборванный по бюджету тик повторяет окно с того же since — уже
	// обработанные события отсеиваются дедупом по id.
	since := cursor
	offset := 0
	var edge time.Time // момент последней строки окна (край)
	done := false

	for !time.Now().After(deadline) {
		rows, size, err := uc.audit.FetchAuditPage(ctx, since, offset)
		if err != nil {
			return fmt.Errorf("returns fetch audit page: %w", err)
		}
		if len(rows) == 0 {
			done = true // окно пусто (size == 0)
			break
		}

		for _, row := range rows {
			if err := uc.processRow(ctx, row); err != nil {
				return err // курсор не двигаем — окно повторится, дедуп защитит
			}
		}

		edge, err = client.ParseAuditMoment(rows[len(rows)-1].Moment)
		if err != nil {
			return fmt.Errorf("returns parse audit moment %q: %w", rows[len(rows)-1].Moment, err)
		}

		offset += len(rows)
		if offset >= size {
			done = true
			break
		}
	}

	// Окно долистано: курсор = край окна + 1 миллисекунда. Фильтр листа несёт
	// миллисекунды (auditMomentLayout; проверено на живом API: событие с
	// моментом .102 отсеивается фильтром >=.500), поэтому +1мс строго
	// выталкивает краевое событие (момент == курсор) из следующего окна — без
	// него оно перечитывалось бы каждый тик (баг прода 09.09: одни и те же
	// события в логе каждую минуту). События той же секунды, но новее края,
	// не теряются (их момент >= курсор).
	// Не долистали (бюджет) — курсор не трогаем: следующий тик продолжит
	// с того же since.
	if done && !edge.IsZero() {
		if err := uc.repo.SetCursor(ctx, edge.Add(time.Millisecond)); err != nil {
			return fmt.Errorf("returns save cursor: %w", err)
		}
	}

	return uc.retryNew(ctx)
}

// eventKind — вид события для записи и уведомления; ok=false — сообщать нечего.
// Отмена — свой вид (вес к ней отношения не имеет), дифф: возврат в продажу
// приоритетнее веса, чистое уменьшение веса — manual_weight_down.
func eventKind(kind returns.EventKind, expected, drops int) (returns.EventKind, bool) {
	switch {
	case kind == returns.KindCancelled:
		return returns.KindCancelled, expected > 0
	case expected > 0:
		return returns.KindRemoved, true
	case drops > 0:
		return returns.KindManualWeightDown, true
	default:
		return "", false
	}
}

// eventPlan — состав события: ожидания возврата, строки ручного уменьшения веса и
// вид для записи (Notify=false — сообщать нечего).
type eventPlan struct {
	Kind     returns.EventKind
	Expected []returns.Expected
	Drops    []returns.ManualWeightDrop
	Notify   bool
}

// resolveEvent — состав события: ожидания возврата, строки ручного уменьшения
// веса и вид для записи; Notify=false — сообщать нечего.
func (uc *UseCase) resolveEvent(ctx context.Context, ev *returns.ReturnEvent,
	kind returns.EventKind) (eventPlan, error) {
	expected, err := uc.buildExpected(ctx, ev)
	if err != nil && !errors.Is(err, returns.ErrNothingToReturn) {
		return eventPlan{}, err
	}

	drops, err := uc.manualWeightDrops(ctx, ev)
	if err != nil {
		return eventPlan{}, err
	}

	k, ok := eventKind(kind, len(expected), len(drops))

	return eventPlan{Kind: k, Expected: expected, Drops: drops, Notify: ok}, nil
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
	// резерва — уведомлять нечего). Ядро сверки даёт ожидания возврата в
	// продажу, а ручное уменьшение веса отложенной весовой строки ожиданий не
	// создаёт (scanmatch: физически тот же кусок) — его собираем отдельно, своим
	// решением. Сообщение на событие ОДНО: expected и drops уходят вместе.
	plan, err := uc.resolveEvent(ctx, ev, kind)
	if err != nil {
		return err
	}

	// Вид события. Отмена — свой вид (состав возврата — позиции заказа), как
	// раньше, вес к ней отношения не имеет. Для события диффа: возврат в продажу
	// приоритетнее веса (штучное расформирование + урезанный вес в одном событии
	// — это KindRemoved, кнопка на страницу возврата); чистое уменьшение веса —
	// свой вид; нечего сообщать — пропускаем.
	if !plan.Notify {
		slog.Info("returns: событие без отложенных позиций — пропущено",
			"event", row.ID, "order", orderName, "kind", kind)
		return nil
	}
	ev.Kind = plan.Kind

	inserted, err := uc.repo.InsertEvent(ctx, ev)
	if err != nil {
		return fmt.Errorf("returns insert event %s: %w", row.ID, err)
	}
	if !inserted {
		return nil // дубль на границе окна — другой тик уже обработал
	}
	slog.Info("returns: событие аудита принято", "event", row.ID, "order", orderName, "kind", ev.Kind)

	if err := uc.sendEventMessage(ctx, ev, plan.Expected, plan.Drops); err != nil {
		// Событие остаётся в статусе new — retryNew дослает в следующих тиках.
		slog.Error("returns send notification failed", "event", row.ID, "err", err)
	}
	return nil
}

// sendEventMessage — текст уведомления (ожидания возврата + строки ручного
// уменьшения веса) и отправка в чат склада с URL-кнопкой. Кнопка ведёт на
// страницу возврата, если есть ожидания; при одном весе — на страницу заказа
// (склад перевешивает/переподбирает там). Событие только с весом на странице
// возврата делать нечего — после успешной отправки сразу закрываем его вручную
// (строка остаётся отметкой дедупа: ListActive отдаёт лишь new/sent).
func (uc *UseCase) sendEventMessage(ctx context.Context, ev *returns.ReturnEvent, expected []returns.Expected, drops []returns.ManualWeightDrop) error {
	button := uc.returnURL(ev.ID)
	if len(expected) == 0 {
		button = uc.orderURL(ev.OrderID)
	}

	chatID, messageID, err := uc.notify.SendWarehouseReturn(ctx, uc.messageText(ev, expected, drops), button)
	if err != nil {
		return err
	}
	if chatID == 0 || messageID == 0 {
		return nil // уведомления не настроены (нет чата склада) — событие видно на странице
	}

	if err := uc.repo.MarkSent(ctx, ev.ID, chatID, messageID); err != nil {
		return fmt.Errorf("returns mark sent %s: %w", ev.ID, err)
	}
	slog.Info("returns: уведомление отправлено в чат склада",
		"event", ev.ID, "order", ev.OrderName, "chat", chatID, "message", messageID)

	if len(expected) == 0 {
		// Уведомление «вес уменьшен вручную»: расформировывать на странице
		// возврата нечего (там только штучный возврат) — событие закрываем
		// сразу, чтобы оно не висело в активных. Повторный тик дубль не шлёт:
		// processRow пропускает уже отслеживаемое событие, retryNew — не new.
		if err := uc.repo.MarkDone(ctx, ev.ID, true); err != nil {
			return fmt.Errorf("returns mark done %s: %w", ev.ID, err)
		}
		ev.Status = returns.StatusDone
		ev.Manual = true
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
		if err != nil && !errors.Is(err, returns.ErrNothingToReturn) {
			slog.Error("returns retry build expected", "event", ev.ID, "err", err)
			continue
		}
		// Событие могло остаться new и как «вес уменьшен вручную» (TG был
		// недоступен при первой отправке): у него нет expected, но есть drops —
		// пересобираем их тем же путём, что processRow.
		drops, err := uc.manualWeightDrops(ctx, &ev)
		if err != nil {
			slog.Error("returns retry build manual weight drops", "event", ev.ID, "err", err)
			continue
		}

		if len(expected) == 0 && len(drops) == 0 {
			// Событие «опустело» (заказ изменился после создания) — закрываем.
			if doneErr := uc.repo.MarkDone(ctx, ev.ID, false); doneErr != nil {
				slog.Error("returns close emptied event", "event", ev.ID, "err", doneErr)
			}
			continue
		}

		if err := uc.sendEventMessage(ctx, &ev, expected, drops); err != nil {
			slog.Error("returns retry notification", "event", ev.ID, "err", err)
		}
	}
	return nil
}

// messageText — текст уведомления в чат склада: отмена — фразой о статусе;
// иначе строки ушедших позиций возврата («удалили») и уменьшенного вручную
// веса — в одном сообщении на событие.
func (uc *UseCase) messageText(ev *returns.ReturnEvent, expected []returns.Expected, drops []returns.ManualWeightDrop) string {
	if ev.Kind == returns.KindCancelled {
		return fmt.Sprintf("Заказ %s был переведён в статус «Отменён»", ev.OrderName)
	}

	var sb strings.Builder
	if len(expected) > 0 {
		fmt.Fprintf(&sb, "Из заказа %s удалили:", ev.OrderName)
		for _, e := range expected {
			sb.WriteString("\n— ")
			sb.WriteString(e.Name)
			sb.WriteByte(' ')
			sb.WriteString(formatQty(e))
		}
	}
	if len(drops) > 0 {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "В заказе %s вручную уменьшили вес:", ev.OrderName)
		for _, d := range drops {
			sb.WriteString("\n— ")
			sb.WriteString(d.Name)
			// Формат кг — 3 знака, как в formatQty: «2.482 кг → 1.962 кг».
			fmt.Fprintf(&sb, " %.3f кг → %.3f кг", d.BeforeKg, d.AfterKg)
		}
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

// orderURL — адрес страницы заказа МС для URL-кнопки уведомления о ручном
// уменьшении веса: склад перевешивает/переподбирает отложенную позицию там
// (страница возврата обслуживает только штучный возврат). Срез хвостовых «/» —
// как в returnURL (PublicURL может кончаться слэшем).
func (uc *UseCase) orderURL(orderID string) string {
	base := uc.cfg.PublicURL
	for base != "" && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + "/ms/orders/" + orderID
}

// skippedSource — событие от нашего API (source из списка пропуска)?
// nil (старые события без source) не пропускается.
func (uc *UseCase) skippedSource(source *string) bool {
	if source == nil {
		return false
	}
	return slices.Contains(uc.cfg.SkipSources, *source)
}

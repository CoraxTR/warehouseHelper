// Пакет usecase — поллер модуля «Проверка сайта»: раз в час получает фид сайта
// и сверяет его с базой (sitecheck.Check), расхождения уходят задачами в общий
// канал. Своих часов и соединений пакет не заводит: время — шов Now, сеть и БД —
// швы ports (интерфейсы ниже, реализации связывает di.go).
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/sitecheck"
)

// Швы модуля — интерфейсы на стороне потребителя, реализации связывает di.go:
// feed — адаптер siteCheckFeed (app, HTTP), catalog — *goods/usecase
// (SiteCheckTargets), stock — *stock/usecase (SiteCheckPositions), tasks —
// *tasks/usecase (Open), warehouse — *telegram.Notifier (NotifyWarehouse).

// Fetcher — загрузка фида сайта: тело ответа как есть (разбор — sitecheck.ParseFeed).
type Fetcher interface {
	FetchFeed(ctx context.Context) ([]byte, error)
}

// Catalog — позиции каталога с заданным url на сайте (вход сверки).
type Catalog interface {
	SiteCheckTargets(ctx context.Context) ([]sitecheck.Target, error)
}

// Stock — состояние позиций по внутренним данным: остаток по факту и скидка
// сайта позиции (шов модуля «Сроки»).
type Stock interface {
	SiteCheckPositions(ctx context.Context) ([]sitecheck.Position, error)
}

// Tasks — задачи общего канала (модуль «Внутренние задачи»): уведомление
// уходит сообщением с кнопкой отметки «кто выполнил» и строкой ленты, как у
// уведомлений о наличии и скидках (решение владельца, 29.09.2026).
type Tasks interface {
	Open(ctx context.Context, kind domain.TaskKind, text string) error
}

// Warehouse — сообщения в чат склада: сбой самой проверки (фид не обновился).
type Warehouse interface {
	NotifyWarehouse(text string) error
}

// Config — параметры модуля (собираются в di.go из конфига приложения).
type Config struct {
	// PollInterval — период опроса фида внутри часа проверки (30 с).
	PollInterval time.Duration
	// MaxAttempts — сколько раз за час пробуем получить свежий фид, после чего
	// уведомляем чат склада и ждём следующий час (60 попыток × 30 с = полчаса).
	MaxAttempts int
	// Now — часы (шов: тесты подставляют своё время); nil — time.Now.
	Now func() time.Time
}

// UseCase — поллер проверки сайта.
type UseCase struct {
	cfg       Config
	feed      Fetcher
	catalog   Catalog
	stock     Stock
	tasks     Tasks
	warehouse Warehouse

	// Состояние часа проверки: пишет Run, читают тесты — под мутексом.
	mu       sync.Mutex
	polling  bool      // идёт опрос фида в текущем часе
	attempts int       // сколько попыток опроса уже сделано в этом часе
	closed   time.Time // час, проверка которого выполнена (нулевое — ни одного)
}

// NewUseCase собирает юзкейс модуля; нулевые параметры заменяются значениями по
// умолчанию (30 с, 60 попыток, time.Now).
func NewUseCase(cfg Config, feed Fetcher, catalog Catalog, stock Stock, tasks Tasks, warehouse Warehouse) *UseCase {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 60
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	return &UseCase{
		cfg:       cfg,
		feed:      feed,
		catalog:   catalog,
		stock:     stock,
		tasks:     tasks,
		warehouse: warehouse,
	}
}

// Run — поллер: раз в PollInterval делает тик. Тик сам решает, начинать ли
// проверку часа (в 0 минут), продолжать опрос свежего фида или ждать следующего
// часа. Ошибка тика поллер не роняет: логируется, следующий тик продолжит
// (состояние часа — в uc, а не в ошибках).
func (uc *UseCase) Run(ctx context.Context) error {
	slog.Info("sitecheck: поллер проверки сайта запущен",
		"интервал", uc.cfg.PollInterval.String(), "попыток в час", uc.cfg.MaxAttempts)

	ticker := time.NewTicker(uc.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := uc.tick(ctx); err != nil && ctx.Err() == nil {
				slog.Error("sitecheck: тик проверки сайта", "err", err)
			}
		}
	}
}

// tick — один тик поллера.
//
// Фид сайта обновляется в 0 минут каждого часа, а сколько времени сайт его
// собирает — заранее неизвестно, поэтому в начале часа фид запрашивается
// каждые PollInterval секунд, пока не придёт фид, составленный в текущем часе
// (решение владельца, 29.09.2026). Свежего фида нет за MaxAttempts попыток —
// уведомление в чат склада и стоп до следующего часа.
//
// Окно старта шире одной минуты: рестарт или выход из сна в начале часа не
// должен пропускать проверку — пока час не закрыт и лимит попыток не исчерпан,
// мы всё ещё внутри той же проверки.
func (uc *UseCase) tick(ctx context.Context) error {
	now := uc.cfg.Now()
	hour := now.Truncate(time.Hour)

	uc.mu.Lock()
	// Час уже проверен: фид обновляется в 0 минут, повторять нечего.
	if !uc.closed.IsZero() && uc.closed.Equal(hour) {
		uc.mu.Unlock()

		return nil
	}
	if !uc.polling {
		if now.Minute() != 0 && now.Minute() >= uc.windowMinutes() {
			uc.mu.Unlock()

			return nil
		}
		uc.polling, uc.attempts = true, 0
	}
	uc.attempts++
	attempt := uc.attempts
	uc.mu.Unlock()

	data, err := uc.feed.FetchFeed(ctx)
	if err != nil {
		return uc.miss(now, attempt, fmt.Errorf("запрос фида: %w", err))
	}

	// Зона разбора — зона часов процесса: время в фиде местное (МСК на проде),
	// сравнивать его с часовым окном можно только в этой же зоне.
	feed, err := sitecheck.ParseFeed(data, uc.cfg.Now().Location())
	if err != nil {
		return uc.miss(now, attempt, err)
	}

	if !freshFeed(feed.CreatedAt, now) {
		return uc.miss(now, attempt, fmt.Errorf("фид не свежий: составлен %s", feedTime(feed.CreatedAt)))
	}

	return uc.handle(ctx, now, feed)
}

// handle — сверка свежего фида: позиции каталога с url против фида, расхождения
// уходят задачами. Час закрывается только после успешной сверки: ошибка чтения
// каталога или остатков оставляет час открытым, следующий тик повторит (фид уже
// свежий, так что повтор подхватится с первой попытки).
func (uc *UseCase) handle(ctx context.Context, now time.Time, feed sitecheck.Feed) error {
	targets, err := uc.catalog.SiteCheckTargets(ctx)
	if err != nil {
		return fmt.Errorf("каталог для сверки: %w", err)
	}

	positions, err := uc.stock.SiteCheckPositions(ctx)
	if err != nil {
		return fmt.Errorf("остатки для сверки: %w", err)
	}
	byID := make(map[string]sitecheck.Position, len(positions))
	for _, p := range positions {
		byID[p.ProductID] = p
	}

	idx := sitecheck.IndexFeed(feed.Items)
	notices := make([]sitecheck.Notice, 0)
	for _, t := range targets {
		// Позиции нет в срезе остатков — остатка нет и скидки нет (Position
		// нулевое): «не убрали с сайта» по такой позиции и сработает.
		notices = append(notices, sitecheck.Check(t, byID[t.ProductID], idx)...)
	}

	uc.mu.Lock()
	uc.polling = false
	uc.closed = now.Truncate(time.Hour)
	uc.mu.Unlock()

	slog.Info("sitecheck: сверка сайта выполнена",
		"фид", feedTime(feed.CreatedAt), "позиций в фиде", len(feed.Items),
		"позиций с url", len(targets), "расхождений", len(notices))

	uc.notify(ctx, notices)

	return nil
}

// miss — попытка получить свежий фид не удалась. Пока лимит не исчерпан —
// просто ждём следующего тика (причина в INFO: неудача внутри часа нормальна).
// Лимит исчерпан — уведомление в чат склада, запись в лог и стоп до следующего
// часа (решение владельца, 29.09.2026: бить дальше весь час не надо).
func (uc *UseCase) miss(now time.Time, attempt int, cause error) error {
	if attempt < uc.cfg.MaxAttempts {
		slog.Info("sitecheck: свежий фид не получен, продолжаем опрос",
			"попытка", attempt, "из", uc.cfg.MaxAttempts, "причина", cause)

		return nil
	}

	uc.mu.Lock()
	uc.polling = false
	uc.closed = now.Truncate(time.Hour)
	uc.mu.Unlock()

	slog.Error("sitecheck: свежий фид не получен за лимит попыток — сверка сайта не выполнена",
		"час", now.Format("15:04"), "попыток", attempt, "причина", cause)

	if uc.warehouse == nil {
		return nil
	}

	text := fmt.Sprintf("Проверка сайта: фид не обновился за %d попыток (%d мин), сверка сайта не выполнена. Последняя причина: %v",
		attempt, uc.windowMinutes(), cause)
	if err := uc.warehouse.NotifyWarehouse(text); err != nil {
		return fmt.Errorf("уведомление складу о несвежем фиде: %w", err)
	}

	return nil
}

// notify — расхождения уходят задачами общего канала. Ошибка задачи сверку не
// роняет: расхождение найдётся и в следующем часе, а важнее не потерять сам
// факт проверки (шов не подключён — тексты только в лог).
func (uc *UseCase) notify(ctx context.Context, notices []sitecheck.Notice) {
	for _, n := range notices {
		if uc.tasks == nil {
			slog.Info(fmt.Sprintf("sitecheck: задача (шов не подключён): %s", n.Text))

			continue
		}
		if err := uc.tasks.Open(ctx, n.Kind, n.Text); err != nil {
			slog.Info(fmt.Sprintf("sitecheck: задача %s: %v", n.Text, err))
		}
	}
}

// windowMinutes — сколько минут часа отведено на попытки (лимит × период).
// Считается в минутах с дробью: при периоде 30 с и 60 попытках это полчаса
// (30 мин), а не «0 × 60» — окно не должно схлопываться в «только ровно 0-я
// минута», иначе рестарт в начале часа пропускал бы проверку.
func (uc *UseCase) windowMinutes() int {
	return int(uc.cfg.PollInterval.Minutes() * float64(uc.cfg.MaxAttempts))
}

// freshFeed — фид составлен в текущем часе. Сравниваются дата и час: время
// сайта (МСК) и время процесса — одна зона, минуты не важны.
func freshFeed(createdAt, now time.Time) bool {
	if createdAt.IsZero() {
		return false
	}
	y1, m1, d1 := createdAt.Date()
	y2, m2, d2 := now.Date()

	return y1 == y2 && m1 == m2 && d1 == d2 && createdAt.Hour() == now.Hour()
}

// feedTime — время составления фида в логах и уведомлении складу.
func feedTime(createdAt time.Time) string {
	if createdAt.IsZero() {
		return "не указано"
	}

	return createdAt.Format("02.01.2006 15:04")
}

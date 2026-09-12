package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	cucase "warehouseHelper/internal/complaints/usecase"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/telegram"
)

type App struct {
	di         *DIContainer
	httpServer *http.Server

	// ctx — корневой контекст приложения: живёт дольше initDeps, отменяется
	// только в Shutdown, чтобы фон (тикеры, поллеры, наблюдатели) завершился
	// одним сигналом, а не каждый по своему.
	//nolint:containedctx // корень жизненного цикла: прокидывать ctx через все инициализаторы смысла нет, его отменяет именно Shutdown
	ctx    context.Context
	cancel context.CancelFunc

	// wg считает фоновые горутины приложения: Shutdown ждёт их завершения,
	// иначе процесс выйдет посреди записи в БД.
	wg sync.WaitGroup

	// RefGoCheckAgainstModule включает модуль сверки с перевозчиком.
	// Выставляется в false, если в .env не заданы параметры модуля.
	RefGoCheckAgainstModule bool
}

func New() *App {
	ctx, cancel := context.WithCancel(context.Background())

	a := &App{
		di:     NewDIContainer(),
		ctx:    ctx,
		cancel: cancel,
	}

	a.initDeps()

	a.RefGoCheckAgainstModule = a.di.Config().CheckAgainstModule

	return a
}

// Run поднимает http-сервер и ждёт либо сигнала остановки (Ctrl+C/SIGTERM),
// либо падения сервера. Любой из исходов завершается Shutdown: закрыть
// ресурсы нужно и при ошибке запуска (например, занятый порт).
func (a *App) Run() error {
	errCh := make(chan error, 1)

	go func() {
		if err := a.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		// Сервер упал сам — всё равно гасим ресурсы: разделять «упал» и
		// «остановили» незачем, набор действий один.
		shutdownErr := a.Shutdown()

		return errors.Join(err, shutdownErr)

	case <-ctx.Done():
		// Вернуть обработку сигналов по умолчанию: второй Ctrl+C убьёт
		// процесс сразу, не дожидаясь таймаутов остановки.
		stop()
	}

	slog.Info("получен сигнал остановки: новые запросы не принимаем, ждём активные и фоновые задачи")

	return a.Shutdown()
}

// Shutdown останавливает приложение снаружи внутрь: сначала сервер (новые
// запросы не принимаются, активные дорабатывают), затем живые ws-соединения,
// затем фон, и только потом ресурсы БД. Порядок важен: закрой пул раньше —
// активные запросы упадут с ошибкой БД вместо того, чтобы доработать.
func (a *App) Shutdown() error {
	var errs []error

	// 1. HTTP: перестаём принимать новое. Долгие запросы (выгрузка, печать
	// бланков пачкой, экспорт Excel) успевают доработать и отдать файл.
	if err := shutdownHTTPServer(a.httpServer, httpShutdownTimeout); err != nil {
		errs = append(errs, err)
	}

	// 2. Веб-сокеты «Сроков» hijack'нуты: http.Server их не закрывает и не
	// ждёт, поэтому закрываем хаб сами. Только если он создан: ленивый геттер
	// построил бы хаб (и полстраницы роутера) прямо на остановке.
	if a.di.stockHub != nil {
		a.di.stockHub.Close()
	} else {
		slog.Debug("ws: хаб не создавался — закрывать нечего")
	}

	// 3. Фон: отменяем корневой ctx (один сигнал всем тикерам и поллерам) и
	// ждём их. Не дождались — не блокируем процесс: см. waitBackground.
	a.cancel()
	waitBackground(&a.wg, backgroundShutdownTimeout)

	// 4. Ресурсы модулей (предзагрузка PDF, воркерпул МС, бэкфилл, пул БД) —
	// после того, как все, кто ими пользовался, остановлены.
	if err := a.di.Close(); err != nil {
		errs = append(errs, err)
	}

	slog.Info("приложение остановлено")

	return errors.Join(errs...)
}

// background запускает фоновую задачу от корневого контекста приложения и
// учитывает её в wg — Shutdown дождётся всех. name нужен только для debug-лога:
// при отмене ctx задачи выходят штатно, имя помогает понять, кто это был.
func (a *App) background(name string, fn func()) {
	a.wg.Add(1)

	go func() {
		defer a.wg.Done()

		fn()

		slog.Debug("фон: задача завершилась", "задача", name)
	}()
}

func (a *App) initDeps() {
	inits := []func(){
		a.initHTTPServer,
		a.initStockCache,
		a.initDayState,
		a.initAverageSales,
		a.initTableSizes,
		a.initComplaints,
		a.initReturns,
		a.initReserveWatch,
	}

	for _, init := range inits {
		init()
	}
}

// initTableSizes запускает фоновый опрос размеров таблиц БД для метрик
// (pg_table_sizes_bytes в /metrics). Ошибки не роняют приложение: метрика
// обновится при следующем тике (раз в минуту).
func (a *App) initTableSizes() {
	pg := a.di.OrdersRepository() // пул создаётся один раз, вне ctx-функции

	a.background("опрос размеров таблиц", func() {
		refresh := func() {
			sizes, err := pg.TableSizes(a.ctx)
			if err != nil {
				slog.Info(fmt.Sprintf("опрос размеров таблиц: %v", err))
				return
			}
			metrics.SetTableSizes(sizes)
		}
		refresh()

		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	})
}

// initAverageSales запускает стартовую дозаливку средних продаж: товары без
// месячной истории заполняются в фоне (запросы к МС через воркерпул).
// Ошибки не роняют приложение — дозаливка идемпотентна, повторится после
// перезапуска (или при следующем сохранении/выгрузке товара).
func (a *App) initAverageSales() {
	a.di.AverageSalesUC().BackfillMissing()
}

// initStockCache прогревает кэш остатков модуля «Сроки» всем каталогом.
// Ошибка не роняет приложение (схема product_stock может быть не применена),
// но пишется в ERROR: страницы «Сроки» пусты — по логу должно быть видно,
// почему (INFO при LOG_LEVEL=ERROR/INFO не пробивается).
func (a *App) initStockCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	uc := a.di.StockUC()
	if err := uc.WarmUp(ctx); err != nil {
		slog.Error(fmt.Sprintf("прогрев кэша остатков: %v (страницы «Сроки» пусты; примените product_stock_schema.sql и перезапустите)", err))
		return
	}
	slog.Info(fmt.Sprintf("кэш остатков прогрет: %d товаров (страницы «Сроки»/«Шорт-лист»)", len(uc.Snapshot())))
}

// initDayState запускает фоновую задачу утреннего снапшота состояний по дням
// (модуль daystate): время — APP_DAYSTATE_SNAPSHOT_TIME (локальное, default
// 09:00). Ошибки не роняют приложение — ретрай на следующем тике (спящий ПК,
// недоступная БД). Start блокируется до отмены ctx, поэтому идёт в фон под
// учётом wg: Shutdown дождётся снапшота, а не закроет пул под ним.
func (a *App) initDayState() {
	snapshotTime := a.di.Config().DayStateSnapshotTime

	a.background("daystate: утренний снапшот", func() {
		a.di.DayStateUC().Start(a.ctx, snapshotTime)
	})
}

// initComplaints запускает фоновые задачи модуля «Жалобы»:
//   - тикер напоминаний по дедлайнам (раз в минуту): обращения, у которых
//     наступил дедлайн и статус не «Завершено», получают уведомление в
//     common_chat, дедлайн сдвигается на сутки;
//   - long-polling поллер inline-кнопок «Получить подробности» (отвечает
//     на нажатия карточкой обращения и фотографиями).
//
// Оба переживают сбои: тикер ретраит на следующем тике, поллер логирует
// ошибки и продолжает. Без токена бота поллер не запускается.
func (a *App) initComplaints() {
	uc := a.di.ComplaintsUC()

	// Start блокируется до отмены ctx — как и поллер, идёт в фон под учётом wg.
	a.background("complaints: тикер напоминаний", func() {
		uc.Start(a.ctx)
	})

	token := a.di.Config().BotToken
	if token == "" {
		slog.Info("complaints: поллер не запущен: токен бота не настроен")
		return
	}
	poller := telegram.NewPoller(token, func(ctx context.Context, cb telegram.CallbackQuery) error {
		id, ok := cucase.ParseCallbackData(cb.Data)
		if !ok {
			return nil // кнопка не нашего модуля — не наше нажатие
		}
		return uc.HandleDetailsButton(ctx, cb.ID, cb.ChatID, id)
	})
	a.background("complaints: поллер кнопок", func() {
		if err := poller.Run(a.ctx); err != nil {
			slog.Info(fmt.Sprintf("complaints: поллер завершился: %v", err))
		}
	})
}

// initReturns запускает наблюдатель журнала действий МС (модуль returns:
// «Возврат в продажу»): раз в минуту опрос audit с курсора, уведомления в
// чат склада. Run живёт до завершения процесса: ошибки тиков логируются
// внутри и не роняют приложение (следующий тик повторит). Без
// MSAPI_CANCELLED_STATE_ID перевод заказов в «Отменён» не отслеживается
// (предупреждение в логе модуля) — удаления позиций работают.
func (a *App) initReturns() {
	uc := a.di.ReturnsUC()
	a.background("returns: наблюдатель журнала", func() {
		if err := uc.Run(a.ctx); err != nil {
			slog.Info(fmt.Sprintf("returns: наблюдатель завершился: %v", err))
		}
	})
}

// initReserveWatch запускает наблюдатель резервов заказов (модуль
// reservewatch: «Контроль резервов»): раз в минуту лист заказов в рабочих
// статусах с плановой отгрузкой в окне, сверка reserve == quantity по
// позициям, уведомления в чат склада с кнопкой «Подобрать». Run живёт до
// завершения процесса: ошибки тиков логируются внутри и не роняют
// приложение (следующий тик повторит). Без MSAPI_RESERVEWATCH_STATES модуль
// не запускается (статусы окна не заданы).
func (a *App) initReserveWatch() {
	uc := a.di.ReserveWatchUC()
	if len(a.di.Config().ReserveWatchStates) == 0 {
		slog.Info("reservewatch: не запущен: MSAPI_RESERVEWATCH_STATES не задан")
		return
	}
	a.background("reservewatch: наблюдатель резервов", func() {
		if err := uc.Run(a.ctx); err != nil {
			slog.Info(fmt.Sprintf("reservewatch: наблюдатель завершился: %v", err))
		}
	})
}

func (a *App) initHTTPServer() {
	a.httpServer = &http.Server{
		Addr:              config.NewConfig().HTTPAddress,
		Handler:           metrics.Middleware(a.di.MUX()),
		ReadHeaderTimeout: 2 * time.Second,
	}
}

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
		// stop() до Shutdown — симметрично signal-ветке: возвращаем обработку
		// сигналов по умолчанию, чтобы второй Ctrl+C убил процесс сразу, а не
		// ждал таймаутов остановки (порт занят — ждать особенно незачем).
		stop()

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
	// ждём их. Не дождались — не блокируем процесс, но помечаем остановку как
	// неполную: main по этой ошибке отличит «штатно» от «бросили работу».
	a.cancel()
	if !waitBackground(&a.wg, backgroundShutdownTimeout) {
		errs = append(errs, ErrStopIncomplete)
	}

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
	a.wg.Go(func() {
		fn()

		slog.Debug("фон: задача завершилась", "задача", name)
	})
}

func (a *App) initDeps() {
	inits := []func(){
		a.initHTTPServer,
		a.initStockCache,
		a.initDayState,
		a.initAverageSales,
		a.initDiscounts,
		a.initTableSizes,
		a.initComplaints,
		a.initBotPoller,
		a.initReturns,
		a.initReserveWatch,
	}

	for _, init := range inits {
		init()
	}
}

// initDiscounts запускает расписание модуля скидок: утренний шаг (окно
// оборотов, пересчёт по сроку, дайджест в общий чат), часовой пересчёт избытка
// и ТГ-день — план слота (14:00) и подъём general (16:00) в дни вт/чт. Шаги
// идемпотентны и проверяют маркеры дня в БД, поэтому после сна или рестарта
// добираются сами (см. usecase/schedule.go).
func (a *App) initDiscounts() {
	uc := a.di.DiscountsUC()
	schedule := a.di.DiscountSchedule()
	a.background("скидки: расписание (утро, час, ТГ-день)", func() { uc.Run(a.ctx, schedule) })
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
}

// initBotPoller запускает поллер бота: нажатия кнопок карточек жалоб и
// текстовые команды (сейчас /discounts — отчёт модуля скидок в чат отправителя).
//
// Поллер живёт здесь, а не внутри модуля жалоб: он ОДИН на токен (второй
// getUpdates получит 409 Conflict), а команды принадлежат разным модулям —
// иначе без жалоб не работала бы и команда скидок.
//
// Здесь же — регистрация меню команд («/» в клиентах): отдельной фоновой
// задачей, потому что поллеру ждать её незачем, а неудача меню приёму команд
// не мешает (набранный руками текст приходит и без регистрации).
func (a *App) initBotPoller() {
	token := a.di.Config().BotToken
	if token == "" {
		slog.Info("бот: поллер не запущен: токен бота не настроен")
		return
	}

	complaintsUC := a.di.ComplaintsUC()
	poller := telegram.NewPoller(token, func(ctx context.Context, cb telegram.CallbackQuery) error {
		id, ok := cucase.ParseCallbackData(cb.Data)
		if !ok {
			return nil // кнопка не нашего модуля — не наше нажатие
		}
		return complaintsUC.HandleDetailsButton(ctx, cb.ID, cb.ChatID, id)
	})
	poller.SetMessageHandler(a.botMessageHandler())

	// Нотифаер собирается ЗДЕСЬ, как и юзкейс команд: за геттером стоит
	// создание пула БД, а такая цепочка из ctx-функции не проходит линт
	// (contextcheck).
	notifier := a.di.TelegramNotifier()
	a.background("бот: меню команд", func() {
		if err := notifier.SetCommands(a.ctx, botCommands()); err != nil {
			slog.Info(fmt.Sprintf("бот: меню команд не задано: %v", err))
		}
	})

	a.background("бот: поллер апдейтов", func() {
		if err := poller.Run(a.ctx); err != nil {
			slog.Info(fmt.Sprintf("бот: поллер завершился: %v", err))
		}
	})
}

// discountsCommand — имя бот-команды отчёта по скидкам. Telegram принимает в
// именах команд только строчные латинские буквы, цифры и подчёркивание: на
// кириллице команда не подсвечивается, в меню «/» не показывается и тапом не
// набирается (прежнее «/скидки» именно поэтому не работало), так что имя
// латиницей, а русское название — в описании.
const discountsCommand = "discounts"

// botCommands — меню команд бота (кнопка «/» в клиентах Telegram).
//
// setMyCommands замещает список ЦЕЛИКОМ, поэтому здесь лежит полный перечень
// команд всех модулей (не «добавка» одной), и он же — источник имён для разбора
// сообщений: имя в меню и в isDiscountsCommand обязаны совпадать, иначе команда
// будет видна, но не отвечает (проверка — TestBotCommandsMatchParser).
func botCommands() []telegram.BotCommand {
	return []telegram.BotCommand{{
		Command:     discountsCommand,
		Description: "Актуальный отчёт по скидкам",
	}}
}

// botMessageHandler — обработчик текстовых команд бота. Сейчас одна: /discounts —
// отчёт по скидкам (тот же текст, что в дайджест 09:00) в чат отправителя.
// Чужие сообщения игнорируются: отвечать на них — дело других модулей.
//
// Юзкейс собирается ЗДЕСЬ, до подписки обработчика: за ленивым геттером стоит
// создание пула БД (context.Background() внутри NewPGClient), а такая цепочка
// из ctx-функции не проходит линт (contextcheck). Контекст команды уходит в
// ReplyDigest на каждом вызове.
func (a *App) botMessageHandler() func(context.Context, telegram.Message) error {
	uc := a.di.DiscountsUC()

	return func(ctx context.Context, msg telegram.Message) error {
		if !isDiscountsCommand(msg.Text) {
			return nil
		}

		return uc.ReplyDigest(ctx, msg.ChatID)
	}
}

// isDiscountsCommand — «/discounts» с необязательным адресом бота и хвостом
// («/discounts@warehouse_bot», «/discounts ?»).
func isDiscountsCommand(text string) bool {
	return commandOf(text) == discountsCommand
}

// commandOf — имя бот-команды из текста сообщения: первое слово, приведённое к
// нижнему регистру, без ведущего слэша и без адреса бота. Без слэша — пустая
// строка: «discounts» обычным словом в чате командой не считается, иначе бот
// отвечал бы отчётом на любое упоминание слова.
func commandOf(text string) string {
	word := strings.ToLower(strings.TrimSpace(text))
	if i := strings.IndexAny(word, " \n	"); i >= 0 {
		word = word[:i]
	}

	word, ok := strings.CutPrefix(word, "/")
	if !ok {
		return ""
	}
	if i := strings.Index(word, "@"); i >= 0 {
		word = word[:i]
	}

	return word
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

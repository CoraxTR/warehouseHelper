package app

import (
	"context"
	"net/http"
	"time"

	"fmt"
	"log/slog"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/metrics"
)

type App struct {
	di         *DIContainer
	httpServer *http.Server

	// RefGoCheckAgainstModule включает модуль сверки с перевозчиком.
	// Выставляется в false, если в .env не заданы параметры модуля.
	RefGoCheckAgainstModule bool
}

func New() *App {
	a := &App{
		di: NewDIContainer(),
	}

	a.initDeps()

	a.RefGoCheckAgainstModule = a.di.Config().CheckAgainstModule

	return a
}

func (a *App) Run() error {
	return a.httpServer.ListenAndServe()
}

func (a *App) initDeps() {
	inits := []func(){
		a.initHTTPServer,
		a.initStockCache,
		a.initDayState,
		a.initAverageSales,
		a.initTableSizes,
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
	go func() {
		ctx := context.Background()
		refresh := func() {
			sizes, err := pg.TableSizes(ctx)
			if err != nil {
				slog.Info(fmt.Sprintf("опрос размеров таблиц: %v", err))
				return
			}
			metrics.SetTableSizes(sizes)
		}
		refresh()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			refresh()
		}
	}()
}

// initAverageSales запускает стартовую дозаливку средних продаж: товары без
// месячной истории заполняются в фоне (запросы к МС через воркерпул).
// Ошибки не роняют приложение — дозаливка идемпотентна, повторится после
// перезапуска (или при следующем сохранении/выгрузке товара).
func (a *App) initAverageSales() {
	a.di.AverageSalesUC().BackfillMissing()
}

// initStockCache прогревает кэш остатков модуля «Сроки» всеми лотами.
// Ошибка не роняет приложение: без схемы product_stock страницы «Сроки»
// покажут пустую таблицу (примените схему и перезапустите).
func (a *App) initStockCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.di.StockUC().WarmUp(ctx); err != nil {
		slog.Info(fmt.Sprintf("прогрев кэша остатков: %v (страницы «Сроки» пусты, примените product_stock_schema.sql и перезапустите)", err))
	}
}

// initDayState запускает фоновую задачу утреннего снапшота состояний по дням
// (модуль daystate): время — APP_DAYSTATE_SNAPSHOT_TIME (локальное, default
// 09:00). Ошибки не роняют приложение — ретрай на следующем тике (спящий ПК,
// недоступная БД).
func (a *App) initDayState() {
	a.di.DayStateUC().Start(context.Background(), a.di.Config().DayStateSnapshotTime)
}

func (a *App) initHTTPServer() {
	a.httpServer = &http.Server{
		Addr:              config.NewConfig().HTTPAddress,
		Handler:           metrics.Middleware(a.di.MUX()),
		ReadHeaderTimeout: 2 * time.Second,
	}
}

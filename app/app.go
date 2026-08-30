package app

import (
	"context"
	"log"
	"net/http"
	"time"

	"warehouseHelper/internal/config"
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
		a.initAverageSales,
	}

	for _, init := range inits {
		init()
	}
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
		log.Printf("прогрев кэша остатков: %v (страницы «Сроки» пусты, примените product_stock_schema.sql и перезапустите)", err)
	}
}

func (a *App) initHTTPServer() {
	a.httpServer = &http.Server{
		Addr:              config.NewConfig().HTTPAddress,
		Handler:           a.di.MUX(),
		ReadHeaderTimeout: 2 * time.Second,
	}
}

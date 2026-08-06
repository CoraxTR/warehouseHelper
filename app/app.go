package app

import (
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

	a.RefGoCheckAgainstModule = a.di.Config().RefGoConfig.CheckAgainstModule

	return a
}

func (a *App) Run() error {
	return a.httpServer.ListenAndServe()
}

func (a *App) initDeps() {
	inits := []func(){
		a.initHTTPServer,
	}

	for _, init := range inits {
		init()
	}
}

func (a *App) initHTTPServer() {
	a.httpServer = &http.Server{
		Addr:              config.NewConfig().HTTPAddress,
		Handler:           a.di.MUX(),
		ReadHeaderTimeout: 2 * time.Second,
	}
}

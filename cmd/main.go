package main

import (
	"log/slog"
	"os"

	"warehouseHelper/app"
	"warehouseHelper/internal/logging"
)

func main() {
	logging.Setup()

	a := app.New()

	if err := a.Run(); err != nil {
		slog.Error("ошибка запуска сервера", "error", err)
		os.Exit(1)
	}

	//TODO: Добавить graceful shutdown с перехватом сигналов и закрытием ресурсов
	//TODO: Добавить автосоздание отгрузок в МС при выгрузке заказов
	//TODO: Дать через страницу доступ к поиску всех заказов в базе
	//TODO: Добавить автопроверку и автоподьём базы, если её нет
	//TODO: Шаблоны перевести на go:embed (сейчас грузятся относительными путями "../internal/delivery/web/templates/*" и работают только из cmd/): go test пакета internal/delivery/http паникует в init, запуск из другого каталога тоже. Embed даст тесты роутера (регресс на дубли паттернов) и запуск откуда угодно
}

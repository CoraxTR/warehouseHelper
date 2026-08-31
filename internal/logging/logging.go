// Пакет logging — настройка структурированного логирования (slog).
//
// Формат — JSON (в Loki удобно фильтровать по level/полям). Уровень и файл
// задаются env: LOG_LEVEL (debug|info|warn|error, по умолчанию info) и
// APP_LOG_FILE (путь к файлу; пусто — вывод в stdout, для NSSM-службы
// перенаправление делает сам nssm).
package logging

import (
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
)

// Setup настраивает slog.Default() и возвращает логгер. Уровень: LOG_LEVEL
// (по умолчанию info); вывод: APP_LOG_FILE или stdout.
func Setup() *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))

	var w io.Writer = os.Stdout
	if path := os.Getenv("APP_LOG_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			log.Printf("logging: не удалось открыть %s, пишу в stdout: %v", path, err)
		} else {
			w = f
		}
	}

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

package config

import (
	"os"
	"strings"
)

// parseMSKeys — группа ключей из env в формате `token-Имя,token-Имя`.
// Кавычки и пробелы снимаем: значение задаётся без них, а кавычка стала бы
// частью токена и дала бы 401. Записи без «-» пропускаем: имя нужно для лога.
func parseMSKeys(env string) []MSWorker {
	parts := strings.Split(os.Getenv(env), ",")
	workers := make([]MSWorker, 0, len(parts))

	for _, raw := range parts {
		key := strings.Trim(strings.TrimSpace(raw), `"`)
		pair := strings.Split(key, "-")
		if len(pair) != 2 {
			continue
		}

		workers = append(workers, MSWorker{APIKey: pair[0], Name: pair[1]})
	}

	return workers
}

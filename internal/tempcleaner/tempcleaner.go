package tempcleaner

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// TempCleaner удаляет устаревшие файлы из временной директории.
type TempCleaner struct {
	dir string
}

// NewTempCleaner создаёт очиститель для указанной директории. Директория
// создаётся при необходимости (как в NewPDFPreloader) — ошибка логируется,
// но не прерывает инициализацию.
func NewTempCleaner(dir string) *TempCleaner {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		log.Printf("Failed to create temp dir %s: %v", dir, err)
	}

	return &TempCleaner{dir: dir}
}

// CleanOlderThan удаляет из директории все файлы, чья дата модификации (ModTime —
// для экспортных артефактов эквивалентна дате создания, т.к. файлы пишутся один раз)
// старше maxAge. Поддиректории не обходит. Ошибки удаления отдельных файлов
// логируются и не прерывают обход. Если директорию не удалось прочитать —
// возвращает ошибку.
func (c *TempCleaner) CleanOlderThan(maxAge time.Duration) error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return fmt.Errorf("failed to read temp dir %s: %w", c.dir, err)
	}

	removed := 0
	cutoff := time.Now().Add(-maxAge)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Printf("Failed to get file info for %s: %v", entry.Name(), err)

			continue
		}

		if !info.ModTime().Before(cutoff) {
			continue
		}

		if err := os.Remove(filepath.Join(c.dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			log.Printf("Failed to remove file %s: %v", entry.Name(), err)

			continue
		}

		log.Printf("Removed old temp file: %s", entry.Name())
		removed++
	}

	log.Printf("Temp cleanup finished: %d file(s) removed", removed)

	return nil
}

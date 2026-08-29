// Пакет photostore — файловое хранилище фотографий кодов маркировки:
// каждая фотография — файл QRCodes/<id>.<ext>. Записи старой схемы (папка
// QRCodes/<id>/photo.<ext>) поддерживаются в ListOlderThan и RemoveAll, чтобы
// дожить до очистки и не остаться мусором.
package photostore

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// idRe — допустимый id фото: 16 hex-символов в нижнем регистре (генерируется приложением).
var idRe = regexp.MustCompile(`^[a-f0-9]{16}$`)

// extRe — допустимое расширение файла (защита от path traversal).
var extRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// fileRe — допустимое имя файла фото: <id>.<ext>.
var fileRe = regexp.MustCompile(`^[a-f0-9]{16}\.[a-z0-9]{1,8}$`)

// Store — файловое хранилище фото: файл QRCodes/<id>.<ext>.
type Store struct {
	dir string
}

// NewStore создаёт хранилище в указанной директории. Директория создаётся
// при необходимости (как в tempcleaner.NewTempCleaner) — ошибка логируется,
// но не прерывает инициализацию: каждый Save делает свой MkdirAll.
func NewStore(dir string) *Store {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		log.Printf("photostore: создание директории %s: %v", dir, err)
	}
	return &Store{dir: dir}
}

// Save записывает фото в файл QRCodes/<id>.<ext>.
// Файл создаётся с флагом O_EXCL (существующий не перезаписывается) и
// подтверждается fsync-ом и проверкой размера: сохранение считается успешным,
// только если файл реально записан на диск и непустой. Содержимое проверяется
// по magic bytes (http.DetectContentType): в хранилище попадают только
// изображения (image/* или HEIC/HEIF), чтобы через раздачу нельзя было
// подсунуть HTML/JS. При любой ошибке созданный файл удаляется.
func (s *Store) Save(ctx context.Context, id, ext string, data io.Reader) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("photostore: сохранение фото %s: %w", id, err)
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("photostore: недопустимый id фото %q", id)
	}
	if !extRe.MatchString(ext) {
		return fmt.Errorf("photostore: недопустимое расширение файла %q", ext)
	}

	// Первые 512 байт — для проверки содержимого; bufio.Reader отдаст
	// подглянутые байты и при io.Copy ниже.
	br := bufio.NewReader(data)
	head, _ := br.Peek(512)
	if len(head) == 0 {
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: сохранение фото %s: файл пустой", id)
	}
	if !isImageContent(head) {
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: сохранение фото %s: содержимое не похоже на изображение", id)
	}

	// Файл кладётся прямо в корень хранилища; MkdirAll — страховка на случай,
	// если NewStore не смог создать директорию при старте.
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("photostore: создание директории хранилища: %w", err)
	}
	path := filepath.Join(s.dir, id+"."+ext)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: создание файла фото %s: %w", id, err)
	}

	if _, err := io.Copy(f, br); err != nil {
		_ = f.Close()
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: запись фото %s: %w", id, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: синхронизация фото %s: %w", id, err)
	}
	if err := f.Close(); err != nil {
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: закрытие файла фото %s: %w", id, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: проверка файла фото %s: %w", id, err)
	}
	if info.Size() == 0 {
		s.removeCreated(id, ext)
		return fmt.Errorf("photostore: проверка файла фото %s: файл пустой", id)
	}
	return nil
}

// isImageContent сообщает, выглядят ли первые байты файла как изображение.
// http.DetectContentType знает jpeg/png/gif/webp; HEIC/HEIF Go не распознаёт
// (отдаёт application/octet-stream) — их определяем по ftyp-боксу с брендом
// heic/heix/hevc/hevx/mif1/msf1.
func isImageContent(head []byte) bool {
	ct := http.DetectContentType(head)
	if strings.HasPrefix(ct, "image/") {
		return true
	}
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		switch string(head[8:12]) {
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return true
		}
	}
	return false
}

// removeCreated удаляет файл фото при ошибке сохранения; ошибки очистки
// логируются, но не перетирают основную ошибку.
func (s *Store) removeCreated(id, ext string) {
	if err := os.Remove(filepath.Join(s.dir, id+"."+ext)); err != nil && !os.IsNotExist(err) {
		log.Printf("photostore: удаление файла фото %s после ошибки: %v", id, err)
	}
}

// RemoveAll удаляет запись фото по имени: файл QRCodes/<name> новой схемы или
// папку QRCodes/<name>/ старой схемы (в обоих случаях передаётся name = id
// или id.ext из списка ListOlderThan); отсутствие записи не считается ошибкой.
func (s *Store) RemoveAll(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("photostore: удаление фото %s: %w", name, err)
	}
	// os.RemoveAll возвращает nil, если путь не существует.
	if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
		return fmt.Errorf("photostore: удаление фото %s: %w", name, err)
	}
	return nil
}

// ListOlderThan возвращает имена записей фото, изменённых раньше cutoff:
// файлы QRCodes/<id>.<ext> новой схемы и папки QRCodes/<id>/ старой схемы.
// Посторонние файлы и папки в корне хранилища пропускаются. ModTime записи
// не меняется после сохранения (файлы пишутся один раз), поэтому оно и есть
// время сохранения. Несуществующая директория считается пустой.
func (s *Store) ListOlderThan(ctx context.Context, cutoff time.Time) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("photostore: список фото: %w", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("photostore: чтение директории %s: %w", s.dir, err)
	}

	var old []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			// Старая схема: папка <id>/.
			if !idRe.MatchString(name) {
				continue
			}
		} else if !fileRe.MatchString(name) {
			// Новая схема: файл <id>.<ext>.
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("photostore: метаданные записи %s: %w", name, err)
		}
		if info.ModTime().Before(cutoff) {
			old = append(old, name)
		}
	}
	return old, nil
}

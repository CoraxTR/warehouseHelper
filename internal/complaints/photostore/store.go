// Пакет photostore — zip-хранилище фотографий жалоб клиентов: одна жалоба —
// один архив <complaintID>.zip в общей папке, внутри архива — файлы фото
// <id>.<ext>. Метод хранения — zip.Store (без сжатия: фото уже сжаты,
// CPU на повторное сжатие не тратим). Добавление и удаление пересобирают
// архив через временный файл и атомарный os.Rename, поэтому исходный архив
// остаётся нетронутым при любой ошибке.
package photostore

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// idRe — допустимый id фото: 16 hex-символов в нижнем регистре (генерируется приложением).
var idRe = regexp.MustCompile(`^[a-f0-9]{16}$`)

// extRe — допустимое расширение файла (защита от path traversal).
var extRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// fileRe — допустимое имя записи фото в архиве: <id>.<ext>.
var fileRe = regexp.MustCompile(`^[a-f0-9]{16}\.[a-z0-9]{1,8}$`)

// Photo — запись фото в архиве жалобы.
type Photo struct {
	Name string // имя entry в архиве: <id>.<ext>
	ID   string
	Ext  string
	Size int64
}

// Upload — фото для добавления в архив жалобы.
type Upload struct {
	ID   string // ровно 16 hex-символов нижнего регистра, генерирует вызывающий код
	Ext  string // jpg/png/webp/gif/heic/heif (строчные)
	Data io.Reader
}

// Store — zip-хранилище фото жалоб: файл <dir>/<complaintID>.zip.
// Мьютекс защищает пересборки архива (Add/Remove) от параллельного доступа:
// две одновременные пересборки одного архива через temp+rename привели бы
// к потере файлов одной из них.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore создаёт хранилище в указанной директории. Директория создаётся
// при необходимости (как в qrcodes/photostore) — ошибка логируется, но не
// прерывает инициализацию: каждая запись делает свой MkdirAll.
func NewStore(dir string) *Store {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		slog.Info(fmt.Sprintf("photostore: создание директории %s: %v", dir, err))
	}
	return &Store{dir: dir}
}

// archivePath — путь к файлу архива жалобы: <dir>/<complaintID>.zip.
func (s *Store) archivePath(complaintID int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("%d.zip", complaintID))
}

// List возвращает фото жалобы в порядке их следования в архиве (порядке
// добавления). Посторонние записи (имена, не подходящие под <id>.<ext>)
// пропускаются. Если архива жалобы нет — пустой список, nil-ошибка.
func (s *Store) List(ctx context.Context, complaintID int64) ([]Photo, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("photostore: список фото жалобы %d: %w", complaintID, err)
	}
	path := s.archivePath(complaintID)
	zr, err := s.openArchive(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("photostore: список фото жалобы %d: открытие архива %s: %w", complaintID, path, err)
	}
	defer zr.f.Close()

	photos := make([]Photo, 0)
	for _, f := range zr.zr.File {
		if !fileRe.MatchString(f.Name) {
			continue
		}
		id, ext, _ := strings.Cut(f.Name, ".")
		photos = append(photos, Photo{
			Name: f.Name,
			ID:   id,
			Ext:  ext,
			Size: int64(f.UncompressedSize64),
		})
	}
	return photos, nil
}

// Add добавляет фото в архив жалобы. Каждый Upload валидируется (id, ext,
// содержимое по magic bytes — только изображения, пустые файлы отклоняются).
// Существующий архив пересобирается: старые записи (только с валидными
// именами) переписываются первыми — в исходном порядке, с сохранением
// FileHeader.Modified — затем дописываются новые файлы. Пересборка идёт через
// временный файл в той же папке и атомарный os.Rename: при ошибке на любом
// этапе временный файл удаляется, а исходный архив остаётся нетронутым.
func (s *Store) Add(ctx context.Context, complaintID int64, ups []Upload) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("photostore: добавление фото в жалобу %d: %w", complaintID, err)
	}
	if len(ups) == 0 {
		return fmt.Errorf("photostore: добавление фото в жалобу %d: пустой список загрузок", complaintID)
	}

	// Валидация id/ext и содержимого выполняется до обращения к диску:
	// bufio.Reader подглядывает первые байты и потом отдаёт их при
	// копировании в архив, а невалидная загрузка не оставляет следов.
	type prepared struct {
		name string
		br   *bufio.Reader
	}
	preparedList := make([]prepared, 0, len(ups))
	for _, up := range ups {
		if !idRe.MatchString(up.ID) {
			return fmt.Errorf("photostore: добавление фото в жалобу %d: недопустимый id фото %q", complaintID, up.ID)
		}
		if !extRe.MatchString(up.Ext) {
			return fmt.Errorf("photostore: добавление фото в жалобу %d: недопустимое расширение %q", complaintID, up.Ext)
		}
		br := bufio.NewReader(up.Data)
		head, _ := br.Peek(512)
		if len(head) == 0 {
			return fmt.Errorf("photostore: добавление фото в жалобу %d: файл %s.%s пустой", complaintID, up.ID, up.Ext)
		}
		if !isImageContent(head) {
			return fmt.Errorf("photostore: добавление фото в жалобу %d: содержимое %s.%s не похоже на изображение", complaintID, up.ID, up.Ext)
		}
		preparedList = append(preparedList, prepared{name: up.ID + "." + up.Ext, br: br})
	}

	// Мутации сериализуются, чтобы параллельные Add/Remove одного архива
	// не теряли файлы при пересборках.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("photostore: добавление фото в жалобу %d: %w", complaintID, err)
	}

	// Старый архив (если есть) открываем заранее: его записи первыми
	// переедут в новый архив. Файл держим открытым до конца пересборки —
	// записи читаются из него по ReaderAt.
	var oldZR *zip.Reader
	path := s.archivePath(complaintID)
	if f, err := os.Open(path); err == nil {
		info, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			return fmt.Errorf("photostore: добавление фото в жалобу %d: метаданные архива %s: %w", complaintID, path, statErr)
		}
		zr, zipErr := zip.NewReader(f, info.Size())
		if zipErr != nil {
			_ = f.Close()
			return fmt.Errorf("photostore: добавление фото в жалобу %d: чтение архива %s: %w", complaintID, path, zipErr)
		}
		oldZR = zr
		defer func() {
			_ = f.Close()
		}()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("photostore: добавление фото в жалобу %d: открытие архива %s: %w", complaintID, path, err)
	}

	return s.rebuild(complaintID, oldZR, nil, func(zw *zip.Writer) error {
		for _, pu := range preparedList {
			hdr := &zip.FileHeader{Name: pu.name, Method: zip.Store, Modified: time.Now()}
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return fmt.Errorf("photostore: добавление фото в жалобу %d: заголовок записи %s: %w", complaintID, pu.name, err)
			}
			if _, err := io.Copy(w, pu.br); err != nil {
				return fmt.Errorf("photostore: добавление фото в жалобу %d: запись файла %s: %w", complaintID, pu.name, err)
			}
		}
		return nil
	})
}

// Remove убирает фото с именем name из архива жалобы. Имя должно подходить
// под <id>.<ext>, иначе — ошибка. Если архива или записи нет — не ошибка
// (идемпотентно). Если после удаления в архиве не осталось ни одного файла,
// архив удаляется целиком, чтобы не копить пустые zip.
func (s *Store) Remove(ctx context.Context, complaintID int64, name string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("photostore: удаление фото %s из жалобы %d: %w", name, complaintID, err)
	}
	if !fileRe.MatchString(name) {
		return fmt.Errorf("photostore: удаление фото %s из жалобы %d: недопустимое имя файла", name, complaintID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("photostore: удаление фото %s из жалобы %d: %w", name, complaintID, err)
	}

	path := s.archivePath(complaintID)
	zr, err := s.openArchive(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // архива нет — удалять нечего
		}
		return fmt.Errorf("photostore: удаление фото %s из жалобы %d: открытие архива %s: %w", name, complaintID, path, err)
	}
	defer func() {
		_ = zr.f.Close()
	}()

	found, remaining := false, 0
	for _, f := range zr.zr.File {
		if f.Name == name {
			found = true
			continue
		}
		if fileRe.MatchString(f.Name) {
			remaining++
		}
	}
	if !found {
		return nil // записи нет — не ошибка
	}
	if remaining == 0 {
		// В архиве не осталось ни одной фотографии — удаляем пустой архив целиком.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("photostore: удаление фото %s из жалобы %d: удаление пустого архива: %w", name, complaintID, err)
		}
		return nil
	}
	return s.rebuild(complaintID, zr.zr, map[string]struct{}{name: {}}, nil)
}

// Open открывает запись name в архиве жалобы и возвращает её содержимое.
// Имя валидируется тем же <id>.<ext> (защита от path traversal). Архива
// или записи нет — ошибка с пояснением.
func (s *Store) Open(ctx context.Context, complaintID int64, name string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("photostore: открытие фото %s жалобы %d: %w", name, complaintID, err)
	}
	if !fileRe.MatchString(name) {
		return nil, fmt.Errorf("photostore: открытие фото %s жалобы %d: недопустимое имя файла", name, complaintID)
	}
	path := s.archivePath(complaintID)
	zr, err := s.openArchive(path)
	if err != nil {
		return nil, fmt.Errorf("photostore: открытие фото %s жалобы %d: открытие архива %s: %w", name, complaintID, path, err)
	}
	for _, f := range zr.zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			_ = zr.f.Close()
			return nil, fmt.Errorf("photostore: открытие фото %s жалобы %d: распаковка записи: %w", name, complaintID, err)
		}
		// Читалка держит файл архива открытым, пока читается запись.
		return &entryReadCloser{rc: rc, archive: zr.f}, nil
	}
	_ = zr.f.Close()
	return nil, fmt.Errorf("photostore: открытие фото %s жалобы %d: запись не найдена в архиве", name, complaintID)
}

// openArchive открывает файл архива path и разбирает его. Возвращаемая
// структура владеет открытым файлом; при ошибке файл закрывается. Ошибка
// отсутствия файла — как есть (os.IsNotExist), чтобы вызывающий мог решить,
// считать ли это пустым архивом.
func (s *Store) openArchive(path string) (*zipArchive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &zipArchive{f: f, zr: zr}, nil
}

// zipArchive — открытый файл архива и разобранный zip.Reader.
type zipArchive struct {
	f  *os.File
	zr *zip.Reader
}

// entryReadCloser — чтение записи архива: закрывает и запись, и файл архива.
type entryReadCloser struct {
	rc      io.ReadCloser
	archive *os.File
}

func (e *entryReadCloser) Read(p []byte) (int, error) { return e.rc.Read(p) }

func (e *entryReadCloser) Close() error {
	err := e.rc.Close()
	if cerr := e.archive.Close(); err == nil {
		err = cerr
	}
	return err
}

// rebuild пересобирает архив жалобы: сначала записи старого архива old
// (только валидные имена <id>.<ext>, в исходном порядке, с сохранением
// FileHeader.Modified; записи из skip пропускаются), затем новые файлы через
// addNew. Пересборка идёт через временный файл в той же папке и атомарный
// os.Rename поверх старого архива; при ошибке на любом этапе временный файл
// удаляется, а старый архив остаётся нетронутым. old == nil означает, что
// старого архива нет.
func (s *Store) rebuild(complaintID int64, old *zip.Reader, skip map[string]struct{}, addNew func(zw *zip.Writer) error) error {
	// MkdirAll — страховка на случай, если NewStore не смог создать
	// директорию при старте.
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("photostore: создание директории хранилища: %w", err)
	}
	path := s.archivePath(complaintID)
	tmp, err := os.CreateTemp(s.dir, fmt.Sprintf("%d-*.tmp", complaintID))
	if err != nil {
		return fmt.Errorf("photostore: пересборка архива %s: создание временного файла: %w", path, err)
	}
	tmpPath := tmp.Name()
	// При любой ошибке временный файл удаляется: основной архив заменяется
	// только успешным os.Rename в конце.
	fail := func(cause error) error {
		_ = tmp.Close()
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			slog.Error(fmt.Sprintf("photostore: удаление временного файла %s после ошибки: %v", tmpPath, err))
		}
		return cause
	}

	zw := zip.NewWriter(tmp)
	if old != nil {
		for _, f := range old.File {
			if _, drop := skip[f.Name]; drop {
				continue
			}
			if !fileRe.MatchString(f.Name) {
				continue // посторонние записи при пересборке отбрасываются
			}
			// Свежий заголовок: метод Store, время из старой записи
			// (Modified не теряется при чтении/записи — см. archive/zip).
			hdr := &zip.FileHeader{Name: f.Name, Method: zip.Store, Modified: f.Modified}
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return fail(fmt.Errorf("photostore: пересборка архива %s: заголовок записи %s: %w", path, f.Name, err))
			}
			rc, err := f.Open()
			if err != nil {
				return fail(fmt.Errorf("photostore: пересборка архива %s: чтение записи %s: %w", path, f.Name, err))
			}
			_, copyErr := io.Copy(w, rc)
			closeErr := rc.Close()
			if copyErr != nil {
				return fail(fmt.Errorf("photostore: пересборка архива %s: копирование записи %s: %w", path, f.Name, copyErr))
			}
			if closeErr != nil {
				return fail(fmt.Errorf("photostore: пересборка архива %s: закрытие записи %s: %w", path, f.Name, closeErr))
			}
		}
	}
	if addNew != nil {
		if err := addNew(zw); err != nil {
			return fail(err)
		}
	}
	if err := zw.Close(); err != nil {
		return fail(fmt.Errorf("photostore: пересборка архива %s: финализация архива: %w", path, err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("photostore: пересборка архива %s: синхронизация файла: %w", path, err))
	}
	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("photostore: пересборка архива %s: закрытие файла: %w", path, err))
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Error(fmt.Sprintf("photostore: удаление временного файла %s после ошибки: %v", tmpPath, rmErr))
		}
		return fmt.Errorf("photostore: пересборка архива %s: замена файла: %w", path, err)
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

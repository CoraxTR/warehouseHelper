// Пакет usecase — сценарии модуля «Честный знак»: сохранение фотографий
// кодов маркировки вместе с заказом, список заказов и периодическая
// очистка устаревших фотографий.
package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"time"

	"warehouseHelper/internal/domain"
)

// ErrEmptyOrderNumber — не указан номер заказа.
var ErrEmptyOrderNumber = errors.New("номер заказа не указан")

// ErrNoPhotos — нет фотографий для сохранения.
var ErrNoPhotos = errors.New("нет фотографий")

// PhotoUpload — одна фотография на сохранение (Ext — расширение без точки, Data — содержимое).
type PhotoUpload struct {
	Ext  string
	Data io.Reader
}

// QRRepository — хранилище заказов и фото (реализация: postgres.PGClient).
type QRRepository interface {
	UpsertOrderWithPhotos(ctx context.Context, orderNumber string, photos []domain.QRPhoto) (int64, error)
	GetOrdersWithPhotos(ctx context.Context) ([]domain.QROrder, error)
	DeletePhotosByIDs(ctx context.Context, ids []string) error
	DeleteEmptyOrders(ctx context.Context) error
}

// PhotoFileStore — файловое хранилище фото (реализация: photostore.Store).
type PhotoFileStore interface {
	Save(ctx context.Context, id, ext string, data io.Reader) error
	RemoveAll(ctx context.Context, id string) error
	ListDirsOlderThan(ctx context.Context, cutoff time.Time) ([]string, error)
}

// QRUseCase — сценарии модуля «Честный знак».
type QRUseCase struct {
	repo      QRRepository
	files     PhotoFileStore
	photosDir string
	maxAge    time.Duration
}

// NewQRUseCase создаёт сценарии модуля «Честный знак».
func NewQRUseCase(repo QRRepository, files PhotoFileStore, photosDir string, maxAge time.Duration) *QRUseCase {
	return &QRUseCase{repo: repo, files: files, photosDir: photosDir, maxAge: maxAge}
}

// SavePhotos сохраняет фотографии кодов маркировки и привязывает их к заказу.
// Сначала на диск записываются и подтверждаются все файлы, и только после
// этого заказ сохраняется в БД; при любой ошибке уже созданные папки фото
// откатываются, а БД не трогается. Возвращает количество сохранённых фото.
func (u *QRUseCase) SavePhotos(ctx context.Context, orderNumber string, uploads []PhotoUpload) (int, error) {
	orderNumber = strings.TrimSpace(orderNumber)
	if orderNumber == "" {
		return 0, ErrEmptyOrderNumber
	}
	if len(uploads) == 0 {
		return 0, ErrNoPhotos
	}

	photos := make([]domain.QRPhoto, 0, len(uploads))
	var savedIDs []string
	// rollback удаляет папки фото, созданные в рамках этого вызова.
	rollback := func() {
		for _, id := range savedIDs {
			if err := u.files.RemoveAll(ctx, id); err != nil {
				log.Printf("qrcodes: откат папки фото %s: %v", id, err)
			}
		}
	}

	for _, up := range uploads {
		id, err := newPhotoID()
		if err != nil {
			rollback()
			return 0, fmt.Errorf("qrcodes: генерация id фото: %w", err)
		}
		if err := u.files.Save(ctx, id, up.Ext, up.Data); err != nil {
			rollback()
			return 0, fmt.Errorf("qrcodes: сохранение фото %s: %w", id, err)
		}
		savedIDs = append(savedIDs, id)
		photos = append(photos, domain.QRPhoto{ID: id, Ext: up.Ext})
	}

	if _, err := u.repo.UpsertOrderWithPhotos(ctx, orderNumber, photos); err != nil {
		rollback()
		return 0, fmt.Errorf("qrcodes: сохранение заказа %q: %w", orderNumber, err)
	}
	return len(photos), nil
}

// ListOrders возвращает заказы с фотографиями, отсортированные по номеру
// заказа natural-сортировкой по возрастанию («2» < «10», «2а» между «2» и «3»).
func (u *QRUseCase) ListOrders(ctx context.Context) ([]domain.QROrder, error) {
	orders, err := u.repo.GetOrdersWithPhotos(ctx)
	if err != nil {
		return nil, fmt.Errorf("qrcodes: список заказов: %w", err)
	}
	sort.Slice(orders, func(i, j int) bool {
		return naturalLess(orders[i].OrderNumber, orders[j].OrderNumber)
	})
	return orders, nil
}

// Cleanup удаляет папки фото старше maxAge и чистит связанные записи в БД.
// Ошибки удаления отдельных папок логируются, но не прерывают обход;
// возвращает количество удалённых папок.
func (u *QRUseCase) Cleanup(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-u.maxAge)
	dirs, err := u.files.ListDirsOlderThan(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("qrcodes: список устаревших папок фото: %w", err)
	}

	removed := 0
	for _, id := range dirs {
		if err := u.files.RemoveAll(ctx, id); err != nil {
			log.Printf("qrcodes: удаление папки фото %s: %v", id, err)
			continue
		}
		removed++
	}
	if removed == 0 {
		return 0, nil
	}

	if err := u.repo.DeletePhotosByIDs(ctx, dirs); err != nil {
		return removed, fmt.Errorf("qrcodes: удаление фото из БД: %w", err)
	}
	if err := u.repo.DeleteEmptyOrders(ctx); err != nil {
		return removed, fmt.Errorf("qrcodes: удаление пустых заказов: %w", err)
	}
	return removed, nil
}

// PhotosDir возвращает директорию хранения фото (для раздачи файлов хендлерами).
func (u *QRUseCase) PhotosDir() string {
	return u.photosDir
}

// newPhotoID генерирует id фото: 16 hex-символов (8 случайных байт).
func newPhotoID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// naturalLess сравнивает строки natural-сортировкой: числовые последовательности
// сравниваются по значению, нечисловые куски — лексикографически; пустые
// строки сортируются в конец. Например: «2» < «10», «2а» между «2» и «3».
func naturalLess(a, b string) bool {
	if a == "" {
		return false // пустая строка сортируется в конец
	}
	if b == "" {
		return true
	}
	for i, j := 0, 0; i < len(a) && j < len(b); {
		da, db := isDigit(a[i]), isDigit(b[j])
		if da != db {
			return da // числа идут раньше нечисловых кусков
		}
		if da {
			// Числовая последовательность: сравниваем по значению.
			si, ti := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[si:i], "0"), strings.TrimLeft(b[ti:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	// Одна строка — префикс другой: короче идёт раньше.
	return len(a) < len(b)
}

// isDigit сообщает, является ли байт десятичной цифрой.
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

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
	"sort"
	"strings"
	"sync"
	"time"

	"log/slog"
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
	RemoveAll(ctx context.Context, name string) error
	ListOlderThan(ctx context.Context, cutoff time.Time) ([]string, error)
}

// QRUseCase — сценарии модуля «Честный знак».
type QRUseCase struct {
	repo      QRRepository
	files     PhotoFileStore
	photosDir string
	maxAge    time.Duration

	// mu защищает lastCheckDate: Cleanup может вызываться из параллельных
	// запросов (одновременные сохранения фото).
	mu            sync.Mutex
	lastCheckDate string // дата последней очистки (ДД.ММ.ГГГГ); пустая — очистка ещё не выполнялась
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
	savedNames := make([]string, 0, len(uploads))
	// rollback удаляет файлы фото, созданные в рамках этого вызова. Контекст
	// берём без отмены: при обрыве клиента (отмена ctx) файлы всё равно
	// должны подчиститься, а не остаться сиротами до очистки.
	rollback := func() {
		rctx := context.WithoutCancel(ctx)
		for _, name := range savedNames {
			if err := u.files.RemoveAll(rctx, name); err != nil {
				slog.Info(fmt.Sprintf("qrcodes: откат файла фото %s: %v", name, err))
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
		savedNames = append(savedNames, id+"."+up.Ext)
		photos = append(photos, domain.QRPhoto{ID: id, Ext: up.Ext})
	}

	if _, err := u.repo.UpsertOrderWithPhotos(ctx, orderNumber, photos); err != nil {
		rollback()
		return 0, fmt.Errorf("qrcodes: сохранение заказа %q: %w", orderNumber, err)
	}

	// Очистка устаревших фото выполняется при добавлении новых (не чаще раза
	// в день — см. Cleanup); ошибка очистки не влияет на уже успешное
	// сохранение, а лишь логируется.
	if _, err := u.Cleanup(ctx); err != nil {
		slog.Info(fmt.Sprintf("qrcodes: очистка устаревших фото после сохранения: %v", err))
	}
	return len(photos), nil
}

// ListOrders возвращает заказы с фотографиями, отсортированные по номеру
// заказа natural-сортировкой по убыванию («10» выше «2», «2а» ниже «2» и
// выше «3»): более новые заказы имеют больший номер и оказываются выше.
// Пустые строки сортируются в конец.
func (u *QRUseCase) ListOrders(ctx context.Context) ([]domain.QROrder, error) {
	orders, err := u.repo.GetOrdersWithPhotos(ctx)
	if err != nil {
		return nil, fmt.Errorf("qrcodes: список заказов: %w", err)
	}
	sort.Slice(orders, func(i, j int) bool {
		a, b := orders[i].OrderNumber, orders[j].OrderNumber
		if a == "" {
			return false // пустые строки — в конец
		}
		if b == "" {
			return true
		}
		return naturalLess(b, a) // по убыванию
	})
	return orders, nil
}

// Cleanup удаляет фото старше maxAge, но не чаще раза в день: при повторном
// вызове в тот же день (lastCheckDate == сегодня) сразу возвращает 0. Дата
// без времени — удалить файлы на несколько часов раньше допустимо. Сначала
// чистится БД (строки фото и заказы без фото), потом файлы: осиротевшие
// строки не самовосстанавливаются, а осиротевшие файлы на следующем прогоне
// снова попадут в список старых. Ошибки удаления отдельных записей
// логируются, но не прерывают обход; возвращает количество удалённых
// записей (файлов новой схемы и папок старой схемы).
func (u *QRUseCase) Cleanup(ctx context.Context) (int, error) {
	u.mu.Lock()
	today := time.Now().Format("02.01.2006")
	if u.lastCheckDate == today {
		u.mu.Unlock()
		return 0, nil
	}
	u.mu.Unlock()

	cutoff := time.Now().Add(-u.maxAge)
	names, err := u.files.ListOlderThan(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("qrcodes: список устаревших фото: %w", err)
	}
	if len(names) == 0 {
		u.setLastCheckDate(today)
		return 0, nil
	}

	// В БД удаляются строки по id, извлечённому из имени записи хранилища.
	ids := make([]string, 0, len(names))
	for _, name := range names {
		ids = append(ids, photoIDFromName(name))
	}
	if err := u.repo.DeletePhotosByIDs(ctx, ids); err != nil {
		return 0, fmt.Errorf("qrcodes: удаление фото из БД: %w", err)
	}
	if err := u.repo.DeleteEmptyOrders(ctx); err != nil {
		return 0, fmt.Errorf("qrcodes: удаление пустых заказов: %w", err)
	}

	removed := 0
	for _, name := range names {
		if err := u.files.RemoveAll(ctx, name); err != nil {
			slog.Info(fmt.Sprintf("qrcodes: удаление фото %s: %v", name, err))
			continue
		}
		removed++
	}
	u.setLastCheckDate(today)
	return removed, nil
}

// setLastCheckDate запоминает дату успешной очистки под защитой мьютекса.
// Дата проставляется только после успеха: при ошибке в тот же день будет
// ещё одна попытка, а не скип до завтра.
func (u *QRUseCase) setLastCheckDate(date string) {
	u.mu.Lock()
	u.lastCheckDate = date
	u.mu.Unlock()
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

// photoIDFromName извлекает id фото из имени записи хранилища: для файла
// <id>.<ext> новой схемы и для папки <id>/ старой схемы. Имена приходят из
// ListOlderThan, который пропускает посторонние записи, поэтому формат
// всегда валиден.
func photoIDFromName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i != -1 {
		return name[:i]
	}
	return name
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

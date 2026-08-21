// Тесты сценариев модуля «Честный знак»: сохранение фотографий кодов
// маркировки, список заказов (natural-сортировка) и очистка устаревших записей.
package usecase

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/qrcodes/photostore"
)

// errStub — универсальная ошибка заглушек.
var errStub = errors.New("ошибка заглушки")

// stubQRRepo — заглушка QRRepository: запоминает вызовы и аргументы,
// умеет возвращать ошибку по сценарию.
type stubQRRepo struct {
	orders    []domain.QROrder
	upsertErr error
	photosErr error
	emptyErr  error

	upsertCalled      bool
	lastOrderNumber   string
	lastPhotos        []domain.QRPhoto
	deletePhotosIDs   []string
	deleteEmptyCalled bool
}

// Компиляционная проверка: стаб реализует QRRepository.
var _ QRRepository = (*stubQRRepo)(nil)

func (s *stubQRRepo) UpsertOrderWithPhotos(_ context.Context, orderNumber string, photos []domain.QRPhoto) (int64, error) {
	s.upsertCalled = true
	s.lastOrderNumber = orderNumber
	s.lastPhotos = photos
	return 0, s.upsertErr
}

func (s *stubQRRepo) GetOrdersWithPhotos(context.Context) ([]domain.QROrder, error) {
	return s.orders, nil
}

func (s *stubQRRepo) DeletePhotosByIDs(_ context.Context, ids []string) error {
	s.deletePhotosIDs = append(s.deletePhotosIDs, ids...)
	return s.photosErr
}

func (s *stubQRRepo) DeleteEmptyOrders(context.Context) error {
	s.deleteEmptyCalled = true
	return s.emptyErr
}

// stubFileStore — заглушка PhotoFileStore: запоминает вызовы Save/RemoveAll.
// Save возвращает saveErr, когда уже успешно сохранено failAfter фото
// (отрицательное значение — никогда не ошибаться).
type stubFileStore struct {
	saveErr   error
	failAfter int
	names     []string
	namesErr  error
	saved     []string
	saveCalls int // всего вызовов Save (включая завершившиеся ошибкой)
	removed   []string
}

// Компиляционная проверка: стаб реализует PhotoFileStore.
var _ PhotoFileStore = (*stubFileStore)(nil)

func (s *stubFileStore) Save(_ context.Context, id, _ string, _ io.Reader) error {
	s.saveCalls++
	if s.saveErr != nil && len(s.saved) >= s.failAfter {
		return s.saveErr
	}
	s.saved = append(s.saved, id)
	return nil
}

func (s *stubFileStore) RemoveAll(_ context.Context, name string) error {
	s.removed = append(s.removed, name)
	return nil
}

func (s *stubFileStore) ListOlderThan(context.Context, time.Time) ([]string, error) {
	return s.names, s.namesErr
}

// newTestStore создаёт реальное файловое хранилище фото в t.TempDir()
// (папка QRCodes/ внутри корня) и возвращает корень и само хранилище.
func newTestStore(t *testing.T) (root string, store *photostore.Store) {
	t.Helper()
	root = t.TempDir()
	store = photostore.NewStore(filepath.Join(root, "QRCodes"))
	return root, store
}

// jpgData возвращает байты с JPEG magic (FF D8 FF E0): real-store Save
// пропускает содержимое только для изображений.
func jpgData(tail string) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(tail)...)
}

func TestSavePhotosSuccess(t *testing.T) {
	root, store := newTestStore(t)
	repo := &stubQRRepo{}
	uc := NewQRUseCase(repo, store, filepath.Join(root, "QRCodes"), time.Hour)

	uploads := []PhotoUpload{
		{Ext: "jpg", Data: bytes.NewReader(jpgData("фото1"))},
		{Ext: "png", Data: bytes.NewReader(jpgData("фото2"))},
	}

	n, err := uc.SavePhotos(context.Background(), "  123  ", uploads)
	if err != nil {
		t.Fatalf("SavePhotos error: %v", err)
	}
	if n != 2 {
		t.Errorf("SavePhotos вернул %d, want 2", n)
	}
	if !repo.upsertCalled {
		t.Fatal("repo.UpsertOrderWithPhotos не вызван")
	}
	if repo.lastOrderNumber != "123" {
		t.Errorf("orderNumber = %q, want %q (обрезка пробелов)", repo.lastOrderNumber, "123")
	}
	if len(repo.lastPhotos) != 2 {
		t.Fatalf("photos передано %d, want 2", len(repo.lastPhotos))
	}
	for i, wantExt := range []string{"jpg", "png"} {
		if repo.lastPhotos[i].Ext != wantExt {
			t.Errorf("photos[%d].Ext = %q, want %q", i, repo.lastPhotos[i].Ext, wantExt)
		}
		// Файл физически записан: QRCodes/<id>.<ext>.
		path := filepath.Join(root, "QRCodes", repo.lastPhotos[i].ID+"."+wantExt)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("файл %s не создан: %v", path, err)
			continue
		}
		want := jpgData("фото1")
		if i == 1 {
			want = jpgData("фото2")
		}
		if !bytes.Equal(got, want) {
			t.Errorf("содержимое %s = %q, want %q", path, got, want)
		}
	}
}

func TestSavePhotosFileError(t *testing.T) {
	// Первое фото «сохраняется», второе падает: откатывается файл первого.
	repo := &stubQRRepo{}
	files := &stubFileStore{saveErr: errStub, failAfter: 1}
	uc := NewQRUseCase(repo, files, "photos", time.Hour)

	uploads := []PhotoUpload{
		{Ext: "jpg", Data: bytes.NewReader([]byte("1"))},
		{Ext: "jpg", Data: bytes.NewReader([]byte("2"))},
	}

	_, err := uc.SavePhotos(context.Background(), "123", uploads)
	if err == nil {
		t.Fatal("ожидалась ошибка сохранения файла")
	}
	if !strings.Contains(err.Error(), "сохранение фото") {
		t.Errorf("ошибка = %q, want содержит «сохранение фото»", err)
	}
	if repo.upsertCalled {
		t.Error("repo.UpsertOrderWithPhotos не должен вызываться при ошибке файла")
	}
	if files.saveCalls != 2 {
		t.Fatalf("Save вызван %d раз, want 2", files.saveCalls)
	}
	// Откат: файл уже «сохранённого» фото удалён через RemoveAll (id.ext).
	wantRemoved := []string{files.saved[0] + ".jpg"}
	if !reflect.DeepEqual(files.removed, wantRemoved) {
		t.Errorf("RemoveAll вызван с %v, want %v (откат первого фото)", files.removed, wantRemoved)
	}
}

func TestSavePhotosRepoError(t *testing.T) {
	root, store := newTestStore(t)
	repo := &stubQRRepo{upsertErr: errStub}
	uc := NewQRUseCase(repo, store, filepath.Join(root, "QRCodes"), time.Hour)

	uploads := []PhotoUpload{
		{Ext: "jpg", Data: bytes.NewReader(jpgData("фото1"))},
		{Ext: "png", Data: bytes.NewReader(jpgData("фото2"))},
	}

	_, err := uc.SavePhotos(context.Background(), "123", uploads)
	if err == nil {
		t.Fatal("ожидалась ошибка репозитория")
	}
	if !strings.Contains(err.Error(), "123") {
		t.Errorf("ошибка = %q, want упоминание номера заказа", err)
	}
	if !repo.upsertCalled {
		t.Fatal("repo.UpsertOrderWithPhotos не вызван")
	}
	// Откат: обе папки фото удалены с диска.
	entries, err := os.ReadDir(filepath.Join(root, "QRCodes"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("после отката остались файлы: %v", names)
	}
}

func TestSavePhotosValidation(t *testing.T) {
	tests := []struct {
		name        string
		orderNumber string
		uploads     []PhotoUpload
		wantErr     error
	}{
		{
			name:        "пустой номер заказа",
			orderNumber: "",
			uploads:     []PhotoUpload{{Ext: "jpg"}},
			wantErr:     ErrEmptyOrderNumber,
		},
		{
			name:        "номер заказа из пробелов",
			orderNumber: "   ",
			uploads:     []PhotoUpload{{Ext: "jpg"}},
			wantErr:     ErrEmptyOrderNumber,
		},
		{
			name:        "нет фотографий",
			orderNumber: "123",
			wantErr:     ErrNoPhotos,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubQRRepo{}
			files := &stubFileStore{}
			uc := NewQRUseCase(repo, files, "photos", time.Hour)

			_, err := uc.SavePhotos(context.Background(), tt.orderNumber, tt.uploads)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SavePhotos error = %v, want %v (errors.Is)", err, tt.wantErr)
			}
			if repo.upsertCalled {
				t.Error("repo.UpsertOrderWithPhotos не должен вызываться")
			}
			if files.saveCalls != 0 {
				t.Errorf("Save файлов вызван %d раз, want 0", files.saveCalls)
			}
		})
	}
}

func TestListOrdersNaturalSort(t *testing.T) {
	tests := []struct {
		name   string
		orders []string
		want   []string
	}{
		{
			name:   "числа по значению, буквенный суффикс ниже числа",
			orders: []string{"10", "2", "2а", "2б", "1", "3", "10а", "9"},
			want:   []string{"10а", "10", "9", "3", "2б", "2а", "2", "1"},
		},
		{
			name:   "10 выше 2",
			orders: []string{"10", "2"},
			want:   []string{"10", "2"},
		},
		{
			name:   "пустая строка — в конец",
			orders: []string{"10", "", "2"},
			want:   []string{"10", "2", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubQRRepo{}
			for _, num := range tt.orders {
				repo.orders = append(repo.orders, domain.QROrder{OrderNumber: num})
			}
			uc := NewQRUseCase(repo, &stubFileStore{}, "photos", time.Hour)

			got, err := uc.ListOrders(context.Background())
			if err != nil {
				t.Fatalf("ListOrders error: %v", err)
			}
			gotNums := make([]string, 0, len(got))
			for _, o := range got {
				gotNums = append(gotNums, o.OrderNumber)
			}
			if !reflect.DeepEqual(gotNums, tt.want) {
				t.Errorf("ListOrders = %v, want %v", gotNums, tt.want)
			}
		})
	}
}

func TestNaturalLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"2", "10", true},
		{"10", "2", false},
		{"2", "2а", true},
		{"2а", "2", false},
		{"2а", "2б", true},
		{"2б", "2а", false},
		{"2", "3", true},
		{"9", "10", true},
		{"10а", "10", false},
		{"1", "1", false},
		{"02", "2", false}, // лидирующие нули равны по значению, «02» длиннее
		{"", "2", false},   // пустая строка — в конец
		{"2", "", true},
		{"", "", false},
	}

	for _, tt := range tests {
		if got := naturalLess(tt.a, tt.b); got != tt.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCleanupRemovesOldRecords(t *testing.T) {
	// Имена записей хранилища: файлы новой схемы <id>.<ext> и папка старой
	// схемы <id>; в БД удаляются строки по извлечённым id (без расширения).
	oldNames := []string{"aaaaaaaaaaaaaaaa.jpg", "bbbbbbbbbbbbbbbb.png", "cccccccccccccccc"}
	wantIDs := []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"}
	repo := &stubQRRepo{}
	files := &stubFileStore{names: oldNames}
	uc := NewQRUseCase(repo, files, "photos", time.Hour)

	removed, err := uc.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}
	if removed != 3 {
		t.Errorf("Cleanup вернул %d, want 3", removed)
	}
	if !reflect.DeepEqual(files.removed, oldNames) {
		t.Errorf("RemoveAll вызван с %v, want %v", files.removed, oldNames)
	}
	if !reflect.DeepEqual(repo.deletePhotosIDs, wantIDs) {
		t.Errorf("DeletePhotosByIDs вызван с %v, want %v", repo.deletePhotosIDs, wantIDs)
	}
	if !repo.deleteEmptyCalled {
		t.Error("DeleteEmptyOrders не вызван")
	}
}

func TestCleanupNoOldDirs(t *testing.T) {
	repo := &stubQRRepo{}
	files := &stubFileStore{} // ListOlderThan → пусто
	uc := NewQRUseCase(repo, files, "photos", time.Hour)

	removed, err := uc.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}
	if removed != 0 {
		t.Errorf("Cleanup вернул %d, want 0", removed)
	}
	if len(files.removed) != 0 {
		t.Errorf("RemoveAll вызван %d раз, want 0", len(files.removed))
	}
	if repo.deletePhotosIDs != nil || repo.deleteEmptyCalled {
		t.Error("репозиторий не должен вызываться без устаревших записей")
	}
}

func TestCleanupRealStoreRemovesOldFiles(t *testing.T) {
	// Интеграция с реальным файловым хранилищем: старый файл физически
	// удаляется, свежий остаётся, репозиторий получает id удалённого файла.
	root, store := newTestStore(t)
	repo := &stubQRRepo{}
	uc := NewQRUseCase(repo, store, filepath.Join(root, "QRCodes"), time.Hour)

	oldID, err := newPhotoID()
	if err != nil {
		t.Fatalf("newPhotoID: %v", err)
	}
	freshID, err := newPhotoID()
	if err != nil {
		t.Fatalf("newPhotoID: %v", err)
	}
	for _, id := range []string{oldID, freshID} {
		if err := store.Save(context.Background(), id, "jpg", bytes.NewReader(jpgData("фото"))); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}
	// Состариваем файл oldID: maxAge = 1 час, mtime = now - 2 часа.
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "QRCodes", oldID+".jpg"), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	removed, err := uc.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}
	if removed != 1 {
		t.Errorf("Cleanup вернул %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", oldID+".jpg")); !os.IsNotExist(err) {
		t.Errorf("старый файл %s не удалён (err=%v)", oldID+".jpg", err)
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", freshID+".jpg")); err != nil {
		t.Errorf("свежий файл %s не должен удаляться: %v", freshID+".jpg", err)
	}
	if !reflect.DeepEqual(repo.deletePhotosIDs, []string{oldID}) {
		t.Errorf("DeletePhotosByIDs вызван с %v, want [%s]", repo.deletePhotosIDs, oldID)
	}
	if !repo.deleteEmptyCalled {
		t.Error("DeleteEmptyOrders не вызван")
	}
}

func TestCleanupThrottleOncePerDay(t *testing.T) {
	oldNames := []string{"aaaaaaaaaaaaaaaa.jpg"}
	repo := &stubQRRepo{}
	files := &stubFileStore{names: oldNames}
	uc := NewQRUseCase(repo, files, "photos", time.Hour)

	// Первая очистка дня выполняется.
	removed, err := uc.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}
	if removed != 1 {
		t.Errorf("Cleanup вернул %d, want 1", removed)
	}
	if len(files.removed) != 1 || len(repo.deletePhotosIDs) != 1 {
		t.Errorf("первая очистка должна выполниться: removed=%v, deletePhotosIDs=%v", files.removed, repo.deletePhotosIDs)
	}

	// Повторный вызов в тот же день — скип: файлы и репозиторий не трогаются.
	removed, err = uc.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}
	if removed != 0 {
		t.Errorf("повторный Cleanup в тот же день вернул %d, want 0", removed)
	}
	if len(files.removed) != 1 || len(repo.deletePhotosIDs) != 1 {
		t.Errorf("повторный Cleanup не должен трогать файлы/БД: removed=%v, deletePhotosIDs=%v", files.removed, repo.deletePhotosIDs)
	}

	// На следующий день (дата сброшена на вчера) очистка выполняется снова.
	uc.lastCheckDate = time.Now().AddDate(0, 0, -1).Format("02.01.2006")
	removed, err = uc.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}
	if removed != 1 {
		t.Errorf("Cleanup на следующий день вернул %d, want 1", removed)
	}
	if len(files.removed) != 2 {
		t.Errorf("RemoveAll вызван %d раз, want 2", len(files.removed))
	}
}

func TestSavePhotosIgnoresCleanupError(t *testing.T) {
	// Ошибка фоновой очистки после успешного сохранения не должна ронять
	// само сохранение: фото уже на диске и в БД, ошибку лишь логируем.
	repo := &stubQRRepo{}
	files := &stubFileStore{namesErr: errStub}
	uc := NewQRUseCase(repo, files, "photos", time.Hour)

	n, err := uc.SavePhotos(context.Background(), "12345", []PhotoUpload{
		{Ext: "jpg", Data: bytes.NewReader(jpgData("фото"))},
	})
	if err != nil {
		t.Fatalf("SavePhotos error: %v", err)
	}
	if n != 1 {
		t.Errorf("SavePhotos вернул %d, want 1", n)
	}
	if !repo.upsertCalled {
		t.Error("UpsertOrderWithPhotos не вызван")
	}
}

func TestPhotosDir(t *testing.T) {
	uc := NewQRUseCase(&stubQRRepo{}, &stubFileStore{}, "/tmp/photos", time.Hour)
	if got := uc.PhotosDir(); got != "/tmp/photos" {
		t.Errorf("PhotosDir() = %q, want %q", got, "/tmp/photos")
	}
}

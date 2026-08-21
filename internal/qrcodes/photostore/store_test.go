// Тесты файлового хранилища фотографий кодов маркировки:
// сохранение QRCodes/<id>/photo.<ext>, листинг устаревших папок и удаление.
package photostore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testID — валидный id фото (16 hex-символов в нижнем регистре).
const testID = "0123456789abcdef"

// newTestStore создаёт хранилище в t.TempDir()/QRCodes и возвращает корень
// temp-директории и само хранилище.
func newTestStore(t *testing.T) (root string, s *Store) {
	t.Helper()
	root = t.TempDir()
	s, err := NewStore(filepath.Join(root, "QRCodes"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return root, s
}

func TestStoreSaveSuccess(t *testing.T) {
	root, s := newTestStore(t)

	want := []byte("фото-данные")
	if err := s.Save(context.Background(), testID, "jpg", bytes.NewReader(want)); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	path := filepath.Join(root, "QRCodes", testID, "photo.jpg")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл %s не создан: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("содержимое = %q, want %q", got, want)
	}
}

func TestStoreSaveEmptyReader(t *testing.T) {
	root, s := newTestStore(t)

	err := s.Save(context.Background(), testID, "jpg", bytes.NewReader(nil))
	if err == nil {
		t.Fatal("ожидалась ошибка для пустого reader")
	}
	if !strings.Contains(err.Error(), "файл пустой") {
		t.Errorf("ошибка = %q, want содержит «файл пустой»", err)
	}
	// Папка удалена после ошибки.
	if _, err := os.Stat(filepath.Join(root, "QRCodes", testID)); !os.IsNotExist(err) {
		t.Errorf("папка %s должна быть удалена после ошибки (err=%v)", testID, err)
	}
}

func TestStoreSaveInvalidIDAndExt(t *testing.T) {
	root, s := newTestStore(t)

	tests := []struct {
		name string
		id   string
		ext  string
	}{
		{name: "пустой id", id: "", ext: "jpg"},
		{name: "короткий id", id: "abc", ext: "jpg"},
		{name: "id в верхнем регистре", id: "0123456789ABCDEF", ext: "jpg"},
		{name: "слишком длинный id", id: "0123456789abcdef0", ext: "jpg"},
		{name: "id со спецсимволом", id: "0123456789abcde!", ext: "jpg"},
		{name: "пустое расширение", id: testID, ext: ""},
		{name: "расширение с точкой", id: testID, ext: ".jpg"},
		{name: "расширение со слешем", id: testID, ext: "jpg/png"},
		{name: "слишком длинное расширение", id: testID, ext: "verylongext"},
		{name: "расширение в верхнем регистре", id: testID, ext: "JPG"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Save(context.Background(), tt.id, tt.ext, bytes.NewReader([]byte("x")))
			if err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			// Валидация выполняется до создания папки: в хранилище
			// не должно появиться ничего (для пустого id путь сворачивается
			// к корню хранилища, поэтому проверяем именно корень).
			entries, err := os.ReadDir(filepath.Join(root, "QRCodes"))
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 0 {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("при невалидном id/ext созданы файлы: %v", names)
			}
		})
	}
}

func TestStoreSaveDuplicateID(t *testing.T) {
	_, s := newTestStore(t)

	if err := s.Save(context.Background(), testID, "jpg", bytes.NewReader([]byte("первое фото"))); err != nil {
		t.Fatalf("первый Save error: %v", err)
	}

	// Повторное сохранение того же id падает: файл создаётся с O_EXCL
	// и существующий не перезаписывается.
	err := s.Save(context.Background(), testID, "jpg", bytes.NewReader([]byte("дубль")))
	if err == nil {
		t.Fatal("ожидалась ошибка при повторном Save того же id")
	}
	if !strings.Contains(err.Error(), "file exists") {
		t.Errorf("ошибка = %q, want содержит «file exists» (O_EXCL)", err)
	}
}

func TestStoreListDirsOlderThan(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()

	oldID := "aaaaaaaaaaaaaaaa"
	newID := "bbbbbbbbbbbbbbbb"
	for _, id := range []string{oldID, newID} {
		if err := s.Save(ctx, id, "jpg", bytes.NewReader([]byte("фото"))); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}
	// Файл в корне хранилища — не папка, должен пропускаться.
	if err := os.WriteFile(filepath.Join(root, "QRCodes", "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Состариваем папку oldID (mtime = now - 2 часа).
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "QRCodes", oldID), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// cutoff = now - 1 час: под него попадает только oldID.
	cutoff := time.Now().Add(-time.Hour)
	dirs, err := s.ListDirsOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListDirsOlderThan error: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != oldID {
		t.Errorf("ListDirsOlderThan = %v, want [%s]", dirs, oldID)
	}
}

func TestStoreListDirsOlderThanMissingDir(t *testing.T) {
	_, s := newTestStore(t)

	// Несуществующая директория считается пустой (nil, nil).
	if err := os.RemoveAll(s.dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	dirs, err := s.ListDirsOlderThan(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ListDirsOlderThan error: %v", err)
	}
	if dirs != nil {
		t.Errorf("ListDirsOlderThan = %v, want nil", dirs)
	}
}

func TestStoreRemoveAll(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()

	if err := s.Save(ctx, testID, "jpg", bytes.NewReader([]byte("фото"))); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	if err := s.RemoveAll(ctx, testID); err != nil {
		t.Fatalf("RemoveAll error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", testID)); !os.IsNotExist(err) {
		t.Errorf("папка %s не удалена (err=%v)", testID, err)
	}

	// Повторное удаление отсутствующей папки — не ошибка.
	if err := s.RemoveAll(ctx, testID); err != nil {
		t.Errorf("RemoveAll отсутствующей папки: %v", err)
	}
}

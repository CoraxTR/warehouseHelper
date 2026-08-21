// Тесты файлового хранилища фотографий кодов маркировки:
// сохранение QRCodes/<id>.<ext>, листинг устаревших записей и удаление.
package photostore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testID — валидный id фото (16 hex-символов в нижнем регистре).
const testID = "0123456789abcdef"

// jpgData возвращает байты с JPEG magic (FF D8 FF E0): http.DetectContentType
// распознаёт их как image/jpeg, поэтому Save пропускает содержимое.
func jpgData(tail string) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(tail)...)
}

// heicData возвращает минимальный HEIC-заголовок: ftyp-бокс с брендом heic.
func heicData() []byte {
	return []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}
}

// newTestStore создаёт хранилище в t.TempDir()/QRCodes и возвращает корень
// temp-директории и само хранилище.
func newTestStore(t *testing.T) (root string, s *Store) {
	t.Helper()
	root = t.TempDir()
	s = NewStore(filepath.Join(root, "QRCodes"))
	return root, s
}

func TestStoreSaveSuccess(t *testing.T) {
	root, s := newTestStore(t)

	want := jpgData("фото-данные")
	if err := s.Save(context.Background(), testID, "jpg", bytes.NewReader(want)); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	path := filepath.Join(root, "QRCodes", testID+".jpg")
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
	// Файл не создан после ошибки.
	if _, err := os.Stat(filepath.Join(root, "QRCodes", testID+".jpg")); !os.IsNotExist(err) {
		t.Errorf("файл %s не должен существовать после ошибки (err=%v)", testID+".jpg", err)
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
			// Валидация выполняется до создания файла: в хранилище
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

	if err := s.Save(context.Background(), testID, "jpg", bytes.NewReader(jpgData("первое фото"))); err != nil {
		t.Fatalf("первый Save error: %v", err)
	}

	// Повторное сохранение того же id падает: файл создаётся с O_EXCL
	// и существующий не перезаписывается.
	err := s.Save(context.Background(), testID, "jpg", bytes.NewReader(jpgData("дубль")))
	if err == nil {
		t.Fatal("ожидалась ошибка при повторном Save того же id")
	}
	if !strings.Contains(err.Error(), "file exists") {
		t.Errorf("ошибка = %q, want содержит «file exists» (O_EXCL)", err)
	}
}

func TestStoreSaveRejectsNonImage(t *testing.T) {
	root, s := newTestStore(t)

	// HTML-файл под видом фото не должен сохраняться (защита от stored XSS
	// через раздачу фото): содержимое проверяется по magic bytes.
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")
	err := s.Save(context.Background(), testID, "jpg", bytes.NewReader(html))
	if err == nil {
		t.Fatal("ожидалась ошибка для содержимого не-изображения")
	}
	if !strings.Contains(err.Error(), "не похоже на изображение") {
		t.Errorf("ошибка = %q, want содержит «не похоже на изображение»", err)
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", testID+".jpg")); !os.IsNotExist(err) {
		t.Errorf("файл %s не должен существовать после ошибки (err=%v)", testID+".jpg", err)
	}
}

func TestStoreSaveAcceptsHEIC(t *testing.T) {
	root, s := newTestStore(t)

	// HEIC Go не распознаёт через DetectContentType, но это валидное фото
	// с iPhone: ftyp-бокс с брендом heic должен приниматься.
	if err := s.Save(context.Background(), testID, "heic", bytes.NewReader(heicData())); err != nil {
		t.Fatalf("Save HEIC error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", testID+".heic")); err != nil {
		t.Errorf("файл %s.heic не создан: %v", testID, err)
	}
}

func TestStoreListOlderThan(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()

	oldID := "aaaaaaaaaaaaaaaa"    // файл новой схемы, будет состарен
	newID := "bbbbbbbbbbbbbbbb"    // файл новой схемы, свежий
	oldDirID := "cccccccccccccccc" // папка старой схемы, будет состарена
	for _, id := range []string{oldID, newID} {
		if err := s.Save(ctx, id, "jpg", bytes.NewReader(jpgData("фото"))); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}
	// Папка старой схемы: <id>/photo.<ext> — должна тоже попасть в список,
	// чтобы дочиститься после перехода на плоские файлы.
	oldDir := filepath.Join(root, "QRCodes", oldDirID)
	if err := os.MkdirAll(oldDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "photo.jpg"), jpgData("старое фото"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Посторонний файл в корне хранилища должен пропускаться.
	if err := os.WriteFile(filepath.Join(root, "QRCodes", "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Состариваем файл oldID и папку oldDirID (mtime = now - 2 часа).
	oldTime := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{
		filepath.Join(root, "QRCodes", oldID+".jpg"),
		filepath.Join(root, "QRCodes", oldDirID),
	} {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatalf("Chtimes(%s): %v", p, err)
		}
	}

	// cutoff = now - 1 час: под него попадают только oldID и oldDirID.
	cutoff := time.Now().Add(-time.Hour)
	names, err := s.ListOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListOlderThan error: %v", err)
	}
	want := []string{oldID + ".jpg", oldDirID}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("ListOlderThan = %v, want %v", names, want)
	}
}

func TestStoreListOlderThanMissingDir(t *testing.T) {
	_, s := newTestStore(t)

	// Несуществующая директория считается пустой (nil, nil).
	if err := os.RemoveAll(s.dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	names, err := s.ListOlderThan(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ListOlderThan error: %v", err)
	}
	if names != nil {
		t.Errorf("ListOlderThan = %v, want nil", names)
	}
}

func TestStoreRemoveAll(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()

	if err := s.Save(ctx, testID, "jpg", bytes.NewReader(jpgData("фото"))); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Файл новой схемы удаляется по полному имени <id>.<ext>.
	if err := s.RemoveAll(ctx, testID+".jpg"); err != nil {
		t.Fatalf("RemoveAll error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", testID+".jpg")); !os.IsNotExist(err) {
		t.Errorf("файл %s не удалён (err=%v)", testID+".jpg", err)
	}

	// Папка старой схемы удаляется по имени <id> (без расширения).
	oldDir := filepath.Join(root, "QRCodes", testID)
	if err := os.MkdirAll(oldDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "photo.jpg"), jpgData("старое"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := s.RemoveAll(ctx, testID); err != nil {
		t.Fatalf("RemoveAll(папка старой схемы): %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("папка %s не удалена (err=%v)", testID, err)
	}

	// Повторное удаление отсутствующей записи — не ошибка.
	if err := s.RemoveAll(ctx, testID+".jpg"); err != nil {
		t.Errorf("RemoveAll отсутствующей записи: %v", err)
	}
}

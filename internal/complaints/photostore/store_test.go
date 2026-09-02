// Тесты zip-хранилища фотографий жалоб: добавление <id>.<ext> в архив
// <complaintID>.zip, листинг в порядке добавления, чтение, удаление и
// изоляция разных жалоб.
package photostore

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Валидные id фото (16 hex-символов в нижнем регистре).
const (
	idA = "0123456789abcdef"
	idB = "1234567890abcdef"
	idC = "abcdef0123456789"
	idD = "fedcba9876543210"
)

// jpgData возвращает байты с JPEG magic (FF D8 FF E0): http.DetectContentType
// распознаёт их как image/jpeg, поэтому Add пропускает содержимое.
func jpgData(tail string) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(tail)...)
}

// newTestStore создаёт хранилище в t.TempDir()/complaints и возвращает корень
// temp-директории и само хранилище.
func newTestStore(t *testing.T) (root string, s *Store) {
	t.Helper()
	root = t.TempDir()
	s = NewStore(filepath.Join(root, "complaints"))
	return root, s
}

// complaintArchivePath — путь к файлу архива жалобы на диске.
func complaintArchivePath(root string, complaintID int64) string {
	return filepath.Join(root, "complaints", fmt.Sprintf("%d.zip", complaintID))
}

// readAll читает весь ReadCloser и закрывает его.
func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return b
}

// archiveEntryModified возвращает Modified записи name в zip-архиве path.
func archiveEntryModified(t *testing.T, path, name string) time.Time {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("zip.OpenReader(%s): %v", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			return f.Modified
		}
	}
	t.Fatalf("запись %s не найдена в архиве %s", name, path)
	return time.Time{}
}

func TestStoreAddListOpen(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()
	const cid int64 = 7

	// Тестовые данные обязаны распознаваться как изображение, иначе Add
	// отклонит их по magic bytes.
	if ct := http.DetectContentType(jpgData("x")); !strings.HasPrefix(ct, "image/jpeg") {
		t.Fatalf("DetectContentType(jpgData) = %q, want image/jpeg", ct)
	}

	orig := map[string][]byte{
		idA + ".jpg": jpgData("первое-фото"),
		idB + ".jpg": jpgData("второе-фото"),
	}
	ups := []Upload{
		{ID: idA, Ext: "jpg", Data: bytes.NewReader(orig[idA+".jpg"])},
		{ID: idB, Ext: "jpg", Data: bytes.NewReader(orig[idB+".jpg"])},
	}
	if err := s.Add(ctx, cid, ups); err != nil {
		t.Fatalf("Add error: %v", err)
	}

	// Архив <complaintID>.zip создан на диске.
	path := complaintArchivePath(root, cid)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("архив %s не создан: %v", path, err)
	}

	// List возвращает файлы в порядке добавления.
	got, err := s.List(ctx, cid)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	want := []Photo{
		{Name: idA + ".jpg", ID: idA, Ext: "jpg", Size: int64(len(orig[idA+".jpg"]))},
		{Name: idB + ".jpg", ID: idB, Ext: "jpg", Size: int64(len(orig[idB+".jpg"]))},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}

	// Open отдаёт те же байты, что были загружены.
	for name, wantData := range orig {
		rc, err := s.Open(ctx, cid, name)
		if err != nil {
			t.Fatalf("Open(%s) error: %v", name, err)
		}
		if data := readAll(t, rc); !bytes.Equal(data, wantData) {
			t.Errorf("Open(%s) = %q, want %q", name, data, wantData)
		}
	}
}

func TestStoreAddInvalid(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()
	const cid int64 = 7

	tests := []struct {
		name string
		id   string
		ext  string
		data []byte
		// wantErr — подстрока сообщения об ошибке; пусто — достаточно любой ошибки.
		wantErr string
	}{
		{name: "пустой id", id: "", ext: "jpg", data: jpgData("x")},
		{name: "короткий id", id: "abc", ext: "jpg", data: jpgData("x")},
		{name: "id в верхнем регистре", id: "0123456789ABCDEF", ext: "jpg", data: jpgData("x")},
		{name: "слишком длинный id", id: "0123456789abcdef0", ext: "jpg", data: jpgData("x")},
		{name: "id со спецсимволом", id: "0123456789abcde!", ext: "jpg", data: jpgData("x")},
		{name: "пустое расширение", id: idA, ext: "", data: jpgData("x")},
		{name: "расширение с точкой", id: idA, ext: ".jpg", data: jpgData("x")},
		{name: "расширение со слешем", id: idA, ext: "jpg/png", data: jpgData("x")},
		{name: "слишком длинное расширение", id: idA, ext: "verylongext", data: jpgData("x")},
		{name: "расширение в верхнем регистре", id: idA, ext: "JPG", data: jpgData("x")},
		{name: "пустой файл", id: idA, ext: "jpg", data: nil, wantErr: "пустой"},
		{name: "не изображение", id: idA, ext: "jpg", data: []byte("<!DOCTYPE html><html><script>alert(1)</script></html>"), wantErr: "не похоже на изображение"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Add(ctx, cid, []Upload{{ID: tt.id, Ext: tt.ext, Data: bytes.NewReader(tt.data)}})
			if err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ошибка = %q, want содержит %q", err, tt.wantErr)
			}
			// Валидация выполняется до создания архива: на диске ничего нет.
			if _, err := os.Stat(complaintArchivePath(root, cid)); !os.IsNotExist(err) {
				t.Errorf("архив не должен существовать после ошибки (err=%v)", err)
			}
		})
	}

	// Пустой список загрузок — ошибка, архив не создаётся.
	for _, ups := range [][]Upload{nil, {}} {
		if err := s.Add(ctx, cid, ups); err == nil {
			t.Fatal("ожидалась ошибка для пустого списка Upload")
		}
	}
	if _, err := os.Stat(complaintArchivePath(root, cid)); !os.IsNotExist(err) {
		t.Errorf("архив не должен существовать после ошибки (err=%v)", err)
	}
}

func TestStoreAddAppendKeepsOldFirst(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()
	const cid int64 = 7

	orig := map[string][]byte{
		idA + ".jpg": jpgData("первое-фото"),
		idB + ".jpg": jpgData("второе-фото"),
		idC + ".jpg": jpgData("третье-фото"),
		idD + ".jpg": jpgData("четвёртое-фото"),
	}
	first := []Upload{
		{ID: idA, Ext: "jpg", Data: bytes.NewReader(orig[idA+".jpg"])},
		{ID: idB, Ext: "jpg", Data: bytes.NewReader(orig[idB+".jpg"])},
	}
	if err := s.Add(ctx, cid, first); err != nil {
		t.Fatalf("первый Add error: %v", err)
	}
	// Время записи idA до пересборки: после добавления второй пачки оно
	// должно сохраниться (пересборка копирует FileHeader.Modified).
	path := complaintArchivePath(root, cid)
	modBefore := archiveEntryModified(t, path, idA+".jpg")

	second := []Upload{
		{ID: idC, Ext: "jpg", Data: bytes.NewReader(orig[idC+".jpg"])},
		{ID: idD, Ext: "jpg", Data: bytes.NewReader(orig[idD+".jpg"])},
	}
	if err := s.Add(ctx, cid, second); err != nil {
		t.Fatalf("второй Add error: %v", err)
	}

	// Старые файлы сохранились и остались первыми, новые дописаны следом.
	got, err := s.List(ctx, cid)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	want := []Photo{
		{Name: idA + ".jpg", ID: idA, Ext: "jpg", Size: int64(len(orig[idA+".jpg"]))},
		{Name: idB + ".jpg", ID: idB, Ext: "jpg", Size: int64(len(orig[idB+".jpg"]))},
		{Name: idC + ".jpg", ID: idC, Ext: "jpg", Size: int64(len(orig[idC+".jpg"]))},
		{Name: idD + ".jpg", ID: idD, Ext: "jpg", Size: int64(len(orig[idD+".jpg"]))},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}

	// Содержимое всех файлов (включая переписанные старые) не изменилось.
	for name, wantData := range orig {
		rc, err := s.Open(ctx, cid, name)
		if err != nil {
			t.Fatalf("Open(%s) error: %v", name, err)
		}
		if data := readAll(t, rc); !bytes.Equal(data, wantData) {
			t.Errorf("Open(%s) = %q, want %q", name, data, wantData)
		}
	}

	// Время старой записи сохранилось при пересборке архива.
	if modAfter := archiveEntryModified(t, path, idA+".jpg"); !modAfter.Equal(modBefore) {
		t.Errorf("Modified записи %s изменился при пересборке: было %v, стало %v", idA+".jpg", modBefore, modAfter)
	}
}

func TestStoreRemove(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()
	const cid int64 = 7

	orig := map[string][]byte{
		idA + ".jpg": jpgData("первое-фото"),
		idB + ".jpg": jpgData("второе-фото"),
	}
	for name, data := range orig {
		id, ext, _ := strings.Cut(name, ".")
		if err := s.Add(ctx, cid, []Upload{{ID: id, Ext: ext, Data: bytes.NewReader(data)}}); err != nil {
			t.Fatalf("Add(%s) error: %v", name, err)
		}
	}

	// Remove убирает запись, вторая остаётся.
	if err := s.Remove(ctx, cid, idA+".jpg"); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	got, err := s.List(ctx, cid)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	want := []Photo{{Name: idB + ".jpg", ID: idB, Ext: "jpg", Size: int64(len(orig[idB+".jpg"]))}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}

	// Повторный Remove того же имени — не ошибка (идемпотентно).
	if err := s.Remove(ctx, cid, idA+".jpg"); err != nil {
		t.Errorf("повторный Remove: %v", err)
	}
	// Remove отсутствующей, но валидной записи — тоже не ошибка.
	if err := s.Remove(ctx, cid, idC+".jpg"); err != nil {
		t.Errorf("Remove отсутствующей записи: %v", err)
	}
	// Имя с обходом пути не проходит fileRe — ошибка.
	if err := s.Remove(ctx, cid, "../secret.jpg"); err == nil {
		t.Error("ожидалась ошибка для имени с обходом пути")
	}

	// Удаление последней фотографии убирает архив целиком.
	if err := s.Remove(ctx, cid, idB+".jpg"); err != nil {
		t.Fatalf("Remove последней фотографии: %v", err)
	}
	if _, err := os.Stat(complaintArchivePath(root, cid)); !os.IsNotExist(err) {
		t.Errorf("пустой архив должен быть удалён (err=%v)", err)
	}
	if empty, err := s.List(ctx, cid); err != nil || len(empty) != 0 {
		t.Errorf("List после удаления последней фотографии = %v, %v; want пусто", empty, err)
	}
	// Remove на удалённом архиве — не ошибка.
	if err := s.Remove(ctx, cid, idB+".jpg"); err != nil {
		t.Errorf("Remove на удалённом архиве: %v", err)
	}
}

func TestStoreListMissingComplaint(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	// Жалобы нет — пустой список и nil-ошибка; Remove тоже идемпотентен.
	photos, err := s.List(ctx, 12345)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(photos) != 0 {
		t.Errorf("List = %v, want пусто", photos)
	}
	if err := s.Remove(ctx, 12345, idA+".jpg"); err != nil {
		t.Errorf("Remove несуществующей жалобы: %v", err)
	}
}

func TestStoreOpenPathTraversal(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()
	const cid int64 = 7

	if err := s.Add(ctx, cid, []Upload{{ID: idA, Ext: "jpg", Data: bytes.NewReader(jpgData("фото"))}}); err != nil {
		t.Fatalf("Add error: %v", err)
	}

	// Имя с обходом пути не проходит fileRe — ошибка ещё до чтения архива.
	if rc, err := s.Open(ctx, cid, "../secret.jpg"); err == nil {
		_ = rc.Close()
		t.Fatal("ожидалась ошибка для имени с обходом пути")
	}
	// Архива жалобы нет — ошибка.
	if _, err := s.Open(ctx, 999, idA+".jpg"); err == nil {
		t.Fatal("ожидалась ошибка для несуществующей жалобы")
	}
	// Валидное имя, но записи в архиве нет — ошибка с пояснением.
	if _, err := s.Open(ctx, cid, idB+".jpg"); err == nil {
		t.Fatal("ожидалась ошибка для отсутствующей записи")
	} else if !strings.Contains(err.Error(), "не найдена") {
		t.Errorf("ошибка = %q, want содержит «не найдена»", err)
	}
}

func TestStoreComplaintsIsolated(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()

	// Разные жалобы живут в разных архивах и не смешиваются.
	firstData := jpgData("фото-жалобы-1")
	secondData := jpgData("фото-жалобы-2")
	if err := s.Add(ctx, 1, []Upload{{ID: idA, Ext: "jpg", Data: bytes.NewReader(firstData)}}); err != nil {
		t.Fatalf("Add(1) error: %v", err)
	}
	if err := s.Add(ctx, 2, []Upload{{ID: idB, Ext: "jpg", Data: bytes.NewReader(secondData)}}); err != nil {
		t.Fatalf("Add(2) error: %v", err)
	}

	checkList := func(cid int64, wantID string) {
		t.Helper()
		got, err := s.List(ctx, cid)
		if err != nil {
			t.Fatalf("List(%d) error: %v", cid, err)
		}
		want := []Photo{{Name: wantID + ".jpg", ID: wantID, Ext: "jpg", Size: int64(len(firstData))}}
		if wantID == idB {
			want[0].Size = int64(len(secondData))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("List(%d) = %+v, want %+v", cid, got, want)
		}
	}
	checkList(1, idA)
	checkList(2, idB)

	// Фото второй жалобы не отдаётся из архива первой.
	if _, err := s.Open(ctx, 1, idB+".jpg"); err == nil {
		t.Fatal("ожидалась ошибка: файлы разных жалоб не должны смешиваться")
	}

	// Удаление из одной жалобы не трогает архив другой.
	if err := s.Remove(ctx, 1, idA+".jpg"); err != nil {
		t.Fatalf("Remove(1) error: %v", err)
	}
	if _, err := os.Stat(complaintArchivePath(root, 1)); !os.IsNotExist(err) {
		t.Errorf("архив жалобы 1 должен быть удалён (err=%v)", err)
	}
	checkList(2, idB)
}

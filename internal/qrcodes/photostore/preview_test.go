// Тесты уменьшенных копий фото кодов маркировки: генерация при сохранении,
// до-генерация по запросу (EnsurePreview) для старых фото, EXIF-ориентация,
// удаление и листинг копий.
package photostore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// realJPEG возвращает настоящий JPEG w x h (декодируется Go). Изображение
// чёрное — для тестов важны размеры и декодируемость, а не содержимое.
func realJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

// withOrientation встраивает в JPEG APP1-сегмент EXIF с ориентацией orient
// (тег 0x0112 в IFD0, little-endian TIFF).
func withOrientation(t *testing.T, base []byte, orient uint16) []byte {
	t.Helper()
	if len(base) < 2 || base[0] != 0xFF || base[1] != 0xD8 {
		t.Fatal("ожидался JPEG с SOI")
	}
	// IFD0 из одной записи: заголовок TIFF (8) + счётчик записей (2) +
	// запись (12) + указатель следующего IFD (4) = 26 байт. Длина сегмента
	// APP1 (2 + «Exif\0\0» + 26) собирается без int-конверсий (их метит
	// gosec G115), а литерал длины в make — чтобы gosec видел границы слайса.
	tiff := make([]byte, 26)
	tiff[0] = 'I'
	tiff[1] = 'I'
	binary.LittleEndian.PutUint16(tiff[2:4], 42)
	binary.LittleEndian.PutUint32(tiff[4:8], 8) // IFD0 сразу после заголовка
	binary.LittleEndian.PutUint16(tiff[8:10], 1)
	binary.LittleEndian.PutUint16(tiff[10:12], 0x0112) // Orientation
	binary.LittleEndian.PutUint16(tiff[12:14], 3)      // SHORT
	binary.LittleEndian.PutUint32(tiff[14:18], 1)
	binary.LittleEndian.PutUint16(tiff[18:20], orient)

	// APP1: маркер FF E1 + длина сегмента (2 байта BE, включает себя и
	// payload «Exif\0\0» + TIFF).
	const exifHeader = 6 // «Exif\0\0»
	seg := make([]byte, 0, 4+exifHeader+26)
	seg = append(seg, 0xFF, 0xE1, 0x00, 0x00)
	binary.BigEndian.PutUint16(seg[2:4], 2+exifHeader+26)
	seg = append(seg, "Exif\x00\x00"...)
	seg = append(seg, tiff...)

	out := append([]byte{}, base[:2]...)
	out = append(out, seg...)
	return append(out, base[2:]...)
}

// jpegDims декодирует JPEG по пути и возвращает его размеры.
func jpegDims(t *testing.T, path string) (w, h int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("открытие %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("декодирование %s: %v", path, err)
	}
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

// writeOriginal кладёт файл-оригинал testID.<ext> в хранилище напрямую (без
// Save — имитация фото, сохранённого до появления уменьшенных копий).
func writeOriginal(t *testing.T, dir, ext string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, testID+"."+ext), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// secondID — второй валидный id фото для тестов, где нужны две записи.
const secondID = "fedcba9876543210"

// TestMain глушит логи в тестах пакета: сгенерированные INFO-сообщения о
// пропуске копий (фейковые jpg в старых тестах, HEIC) засоряют вывод.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	m.Run()
}

func TestStoreSaveGeneratesPreviews(t *testing.T) {
	root, s := newTestStore(t)

	if err := s.Save(context.Background(), testID, "jpg", bytes.NewReader(realJPEG(t, 1000, 750))); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Оригинал не тронут, рядом появились обе копии.
	orig := filepath.Join(root, "QRCodes", testID+".jpg")
	if _, err := os.Stat(orig); err != nil {
		t.Fatalf("оригинал %s не создан: %v", orig, err)
	}
	thumb := PreviewPath(s.dir, ThumbKind, testID)
	view := PreviewPath(s.dir, ViewKind, testID)
	for _, p := range []string{thumb, view} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("копия %s не создана: %v", p, err)
		}
	}

	// Превью уменьшено до 480 по длинной стороне; копия просмотра не
	// увеличивает изображение меньше 1920.
	if w, h := jpegDims(t, thumb); w != 480 || h != 360 {
		t.Errorf("thumb = %dx%d, want 480x360", w, h)
	}
	if w, h := jpegDims(t, view); w != 1000 || h != 750 {
		t.Errorf("view = %dx%d, want 1000x750 (без увеличения)", w, h)
	}
}

func TestStoreSaveSkipsPreviewsWhenUndecodable(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	// HEIC: магия проходит проверку содержимого, но декодера нет —
	// сохранение успешно, копий нет (раздача отдаст оригинал).
	if err := s.Save(ctx, testID, "heic", bytes.NewReader(heicData())); err != nil {
		t.Fatalf("Save HEIC error: %v", err)
	}
	// Фейковый jpg: магия есть, декодировать нечего.
	if err := s.Save(ctx, secondID, "jpg", bytes.NewReader(jpgData("фото"))); err != nil {
		t.Fatalf("Save fake jpg error: %v", err)
	}

	for _, id := range []string{testID, secondID} {
		for _, kind := range previewKindsOrder {
			if _, err := os.Stat(PreviewPath(s.dir, kind, id)); !os.IsNotExist(err) {
				t.Errorf("копия %s/%s не должна существовать (err=%v)", kind, id, err)
			}
		}
	}
}

func TestEnsurePreviewCreatesFromExistingOriginal(t *testing.T) {
	root, s := newTestStore(t)

	writeOriginal(t, filepath.Join(root, "QRCodes"), "jpg", realJPEG(t, 640, 480))

	path, err := EnsurePreview(context.Background(), s.dir, ThumbKind, testID)
	if err != nil {
		t.Fatalf("EnsurePreview error: %v", err)
	}
	if path != PreviewPath(s.dir, ThumbKind, testID) {
		t.Errorf("EnsurePreview = %q, want %q", path, PreviewPath(s.dir, ThumbKind, testID))
	}
	if w, h := jpegDims(t, path); w != 480 || h != 360 {
		t.Errorf("thumb = %dx%d, want 480x360", w, h)
	}

	// Повторный вызов не перегенерирует, а возвращает существующий файл.
	again, err := EnsurePreview(context.Background(), s.dir, ThumbKind, testID)
	if err != nil {
		t.Fatalf("EnsurePreview повторно error: %v", err)
	}
	if again != path {
		t.Errorf("повторный EnsurePreview = %q, want %q", again, path)
	}
}

func TestEnsurePreviewOldSchemeDir(t *testing.T) {
	root, s := newTestStore(t)

	// Оригинал старой схемы: папка <id>/photo.jpg.
	oldDir := filepath.Join(root, "QRCodes", testID)
	if err := os.MkdirAll(oldDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "photo.jpg"), realJPEG(t, 800, 600), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	path, err := EnsurePreview(context.Background(), s.dir, ThumbKind, testID)
	if err != nil {
		t.Fatalf("EnsurePreview error: %v", err)
	}
	if w, h := jpegDims(t, path); w != 480 || h != 360 {
		t.Errorf("thumb = %dx%d, want 480x360", w, h)
	}
}

func TestEnsurePreviewErrors(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()

	if _, err := EnsurePreview(ctx, s.dir, ThumbKind, testID); err == nil {
		t.Error("ожидалась ошибка для отсутствующего оригинала")
	}
	if _, err := EnsurePreview(ctx, s.dir, "full", testID); err == nil {
		t.Error("ожидалась ошибка для неизвестного вида копии")
	}
	if _, err := EnsurePreview(ctx, s.dir, ThumbKind, "../x"); err == nil {
		t.Error("ожидалась ошибка для недопустимого id (path traversal)")
	}

	// Оригинал HEIC: копию не построить.
	writeOriginal(t, filepath.Join(root, "QRCodes"), "heic", heicData())
	if _, err := EnsurePreview(ctx, s.dir, ThumbKind, testID); !errors.Is(err, ErrPreviewUnsupported) {
		t.Errorf("ошибка = %v, want ErrPreviewUnsupported", err)
	}
}

func TestEnsurePreviewRespectsEXIFOrientation(t *testing.T) {
	root, s := newTestStore(t)

	// Ориентация 6 (90° по часовой): 80x40 → 40x80; копия должна учесть поворот.
	jpg := withOrientation(t, realJPEG(t, 80, 40), 6)
	writeOriginal(t, filepath.Join(root, "QRCodes"), "jpg", jpg)

	path, err := EnsurePreview(context.Background(), s.dir, ThumbKind, testID)
	if err != nil {
		t.Fatalf("EnsurePreview error: %v", err)
	}
	if w, h := jpegDims(t, path); w != 40 || h != 80 {
		t.Errorf("thumb = %dx%d, want 40x80 (EXIF-поворот применён)", w, h)
	}
}

func TestDecodeOrientedFormats(t *testing.T) {
	root, _ := newTestStore(t)
	dir := filepath.Join(root, "QRCodes")

	// PNG.
	pngImg := image.NewRGBA(image.Rect(0, 0, 30, 20))
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, pngImg); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	writeOriginal(t, dir, "png", pngBuf.Bytes())
	img, err := decodeOriented(filepath.Join(dir, testID+".png"), "png")
	if err != nil {
		t.Fatalf("decodeOriented png: %v", err)
	}
	if img.Bounds().Dx() != 30 || img.Bounds().Dy() != 20 {
		t.Errorf("png = %v, want 30x20", img.Bounds())
	}

	// WebP: декодер x/image/webp (энкодера в тесте нет — ветка decodeOriented
	// для webp тонкая, ошибки декодирования покрыты тестом ниже).
	if _, err := decodeOriented(filepath.Join(dir, secondID+".jpg"), "webp"); err == nil {
		t.Error("ожидалась ошибка декодирования для несуществующего файла webp")
	}

	// Неизвестный формат — ErrPreviewUnsupported (файл существует: png выше).
	if _, err := decodeOriented(filepath.Join(dir, testID+".png"), "heic"); !errors.Is(err, ErrPreviewUnsupported) {
		t.Errorf("ошибка = %v, want ErrPreviewUnsupported", err)
	}
}

func TestJPEGOrientation(t *testing.T) {
	_, s := newTestStore(t)
	dir := filepath.Join(s.dir, "exif-test")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	plain := filepath.Join(dir, "plain.jpg")
	if err := os.WriteFile(plain, realJPEG(t, 10, 10), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := jpegOrientation(plain); got != 1 {
		t.Errorf("jpegOrientation(без EXIF) = %d, want 1", got)
	}

	tests := []struct {
		name   string
		orient uint16
		want   int
	}{
		{name: "ориентация 3", orient: 3, want: 3},
		{name: "ориентация 6", orient: 6, want: 6},
		{name: "ориентация 8", orient: 8, want: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, tt.name+".jpg")
			if err := os.WriteFile(p, withOrientation(t, realJPEG(t, 10, 10), tt.orient), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if got := jpegOrientation(p); got != tt.want {
				t.Errorf("jpegOrientation = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRemoveAllDeletesPreviews(t *testing.T) {
	root, s := newTestStore(t)
	ctx := context.Background()

	if err := s.Save(ctx, testID, "jpg", bytes.NewReader(realJPEG(t, 300, 200))); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Удаление имени копии убирает только её.
	if err := s.RemoveAll(ctx, ThumbKind+"/"+testID+".jpg"); err != nil {
		t.Fatalf("RemoveAll(копия) error: %v", err)
	}
	if _, err := os.Stat(PreviewPath(s.dir, ThumbKind, testID)); !os.IsNotExist(err) {
		t.Errorf("копия thumbs не удалена (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", testID+".jpg")); err != nil {
		t.Errorf("оригинал удалён вместе с копией (err=%v)", err)
	}

	// Удаление оригинала убирает и обе копии.
	if err := s.Save(ctx, secondID, "jpg", bytes.NewReader(realJPEG(t, 300, 200))); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if err := s.RemoveAll(ctx, secondID+".jpg"); err != nil {
		t.Fatalf("RemoveAll(оригинал) error: %v", err)
	}
	for _, kind := range previewKindsOrder {
		if _, err := os.Stat(PreviewPath(s.dir, kind, secondID)); !os.IsNotExist(err) {
			t.Errorf("копия %s/%s не удалена (err=%v)", kind, secondID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "QRCodes", secondID+".jpg")); !os.IsNotExist(err) {
		t.Errorf("оригинал %s не удалён (err=%v)", secondID, err)
	}
}

func TestListOlderThanIncludesPreviews(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	oldID := "aaaaaaaaaaaaaaaa"
	if err := s.Save(ctx, oldID, "jpg", bytes.NewReader(realJPEG(t, 300, 200))); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	freshID := "bbbbbbbbbbbbbbbb"
	if err := s.Save(ctx, freshID, "jpg", bytes.NewReader(realJPEG(t, 300, 200))); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Старим оригинал и обе его копии (mtime = now - 2 часа).
	oldTime := time.Now().Add(-2 * time.Hour)
	paths := []string{
		PreviewPath(s.dir, ThumbKind, oldID),
		PreviewPath(s.dir, ViewKind, oldID),
	}
	// В корне файлы идут раньше папок копий по алфавиту, порядок ожиданий
	// зависит только от содержимого; состарим и сам оригинал.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == oldID+".jpg" {
			paths = append(paths, filepath.Join(s.dir, e.Name()))
		}
	}
	for _, p := range paths {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatalf("Chtimes(%s): %v", p, err)
		}
	}

	cutoff := time.Now().Add(-time.Hour)
	names, err := s.ListOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListOlderThan error: %v", err)
	}
	want := []string{
		oldID + ".jpg",
		ThumbKind + "/" + oldID + ".jpg",
		ViewKind + "/" + oldID + ".jpg",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("ListOlderThan = %v, want %v", names, want)
	}
}

func TestListOlderThanPicksOrphanPreview(t *testing.T) {
	_, s := newTestStore(t)

	// Сирота: копия без оригинала, старше cutoff — должна попасть в список
	// и удалиться следующим проходом очистки.
	orphanID := "deadbeefdeadbeef"
	orphan := PreviewPath(s.dir, ThumbKind, orphanID)
	if err := os.MkdirAll(filepath.Dir(orphan), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(orphan, realJPEG(t, 10, 10), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(orphan, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	names, err := s.ListOlderThan(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListOlderThan error: %v", err)
	}
	want := []string{ThumbKind + "/" + orphanID + ".jpg"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("ListOlderThan = %v, want %v", names, want)
	}
}

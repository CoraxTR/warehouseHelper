package photostore

import (
	"context"
	"errors"
	"fmt"
	"image"
	stddraw "image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Уменьшенные копии фотографий: оригинал (<id>.<ext>) не трогается, рядом
// хранятся JPEG-копии в подпапках — для списка заказов (thumbs) и для
// полноэкранного просмотра (view). Страницы грузят только копии: полный
// оригинал по Wi-Fi склада не отдаётся, список открывается за секунды.

// Имена подпапок уменьшенных копий и максимальная длинная сторона копии.
const (
	ThumbKind = "thumbs" // копии для списка заказов (превью)
	ViewKind  = "view"   // копии для просмотра на мониторе (код считывают с экрана)
)

const (
	ThumbMaxSide = 480  // длинная сторона превью, px
	ViewMaxSide  = 1920 // длинная сторона копии просмотра, px
)

// Качество JPEG копий: превью списка мельче и легче, копия просмотра выше —
// с неё считывают код маркировки.
const (
	thumbJPEGQuality = 82
	viewJPEGQuality  = 85
)

// previewKindsOrder — порядок генерации и обхода копий: детерминированный,
// чтобы листинг при очистке и тесты не зависели от порядка в map.
var previewKindsOrder = []string{ThumbKind, ViewKind}

// previewFileRe — имя файла уменьшенной копии: <id>.jpg.
var previewFileRe = regexp.MustCompile(`^[a-f0-9]{16}\.jpg$`)

// ErrPreviewUnsupported — оригинал в формате, который Go не декодирует
// (HEIC/HEIF): уменьшенную копию не построить, раздача отдаст оригинал.
var ErrPreviewUnsupported = errors.New("формат оригинала не поддерживает уменьшенные копии")

// spec — параметры копии одного вида.
type spec struct {
	maxSide int
	quality int
}

func specFor(kind string) (spec, error) {
	switch kind {
	case ThumbKind:
		return spec{maxSide: ThumbMaxSide, quality: thumbJPEGQuality}, nil
	case ViewKind:
		return spec{maxSide: ViewMaxSide, quality: viewJPEGQuality}, nil
	default:
		return spec{}, fmt.Errorf("photostore: неизвестный вид копии %q", kind)
	}
}

// PreviewPath возвращает путь к уменьшенной копии <dir>/<kind>/<id>.jpg.
func PreviewPath(dir, kind, id string) string {
	return filepath.Join(dir, kind, id+".jpg")
}

// Original возвращает путь к файлу-оригиналу фото и его расширение:
// <dir>/<id>.<ext> новой схемы или <dir>/<id>/photo.<ext> старой. Ошибка —
// оригинал не найден.
func Original(dir, id string) (string, string, error) {
	if !idRe.MatchString(id) {
		return "", "", fmt.Errorf("photostore: недопустимый id фото %q", id)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", fmt.Errorf("photostore: поиск оригинала %s: %w", id, err)
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && fileRe.MatchString(name) && strings.HasPrefix(name, id+".") {
			return filepath.Join(dir, name), extFromName(name), nil
		}
	}
	// Старая схема: папка <id>/photo.<ext>.
	if entries, err := os.ReadDir(filepath.Join(dir, id)); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() && strings.HasPrefix(name, "photo.") {
				return filepath.Join(dir, id, name), extFromName(name), nil
			}
		}
	}
	return "", "", fmt.Errorf("photostore: оригинал фото %s не найден", id)
}

// extFromName извлекает расширение без точки из имени файла.
func extFromName(name string) string {
	return name[strings.LastIndexByte(name, '.')+1:]
}

// EnsurePreview возвращает путь к уменьшенной копии <kind>/<id>.jpg, создавая
// её из оригинала при первом запросе — так до-генерируются копии для фото,
// сохранённых до появления этой функции, без отдельной миграции. Копия всегда
// JPEG; если оригинал не декодируется (HEIC/HEIF) или не найден, возвращается
// ошибка, и раздача отдаст оригинал как раньше. Файл пишется атомарно
// (временный + rename): параллельные запросы одной копии безопасны.
func EnsurePreview(ctx context.Context, dir, kind, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := specFor(kind); err != nil {
		return "", err
	}
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("photostore: недопустимый id фото %q", id)
	}
	path := PreviewPath(dir, kind, id)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	origPath, ext, err := Original(dir, id)
	if err != nil {
		return "", err
	}
	img, err := decodeOriented(origPath, ext)
	if err != nil {
		return "", err
	}
	if err := writePreview(ctx, img, path, kind); err != nil {
		return "", err
	}
	return path, nil
}

// writePreviews генерирует все уменьшенные копии оригинала (вызывается после
// успешного сохранения файла). Ошибка не откатывает сохранение: копий нет —
// раздача отдаст оригинал.
func (s *Store) writePreviews(ctx context.Context, id, ext, origPath string) error {
	img, err := decodeOriented(origPath, ext)
	if err != nil {
		return err
	}
	for _, kind := range previewKindsOrder {
		if err := writePreview(ctx, img, PreviewPath(s.dir, kind, id), kind); err != nil {
			return fmt.Errorf("photostore: копия %s фото %s.%s: %w", kind, id, ext, err)
		}
	}
	return nil
}

// decodeOriented декодирует изображение по расширению и, если это JPEG с
// EXIF-ориентацией, разворачивает пиксели так, как их показал бы браузер.
func decodeOriented(path, ext string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("photostore: открытие фото %s: %w", path, err)
	}
	defer f.Close()

	var img image.Image
	switch ext {
	case "jpg", "jpeg":
		img, err = jpeg.Decode(f)
	case "png":
		img, err = png.Decode(f)
	case "gif":
		img, err = gif.Decode(f)
	case "webp":
		img, err = webp.Decode(f)
	default:
		return nil, fmt.Errorf("%w: %s", ErrPreviewUnsupported, ext)
	}
	if err != nil {
		return nil, fmt.Errorf("photostore: декодирование %s: %w", path, err)
	}
	if (ext == "jpg" || ext == "jpeg") && img != nil {
		if orient := jpegOrientation(path); orient != 1 {
			img = rotateImage(img, orient)
		}
	}
	return img, nil
}

// rotateImage разворачивает изображение согласно EXIF-ориентации:
// 3 — 180°, 6 — 90° по часовой, 8 — 90° против часовой; остальные значения
// (зеркальные) не поддерживаются и оставляют изображение как есть.
func rotateImage(img image.Image, orient int) image.Image {
	switch orient {
	case 3, 6, 8:
	default:
		return img
	}
	b := img.Bounds()
	// Вращение попиксельно с прямым доступом к Pix — нужен *image.RGBA.
	var src *image.RGBA
	if rgba, ok := img.(*image.RGBA); ok {
		src = rgba
	} else {
		src = image.NewRGBA(b)
		stddraw.Draw(src, b, img, b.Min, stddraw.Src)
	}
	w, h := b.Dx(), b.Dy()
	switch orient {
	case 3: // 180°: размер не меняется.
		return rotate(src, w, h, func(x, y int) (int, int) { return w - 1 - x, h - 1 - y })
	case 6: // 90° по часовой: ширина и высота меняются местами.
		return rotate(src, h, w, func(x, y int) (int, int) { return y, h - 1 - x })
	default: // 8: 90° против часовой.
		return rotate(src, h, w, func(x, y int) (int, int) { return w - 1 - y, x })
	}
}

// rotate копирует пиксели src в новое изображение dstW x dstH; srcFn
// отображает координаты пикселя назначения в координаты источника.
func rotate(src *image.RGBA, dstW, dstH int, srcFn func(x, y int) (int, int)) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	s, d := src.Pix, dst.Pix
	ss, ds := src.Stride, dst.Stride
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx, sy := srcFn(x, y)
			i := sy*ss + sx*4
			j := y*ds + x*4
			d[j], d[j+1], d[j+2], d[j+3] = s[i], s[i+1], s[i+2], s[i+3]
		}
	}
	return dst
}

// writePreview масштабирует изображение до длинной стороны вида (изображения
// меньше размера копии не увеличиваются) и пишет JPEG-копию по пути.
func writePreview(ctx context.Context, src image.Image, path, kind string) error {
	s, err := specFor(kind)
	if err != nil {
		return err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	nw, nh := w, h
	if long > s.maxSide {
		k := float64(s.maxSide) / float64(long)
		nw, nh = max(1, int(float64(w)*k+0.5)), max(1, int(float64(h)*k+0.5))
	}
	out := src
	if nw != w || nh != h {
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
		out = dst
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeJPEG(out, path, s.quality)
}

// writeJPEG пишет изображение в файл атомарно: сначала во временный файл в
// той же папке, затем rename — раздача никогда не увидит полузаписанный файл.
func writeJPEG(img image.Image, path string, quality int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("photostore: создание папки копий: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.jpg")
	if err != nil {
		return fmt.Errorf("photostore: временный файл копии: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// После успешного rename удаление — no-op; после ошибки убирает мусор.
		_ = os.Remove(tmpName)
	}()
	if err := jpeg.Encode(tmp, img, &jpeg.Options{Quality: quality}); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("photostore: кодирование копии: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("photostore: синхронизация копии: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("photostore: закрытие копии: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("photostore: сохранение копии: %w", err)
	}
	return nil
}

// removePreviews удаляет уменьшенные копии фото по id; отсутствие копии — не
// ошибка (ошибки удаления логируются: осиротевшая копия переживёт до очистки).
func (s *Store) removePreviews(id string) {
	for _, kind := range previewKindsOrder {
		if err := os.Remove(filepath.Join(s.dir, kind, id+".jpg")); err != nil && !os.IsNotExist(err) {
			slog.Info(fmt.Sprintf("photostore: удаление копии %s фото %s: %v", kind, id, err))
		}
	}
}

// Рисование текста и клеток таблицы: тонкая обёртка над x/image/font, чтобы
// раскладка в table.go читалась как сетка, а не как арифметика базовых линий.
package stockreport

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// faceSet — вшитые шрифты и кэш нарезанных начертаний по размеру.
type faceSet struct {
	regular *sfnt.Font
	bold    *sfnt.Font
	cache   map[faceKey]font.Face
}

// weight — начертание: обычное или жирное. Не bool-флаг: revive ругается на
// управляющие булевы параметры (flag-parameter), а вес читается как значение.
type weight int

const (
	weightRegular weight = iota
	weightBold
)

type faceKey struct {
	size int
	bold bool
}

// face отдаёт начертание нужного размера (кэшируется: нарезка небесплатна).
func (f *faceSet) face(size int, w weight) font.Face {
	key := faceKey{size: size, bold: w == weightBold}
	if face, ok := f.cache[key]; ok {
		return face
	}

	base := f.regular
	if key.bold {
		base = f.bold
	}

	face, err := opentype.NewFace(base, &opentype.FaceOptions{
		Size: float64(size), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	f.cache[key] = face

	return face
}

var cachedFaces *faceSet

// loadFaces разбирает вшитые шрифты один раз на процесс.
func loadFaces() (*faceSet, error) {
	if cachedFaces != nil {
		return cachedFaces, nil
	}

	regular, err := sfnt.Parse(fontRegular)
	if err != nil {
		return nil, fmt.Errorf("шрифт: %w", err)
	}

	bold, err := sfnt.Parse(fontBold)
	if err != nil {
		return nil, fmt.Errorf("шрифт жирный: %w", err)
	}

	cachedFaces = &faceSet{regular: regular, bold: bold, cache: make(map[faceKey]font.Face)}

	return cachedFaces, nil
}

// canvas рисует в готовый RGBA-холст.
type canvas struct {
	img   *image.RGBA
	faces *faceSet
}

// cell заливает клетку и обводит её линией.
func (c *canvas) cell(x, y, w, h int, fill color.Color) {
	draw.Draw(c.img, image.Rect(x, y, x+w-1, y+h-1), &image.Uniform{C: fill}, image.Point{}, draw.Src)
	c.line(x, y, w, 1)
	c.line(x, y+h-1, w, 1)
	c.line(x, y, 1, h)
	c.line(x+w-1, y, 1, h)
}

// line рисует линию толщиной 1 пиксель.
func (c *canvas) line(x, y, w, h int) {
	draw.Draw(c.img, image.Rect(x, y, x+w, y+h), &image.Uniform{C: colLine}, image.Point{}, draw.Src)
}

// outline обводит прямоугольник рамкой thickness пикселей.
func (c *canvas) outline(x, y, w, h, thickness int, col color.Color) {
	fill := &image.Uniform{C: col}
	draw.Draw(c.img, image.Rect(x, y, x+w, y+thickness), fill, image.Point{}, draw.Src)
	draw.Draw(c.img, image.Rect(x, y+h-thickness, x+w, y+h), fill, image.Point{}, draw.Src)
	draw.Draw(c.img, image.Rect(x, y, x+thickness, y+h), fill, image.Point{}, draw.Src)
	draw.Draw(c.img, image.Rect(x+w-thickness, y, x+w, y+h), fill, image.Point{}, draw.Src)
}

// text печатает строку по центру точки (x, cy): cy — середина строки, а не
// базовая линия, так клетки выравниваются без магии чисел.
func (c *canvas) text(s string, x, cy, size int, w weight, col color.Color) {
	face := c.faces.face(size, w)
	if face == nil {
		return
	}

	width := font.MeasureString(face, s).Ceil()
	metrics := face.Metrics()
	baseline := cy + (metrics.Ascent+metrics.Descent).Ceil()/2 - metrics.Descent.Ceil()

	d := &font.Drawer{
		Dst:  c.img,
		Src:  &image.Uniform{C: col},
		Face: face,
		Dot:  fixed.Point26_6{X: fixed.I(x - width/2), Y: fixed.I(baseline)},
	}
	d.DrawString(s)
}

// textLeft печатает строку от левого края (заголовки таблицы, подписи):
// у центрирования по x заголовок уезжает за границу холста.
func (c *canvas) textLeft(s string, x, cy, size int, w weight, col color.Color) {
	face := c.faces.face(size, w)
	if face == nil {
		return
	}

	metrics := face.Metrics()
	baseline := cy + (metrics.Ascent+metrics.Descent).Ceil()/2 - metrics.Descent.Ceil()

	d := &font.Drawer{
		Dst:  c.img,
		Src:  &image.Uniform{C: col},
		Face: face,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(baseline)},
	}
	d.DrawString(s)
}

// textWrap печатает название в колонку с переносом по словам: строки
// выравниваются по центру клетки, лишнее уходит в многоточие.
func (c *canvas) textWrap(s string, x, y, width, height, size int, col color.Color) {
	face := c.faces.face(size, weightRegular)
	if face == nil {
		return
	}

	lines := wrapWords(face, s, width)
	if len(lines) > 2 {
		lines = []string{lines[0], truncate(face, strings.Join(lines[1:], " "), width)}
	}

	lineH := size + 4
	top := y + (height-len(lines)*lineH)/2
	for i, line := range lines {
		c.text(line, x+width/2, top+lineH/2+i*lineH, size, weightRegular, col)
	}
}

// wrapWords режет текст по словам под ширину колонки.
func wrapWords(face font.Face, s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}

	lines := make([]string, 0, 2)
	current := words[0]
	for _, word := range words[1:] {
		candidate := current + " " + word
		if font.MeasureString(face, candidate).Ceil() <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}

	return append(lines, current)
}

// truncate обрезает строку по ширине, добавляя многоточие.
func truncate(face font.Face, s string, width int) string {
	if font.MeasureString(face, s).Ceil() <= width {
		return s
	}

	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		if font.MeasureString(face, string(runes)+"…").Ceil() <= width {
			return string(runes) + "…"
		}
	}

	return "…"
}

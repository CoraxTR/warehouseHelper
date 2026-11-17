// Таблица избытков картинкой: PNG рисуется кодом, без браузера (решение
// владельца 17.11.2026 — на машине склада нет места под Chromium).
//
// Раскладка повторяет страницу «Сроки»: строка — товар, дальше пары
// «количество + срок годности» по лотам; срок со скидкой подсвечен жёлтым, а
// размер скидки напечатан мелким шрифтом под датой.
package stockreport

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"time"

	"warehouseHelper/internal/discounts"
)

//go:embed assets/DejaVuSans.ttf
var fontRegular []byte

//go:embed assets/DejaVuSans-Bold.ttf
var fontBold []byte

// Цвета страниц склада: тёмная тема, жёлтый — скидка.
var (
	colBG     = color.RGBA{R: 0x0F, G: 0x0F, B: 0x14, A: 0xFF}
	colPanel  = color.RGBA{R: 0x14, G: 0x14, B: 0x1A, A: 0xFF}
	colHead   = color.RGBA{R: 0x17, G: 0x17, B: 0x20, A: 0xFF}
	colLine   = color.RGBA{R: 0x2A, G: 0x2A, B: 0x33, A: 0xFF}
	colEmpty  = color.RGBA{R: 0x11, G: 0x11, B: 0x17, A: 0xFF}
	colText   = color.RGBA{R: 0xE8, G: 0xE8, B: 0xEF, A: 0xFF}
	colMuted  = color.RGBA{R: 0x8A, G: 0x94, B: 0xA0, A: 0xFF}
	colYellow = color.RGBA{R: 0xFF, G: 0xE0, B: 0x7A, A: 0xFF}
	colYellBG = color.RGBA{R: 0x3A, G: 0x32, B: 0x10, A: 0xFF}
	colGroup  = color.RGBA{R: 0xB9, G: 0xC0, B: 0xCA, A: 0xFF}
)

// Геометрия таблицы в пикселях.
const (
	padX     = 24
	padY     = 20
	nameW    = 320
	qtyW     = 64
	bbW      = 96
	rowH     = 48
	headH    = 26
	groupH   = 26
	maxLots  = 6
	titlePX  = 14
	subPX    = 12
	cellPX   = 13
	smallPX  = 10
	headerPX = 11
)

// DrawSurplusTable рисует таблицу избытков в PNG. Пустой список — nil без
// ошибки: вызывающий решает, что отправить в чат вместо картинки.
func DrawSurplusTable(lots []discounts.SurplusLot, day time.Time) ([]byte, error) {
	groups := groupSurplus(lots)
	if len(groups) == 0 {
		return nil, nil
	}

	faces, err := loadFaces()
	if err != nil {
		return nil, err
	}

	cols := lotColumns(groups)
	width := padX*2 + nameW + cols*(qtyW+bbW)
	height := padY + 44 + groupH + headH + len(groups)*rowH + 34

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: colBG}, image.Point{}, draw.Src)

	c := &canvas{img: img, faces: faces}

	c.textLeft("ВЫГРУЗКА · ИЗБЫТОК", padX, padY+8, titlePX, weightBold, colText)
	c.textLeft(fmt.Sprintf("%s · %d товара · жёлтым — срок со скидкой",
		day.Format("02.01.2006"), len(groups)), padX, padY+30, subPX, weightRegular, colMuted)

	y := padY + 44
	c.cell(padX, y, nameW, groupH+headH, colPanel)
	for i := range cols {
		x := padX + nameW + i*(qtyW+bbW)
		c.cell(x, y, qtyW+bbW, groupH, colHead)
		c.text(fmt.Sprintf("ЛОТ %d", i+1), x+(qtyW+bbW)/2, y+groupH/2, headerPX, weightBold, colGroup)
	}
	y += groupH

	for i := range cols {
		x := padX + nameW + i*(qtyW+bbW)
		c.cell(x, y, qtyW, headH, colHead)
		c.text("КОЛ-ВО", x+qtyW/2, y+headH/2, smallPX, weightBold, colMuted)
		c.cell(x+qtyW, y, bbW, headH, colHead)
		c.text("СРОК", x+qtyW+bbW/2, y+headH/2, smallPX, weightBold, colMuted)
	}
	c.textLeft("ТОВАР", padX+10, y+headH/2, headerPX, weightBold, colMuted)
	y += headH

	for _, g := range groups {
		c.cell(padX, y, nameW, rowH, colPanel)
		c.textWrap(g.Name, padX+8, y, nameW-16, rowH, cellPX, colText)

		for i := range cols {
			x := padX + nameW + i*(qtyW+bbW)
			if i >= len(g.Lots) {
				c.cell(x, y, qtyW, rowH, colEmpty)
				c.cell(x+qtyW, y, bbW, rowH, colEmpty)
				continue
			}

			lot := g.Lots[i]
			fill := colPanel
			if lot.HasDiscount() {
				fill = colYellBG
			}

			c.cell(x, y, qtyW, rowH, fill)
			c.text(fmt.Sprintf("%d", lot.Qty), x+qtyW/2, y+rowH/2, cellPX+1, weightBold, colText)
			if lot.HasDiscount() {
				// Рамка на всю пару: заливка на тёмной теме читается слабо,
				// а жёлтый контур видно и на печати.
				c.outline(x, y, qtyW+bbW, rowH, 2, colYellow)
			}

			c.cell(x+qtyW, y, bbW, rowH, fill)
			if lot.HasDiscount() {
				c.text(lot.BestBefore.Format("02.01"), x+qtyW+bbW/2, y+17, cellPX, weightRegular, colText)
				c.text(lot.DiscountLabel(), x+qtyW+bbW/2, y+33, smallPX, weightBold, colYellow)
				continue
			}
			c.text(lot.BestBefore.Format("02.01"), x+qtyW+bbW/2, y+rowH/2, cellPX, weightRegular, colText)
		}

		y += rowH
	}

	c.textLeft("Пустые клетки — у товара меньше сроков", padX, y+16, smallPX, weightRegular, colMuted)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("png: %w", err)
	}

	return buf.Bytes(), nil
}

// lotColumns — сколько пар «количество + срок» показать: по максимуму среди
// товаров, но не больше maxLots (иначе таблица нечитаема в телефоне).
func lotColumns(groups []surplusGroup) int {
	cols := 1
	for _, g := range groups {
		if len(g.Lots) > cols {
			cols = len(g.Lots)
		}
	}

	if cols > maxLots {
		return maxLots
	}

	return cols
}

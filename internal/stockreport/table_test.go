package stockreport

import (
	"bytes"
	"image/png"
	"os"
	"testing"
	"time"

	"golang.org/x/image/font"

	"warehouseHelper/internal/discounts"
)

// Таблица избытков рисуется в PNG без браузера: картинка должна быть, быть
// валидным PNG и расширяться по числу лотов (решение владельца 17.11.2026).
func TestDrawSurplusTable(t *testing.T) {
	forty, twenty, ten := int16(40), int16(20), int16(10)
	lots := []discounts.SurplusLot{
		{ProductID: "p1", Name: "Ребра кальби нерезанные Мираторг. Охл.", BestBefore: time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC), Qty: 1, Site: &forty},
		{ProductID: "p1", Name: "Ребра кальби нерезанные Мираторг. Охл.", BestBefore: time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC), Qty: 1, Site: &forty},
		{ProductID: "p1", Name: "Ребра кальби нерезанные Мираторг. Охл.", BestBefore: time.Date(2026, time.December, 2, 0, 0, 0, 0, time.UTC), Qty: 1},
		{ProductID: "p2", Name: "Специя Африканское барбекю", BestBefore: time.Date(2026, time.December, 3, 0, 0, 0, 0, time.UTC), Qty: 30, Site: &ten, TG: &twenty},
		{ProductID: "p2", Name: "Специя Африканское барбекю", BestBefore: time.Date(2026, time.December, 17, 0, 0, 0, 0, time.UTC), Qty: 8},
	}

	data, err := DrawSurplusTable(lots, time.Date(2026, time.November, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("рисование: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("картинка пустая")
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("не PNG: %v", err)
	}
	if img.Bounds().Dx() < 600 || img.Bounds().Dy() < 150 {
		t.Errorf("размер %dx%d, want не меньше 600x150", img.Bounds().Dx(), img.Bounds().Dy())
	}

	// Пустой список — nil без ошибки: отправлять нечего.
	empty, err := DrawSurplusTable(nil, time.Now())
	if err != nil || empty != nil {
		t.Errorf("пустой список: data=%v err=%v, want nil/nil", empty, err)
	}

	// Образец для глаза: только когда путь задан явно.
	if path := os.Getenv("SURPLUS_SAMPLE"); path != "" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("образец: %v", err)
		}
	}
}

// Длинное название переносится по словам и не вылезает за колонку.
func TestWrapWords(t *testing.T) {
	faces, err := loadFaces()
	if err != nil {
		t.Fatalf("шрифты: %v", err)
	}

	face := faces.face(cellPX, false)
	limit := nameW - 16
	lines := wrapWords(face, "Ребра кальби нерезанные Мираторг. Охлаждённые, лот 18.11", limit)
	if len(lines) < 2 {
		t.Fatalf("строк = %d, want перенос: %v", len(lines), lines)
	}

	for _, line := range lines {
		if width := font.MeasureString(face, line).Ceil(); width > limit {
			t.Errorf("строка %q шириной %d, want не шире %d", line, width, limit)
		}
	}
}

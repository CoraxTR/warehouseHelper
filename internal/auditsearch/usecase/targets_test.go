package usecase

import (
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// Один скан по нескольким товарам: оба удаления из одного заказа находятся за
// один проход, а колонка «Поиск» говорит, какой именно товар искали.
func TestDeletionsMultiProduct(t *testing.T) {
	t.Parallel()

	rib := client.AuditPositionChange{OldValue: position(
		"https://api.moysklad.ru/api/remap/1.2/entity/product/can-rib", "Рибай Cresud. Охл.", 1.5)}
	cut := client.AuditPositionChange{OldValue: position(
		"https://api.moysklad.ru/api/remap/1.2/entity/product/can-cut", "Вырезка Cresud. Охл.", 2)}
	foreign := client.AuditPositionChange{OldValue: position(
		"https://api.moysklad.ru/api/remap/1.2/entity/product/can-other", "Стейк Рибай Праймбиф. Охл.", 3)}

	events := []client.AuditEventRow{
		orderEvent("00100", "o-100", nil, rib, cut, foreign),
	}

	targets := []target{
		{id: "p-rib", canonical: "can-rib", name: "Рибай Cresud. Охл."},
		{id: "p-cut", canonical: "can-cut", name: "Вырезка Cresud. Охл."},
	}

	got := deletions(events, targets)
	if len(got) != 2 {
		t.Fatalf("найдено %d, ждали 2: %+v", len(got), got)
	}

	byQuery := map[string]Result{}
	for _, r := range got {
		byQuery[r.Query] = r
	}

	if r, ok := byQuery["Рибай Cresud. Охл."]; !ok || r.Quantity != 1.5 {
		t.Errorf("Рибай: %+v, ждали 1.5", byQuery["Рибай Cresud. Охл."])
	}

	if r, ok := byQuery["Вырезка Cresud. Охл."]; !ok || r.Quantity != 2 {
		t.Errorf("Вырезка: %+v, ждали 2", byQuery["Вырезка Cresud. Охл."])
	}
}

// Товар, который добавлен дважды или с мусором в строке, идёт в скан один раз.
func TestNormalizeProducts(t *testing.T) {
	t.Parallel()

	got, err := normalizeProducts([]string{" a ", "b,a", "", "b"})
	if err != nil {
		t.Fatalf("разбор списка: %v", err)
	}

	want := []string{"a", "b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("список %v, ждали %v", got, want)
	}

	if _, err := normalizeProducts([]string{"  "}); err == nil {
		t.Error("пустой список должен быть отказом")
	}
}

// Подпись задачи: одним товаром — имя, несколькими — перечисление.
func TestProductLabel(t *testing.T) {
	t.Parallel()

	if got := productLabel([]target{{name: "Рибай Cresud. Охл."}}); got != "Рибай Cresud. Охл." {
		t.Errorf("один товар: %q", got)
	}

	got := productLabel([]target{{name: "Рибай"}, {name: "Вырезка"}})
	if got != "Рибай, Вырезка" {
		t.Errorf("два товара: %q", got)
	}
}

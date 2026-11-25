package usecase

import (
	"fmt"
	"testing"

	"warehouseHelper/internal/discounts"
)

// Ёмкость окна считается по ТОВАРАМ, а не по парам: товар с несколькими лотами
// занимает ОДНО место, и следующий товар в окно входит (решение владельца
// 25.11.2026: 12 строк = 12 товаров). До правки дальние лоты выбивали из окна
// следующую карточку, и её значения не писались вовсе.
func TestWindowCapacityCountsProducts(t *testing.T) {
	pairs := make([]PairState, 0, 13)
	// Товар с двумя лотами — по сроку, оба в окне по приоритету.
	pairs = append(pairs,
		PairState{ProductID: "multi", Name: "Филе-Миньон", BestBefore: day(10), Qty: 5, DaysLeft: 10, Expiry: new(int16(20))},
		PairState{ProductID: "multi", Name: "Филе-Миньон", BestBefore: day(13), Qty: 5, DaysLeft: 13, Expiry: new(int16(20))},
	)
	for i := range 11 {
		pairs = append(pairs, PairState{
			ProductID:  fmt.Sprintf("p%02d", i),
			Name:       fmt.Sprintf("Товар %d", i),
			BestBefore: day(14 + i),
			Qty:        3,
			DaysLeft:   14 + i,
			Expiry:     new(int16(10)),
		})
	}

	// Ёмкость 12 = 12 товаров: 11 однотипных + товар с двумя лотами.
	keys := windowKeys(pairs, 12)
	if len(keys) != 13 {
		t.Fatalf("пар в окне %d, want 13 (12 товаров, один с двумя лотами): %v", len(keys), keys)
	}

	// Последний по приоритету товар не выброшен из окна.
	last := discounts.LotKey{ProductID: "p10", BestBefore: day(24)}
	if _, ok := keys[last]; !ok {
		t.Error("товар p10 не попал в окно: место заняли дальние лоты другого товара")
	}
	// Оба лота многолотьевого товара — в окне: значения дальних лотов не снимаются.
	for _, dt := range []int{10, 13} {
		if _, ok := keys[discounts.LotKey{ProductID: "multi", BestBefore: day(dt)}]; !ok {
			t.Errorf("лот multi/%d выпал из окна", dt)
		}
	}
}

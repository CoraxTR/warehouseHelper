package usecase

import (
	"testing"

	"warehouseHelper/internal/discounts"
)

// lotKeys — ключи пар в виде «товар@срок»: так ожидание читается глазами.
func lotKeys(keys []discounts.LotKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.ProductID+"@"+k.BestBefore.Format("02.01"))
	}

	return out
}

// ActiveLots — пары активных позиций окна: страница «Сроки» показывает скидку в
// правой колонке только у них, у пар за ёмкостью остаётся подсветка и карточка
// количества (решение владельца 23.09.2026). Группа избытка раскрывается во все
// свои сроки — позиция в окне одна, а пар в ней по числу сроков.
func TestActiveLotsWindowAndGroup(t *testing.T) {
	rows := []discounts.Row{
		{ProductID: "p-manual", Name: "Творог", BestBefore: day(8), Percent: 40, Source: discounts.SourceManual},
		{ProductID: "p-expiry", Name: "Сыр", BestBefore: day(5), Percent: 30, Source: discounts.SourceExpiry},
		{ProductID: "p-group", Name: "Стейк", BestBefore: day(15), Percent: 10, Source: discounts.SourceSurplus, Coeff: 1.4, SurplusGroup: true},
		{ProductID: "p-group", Name: "Стейк", BestBefore: day(22), Percent: 10, Source: discounts.SourceSurplus, Coeff: 1.2, SurplusGroup: true},
		{ProductID: "p-far", Name: "Колбаса", BestBefore: day(30), Percent: 10, Source: discounts.SourceSurplus, Coeff: 1.1},
	}

	tests := []struct {
		name     string
		capacity int
		want     []string
	}{
		{
			"ёмкость 2: две первые позиции приоритета",
			2,
			[]string{"p-manual@" + day(8).Format("02.01"), "p-expiry@" + day(5).Format("02.01")},
		},
		{
			"ёмкость 3: группа входит целиком (два срока — две пары)",
			3,
			[]string{
				"p-manual@" + day(8).Format("02.01"),
				"p-expiry@" + day(5).Format("02.01"),
				"p-group@" + day(15).Format("02.01"),
				"p-group@" + day(22).Format("02.01"),
			},
		},
		{
			"ёмкость 0: без ограничения — все пары",
			0,
			[]string{
				"p-manual@" + day(8).Format("02.01"),
				"p-expiry@" + day(5).Format("02.01"),
				"p-group@" + day(15).Format("02.01"),
				"p-group@" + day(22).Format("02.01"),
				"p-far@" + day(30).Format("02.01"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lotKeys(activeLots(rows, tc.capacity))
			if len(got) != len(tc.want) {
				t.Fatalf("пар в окне %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("пара %d: %s, want %s (%v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

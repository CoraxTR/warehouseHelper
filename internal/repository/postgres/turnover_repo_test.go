package postgres

import (
	"fmt"
	"testing"
)

// Батч-чтение окна оборотов: id режутся пачками по turnoverReadBatch — число
// запросов растёт как число пачек, а не как число товаров (1000 товаров = 2
// запроса), порядок id сохраняется.
func TestIDBatches(t *testing.T) {
	tests := []struct {
		name string
		ids  int
		size int
		want []int // размеры пачек
	}{
		{name: "пустой список", ids: 0, size: turnoverReadBatch, want: nil},
		{name: "одна полная пачка", ids: 500, size: turnoverReadBatch, want: []int{500}},
		{name: "ровно две пачки", ids: 1000, size: turnoverReadBatch, want: []int{500, 500}},
		{name: "неполная последняя пачка", ids: 1001, size: turnoverReadBatch, want: []int{500, 500, 1}},
		{name: "нулевой размер — по одному", ids: 3, size: 0, want: []int{1, 1, 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := make([]string, 0, tt.ids)
			for i := range tt.ids {
				ids = append(ids, fmt.Sprintf("p%04d", i))
			}

			got := idBatches(ids, tt.size)
			if len(got) != len(tt.want) {
				t.Fatalf("idBatches() = %d пачек, want %d", len(got), len(tt.want))
			}

			// Пачки идут по порядку и вместе дают исходный список (без потерь
			// и повторов): порядок чтения стабилен.
			var flat []string
			for i, batch := range got {
				if len(batch) != tt.want[i] {
					t.Errorf("пачка %d: %d id, want %d", i, len(batch), tt.want[i])
				}
				flat = append(flat, batch...)
			}
			if len(flat) != len(ids) {
				t.Fatalf("id после разбиения = %d, want %d", len(flat), len(ids))
			}
			for i, id := range ids {
				if flat[i] != id {
					t.Fatalf("id[%d] = %q, want %q (порядок сохранён)", i, flat[i], id)
				}
			}
		})
	}
}

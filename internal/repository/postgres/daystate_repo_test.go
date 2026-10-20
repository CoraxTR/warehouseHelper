package postgres

import (
	"strings"
	"testing"
)

// TestSnapshotDoneCoversAllLots — маркер снапшота не должен срабатывать от
// одной строки дня: строки пишут ещё события стока и календарь «Доступность».
// Признак «снапшот сделан» — покрыты все товары с лотами (инцидент 19.10.2026:
// 26 строк из ~200 отменили снапшот дня, отчёт покраснел на 224 позиции).
// Пустой сток снапшотом не считается — иначе первый же тик до загрузки сроков
// закрывал бы день навсегда.
func TestSnapshotDoneCoversAllLots(t *testing.T) {
	for _, want := range []string{
		"SELECT DISTINCT product_id FROM product_stock",
		"LEFT JOIN product_day_state d",
		"WHERE d.product_id IS NULL",
		"EXISTS (SELECT 1 FROM lots)",
	} {
		if !strings.Contains(snapshotDoneSQL, want) {
			t.Errorf("в маркере снапшота нет %q", want)
		}
	}
	if strings.Contains(snapshotDoneSQL, "WHERE date = $1 LIMIT 1") {
		t.Error("маркер вернулся к «есть хоть одна строка за день»")
	}
}

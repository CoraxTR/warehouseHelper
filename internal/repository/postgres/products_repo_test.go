package postgres

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/domain"
)

// TestScanProductNullableText — NULL в nullable TEXT-колонках каталога
// (internal_code, group_name, folder_id) даёт пустые строки, а не падение Scan
// («cannot scan NULL into *string»): у товара без кода МС и без папки в БД
// именно NULL, а модель хранит пустую строку.
//
// Порядок значений — productColumns: цены (buy_price, sale_price, effective_vat)
// идут после site_url; NULL в них — «МС не отдала», домен это различает (nil).
func TestScanProductNullableText(t *testing.T) {
	tests := []struct {
		name string
		vals []any
		want domain.Product
	}{
		{
			name: "три TEXT-колонки NULL",
			vals: []any{"p-1", nil, "Молоко", "шт", nil, nil, nil,
				int16(14), int16(6), "Копейка", true, false, nil, nil, nil, nil},
			want: domain.Product{
				ID: "p-1", Name: "Молоко", UOM: "шт",
				ShelfLife: new(int16(14)), PackSize: new(int16(6)),
				InventoryType: "Копейка", ShortList: true,
			},
		},
		{
			name: "url на сайте NULL — пустая строка",
			vals: []any{"p-3", "00009999", "Стейк", "кг", "Мясо", "folder-9", nil,
				nil, nil, "Копейка", false, false, nil, nil, nil, nil},
			want: domain.Product{
				ID: "p-3", InternalCode: "00009999", Name: "Стейк", UOM: "кг",
				GroupName: "Мясо", FolderID: "folder-9", InventoryType: "Копейка",
			},
		},
		{
			name: "значения есть — переносятся как есть",
			vals: []any{"p-2", "00001234", "Сыр", "кг", "Молочка/Сыры", "folder-7", 0.35,
				nil, nil, "Копейка", false, true, "https://www.steakhome.ru/catalog/element/syr/",
				int64(12345), int64(19999), int16(22)},
			want: domain.Product{
				ID: "p-2", InternalCode: "00001234", Name: "Сыр", UOM: "кг",
				GroupName: "Молочка/Сыры", FolderID: "folder-7",
				AverageWeight: new(0.35), InventoryType: "Копейка", TrackWeekly: true,
				SiteURL:      "https://www.steakhome.ru/catalog/element/syr/",
				BuyPrice:     new(int64(12345)),
				SalePrice:    new(int64(19999)),
				EffectiveVat: new(int16(22)),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanProduct(fakeRow{vals: tc.vals})
			if err != nil {
				t.Fatalf("scanProduct: %v", err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("scanProduct = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

// TestScanProductPrices — цены: NULL остаётся nil («МС не отдала»), значения
// читаются в указатели, effective_vat = -1 — наша метка «без НДС» и не путается
// с NULL. Ключевое: NULL под *int64/*int16 допустим, а под сам int64 — упал бы
// (проверяет scanValue харнесса) — ровно то, что роняет прод.
func TestScanProductPrices(t *testing.T) {
	base := func(buy, sale, vat any) []any {
		return []any{"p-1", "00010001", "Товар", "шт", nil, nil, nil,
			nil, nil, "Копейка", false, false, nil, buy, sale, vat}
	}

	tests := []struct {
		name string
		vals []any
		want domain.Product
	}{
		{
			name: "цены не заданы — NULL в nil",
			vals: base(nil, nil, nil),
			want: domain.Product{
				ID: "p-1", InternalCode: "00010001", Name: "Товар", UOM: "шт",
				InventoryType: "Копейка",
			},
		},
		{
			name: "цены заданы (копейки), НДС 22",
			vals: base(int64(150000), int64(249000), int16(22)),
			want: domain.Product{
				ID: "p-1", InternalCode: "00010001", Name: "Товар", UOM: "шт",
				InventoryType: "Копейка",
				BuyPrice:      new(int64(150000)),
				SalePrice:     new(int64(249000)),
				EffectiveVat:  new(int16(22)),
			},
		},
		{
			name: "effective_vat = -1 — «без НДС», не запутать с NULL",
			vals: base(int64(1000), nil, int16(-1)),
			want: domain.Product{
				ID: "p-1", InternalCode: "00010001", Name: "Товар", UOM: "шт",
				InventoryType: "Копейка",
				BuyPrice:      new(int64(1000)),
				EffectiveVat:  new(int16(-1)),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanProduct(fakeRow{vals: tc.vals})
			if err != nil {
				t.Fatalf("scanProduct: %v", err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("scanProduct = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

// TestScanProductColumnCount — число колонок в productColumns обязано совпасть с
// арностью Scan в scanProduct (AGENTS.md): добавление колонки без правки хелпера
// иначе молчит — строки читаются со сдвигом.
func TestScanProductColumnCount(t *testing.T) {
	row := &captureRow{}
	if _, err := scanProduct(row); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("scanProduct: %v, want pgx.ErrNoRows", err)
	}

	want := countColumns(productColumns)
	if row.dests != want {
		t.Errorf("scan-хелпер разбирает %d колонок, в списке — %d", row.dests, want)
	}
}

// TestUpsertProductSQL — три ценовые колонки обязаны быть и в INSERT, и в
// ON CONFLICT (id) DO UPDATE SET ... = EXCLUDED.…: цены синк перезаписывает
// всегда (иначе они никогда не появятся в базе), в отличие от ручного site_url,
// которого в этом запросе быть НЕ должно.
func TestUpsertProductSQL(t *testing.T) {
	for _, frag := range []string{
		"buy_price, sale_price, effective_vat",
		"$13, $14, $15",
		"buy_price     = EXCLUDED.buy_price",
		"sale_price    = EXCLUDED.sale_price",
		"effective_vat = EXCLUDED.effective_vat",
	} {
		if !strings.Contains(upsertProductSQL, frag) {
			t.Errorf("upsertProductSQL: нет фрагмента %q", frag)
		}
	}
	if strings.Contains(upsertProductSQL, "site_url") {
		t.Error("upsertProductSQL: site_url не должен перезаписываться синком (ручной url карточки)")
	}
}

// TestProductIDsSQL — обход каталога обновителем цен: только id, стабильный
// порядок.
func TestProductIDsSQL(t *testing.T) {
	for _, frag := range []string{"SELECT id", "FROM products", "ORDER BY id"} {
		if !strings.Contains(productIDsSQL, frag) {
			t.Errorf("productIDsSQL: нет фрагмента %q", frag)
		}
	}
}

// TestUpdateProductPricesEmptyInput — пустой вход не должен трогать БД: у
// PGClient без пула (nil-Pool) обращение упало бы паникой, а метод обязан
// вернуть (0, nil) до него.
func TestUpdateProductPricesEmptyInput(t *testing.T) {
	pg := &PGClient{}
	for _, prices := range [][]domain.ProductPrice{nil, {}} {
		got, err := pg.UpdateProductPrices(context.Background(), prices)
		if err != nil {
			t.Fatalf("UpdateProductPrices(%v): %v", prices, err)
		}
		if got != 0 {
			t.Errorf("UpdateProductPrices(%v) = %d, want 0", prices, got)
		}
	}
}

// TestUpdateProductPriceSQL — семантика «nil-поле не затирает известное» и
// «холостых UPDATE нет» держится ровно на COALESCE в SET и IS DISTINCT FROM в
// WHERE. Проверяем текст запроса: SQL на VM не гоняем (Postgres только на
// препроде), поэтому это единственная страховка от опечатки.
func TestUpdateProductPriceSQL(t *testing.T) {
	for _, frag := range []string{
		"buy_price     = COALESCE($2, buy_price)",
		"sale_price    = COALESCE($3, sale_price)",
		"effective_vat = COALESCE($4, effective_vat)",
		"WHERE id = $1",
		"IS DISTINCT FROM",
	} {
		if !strings.Contains(updateProductPriceSQL, frag) {
			t.Errorf("updateProductPriceSQL: нет фрагмента %q", frag)
		}
	}
}

// declaredColumns вытаскивает колонки из CREATE TABLE <table> (...) в .sql-файле
// пакета: имя → тип (верхний регистр). Схема — источник правды, поэтому SQL-код
// в Go сверяем именно с ней: Postgres на VM нет, и опечатка/переименование
// колонки всплыли бы только на препроде. Общий для products и курсора цен.
func declaredColumns(t *testing.T, file, table string) map[string]string {
	t.Helper()

	sql := readSQLFile(t, file)
	marker := "CREATE TABLE " + table
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatalf("%s: нет %q", file, marker)
	}
	body := sql[start:]
	// Конец тела — закрывающая скобка на СВОЕЙ строке («\n);»), а не первое
	// «);» вообще: в комментариях к колонкам встречается «);» (напр.
	// «(productFolder.name); NULL»), и наивный срез обрезал бы схему раньше.
	if end := strings.Index(body, "\n);"); end >= 0 {
		body = body[:end]
	}

	colRe := regexp.MustCompile(`(?m)^\s*([a-z_][a-z0-9_]*)\s+([A-Z][A-Z0-9]*)`)
	cols := map[string]string{}
	for _, m := range colRe.FindAllStringSubmatch(body, -1) {
		cols[m[1]] = m[2]
	}
	if len(cols) == 0 {
		t.Fatalf("%s: не разобрана ни одна колонка таблицы %s", file, table)
	}

	return cols
}

// TestProductColumnsMatchSchema — список колонок SELECT (productColumns) и
// ценовые колонки синка обязаны существовать в products_schema.sql. Иначе
// запрос валится только на живой БД, а тесты молчат.
func TestProductColumnsMatchSchema(t *testing.T) {
	cols := declaredColumns(t, "products_schema.sql", "products")

	for _, col := range strings.Split(productColumns, ",") {
		col = strings.TrimSpace(col)
		if col == "" {
			continue
		}
		if _, ok := cols[col]; !ok {
			t.Errorf("productColumns называет колонку %q, которой нет в products_schema.sql", col)
		}
	}

	// Типы цен: копейки — BIGINT (domain int64), НДС — SMALLINT (int16).
	// Несовпадение молча разъехалось бы с доменной моделью.
	for col, want := range map[string]string{
		"buy_price":     "BIGINT",
		"sale_price":    "BIGINT",
		"effective_vat": "SMALLINT",
	} {
		got, ok := cols[col]
		if !ok {
			t.Errorf("в products_schema.sql нет колонки %q", col)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}
}

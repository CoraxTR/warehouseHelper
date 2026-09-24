package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/msorders"
)

// Журнал подбора — таблица order_picking; Postgres на VM нет, поэтому проверяем
// чистые части: разбор строки (scanPickingUnit), сборку выборки (collectPickingUnits),
// арность списка колонок, ветки пустых списков (по nil-пулу — запроса быть не должно)
// и тексты SQL/схемы. SQL-ветки сами по себе без БД не прогоняются.

func TestScanPickingUnit(t *testing.T) {
	produced := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	bestBefore := time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)

	t.Run("весовой кусок: все поля", func(t *testing.T) {
		got, err := scanPickingUnit(fakeRow{vals: []any{
			"ord-1", "pos-1", "00001234", "pr-1", "Молоко", true, 1.2345, produced, bestBefore,
		}})
		if err != nil {
			t.Fatalf("scanPickingUnit: %v", err)
		}
		want := msorders.PickingUnit{
			OrderID: "ord-1", PositionID: "pos-1", InternalCode: "00001234",
			ProductID: "pr-1", ProductName: "Молоко", Weighted: true, WeightKg: 1.2345,
			ProducedOn: &produced, BestBefore: bestBefore,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("scanPickingUnit = %+v, want %+v", got, want)
		}
	})

	t.Run("штучный: NULL product_id и NULL produced_on", func(t *testing.T) {
		// product_id обнуляется при удалении товара из каталога (FK ON DELETE SET
		// NULL) — сканирование NULL прямо в string упало бы на первой же такой
		// строке. produced_on NULL — «дата выработки не известна», и nil тут
		// значим: ноль-дата подделала бы реальную дату.
		got, err := scanPickingUnit(fakeRow{vals: []any{
			"ord-2", "pos-2", "00009999", nil, "Товар без каталога", false, 1.0, nil, bestBefore,
		}})
		if err != nil {
			t.Fatalf("scanPickingUnit: %v", err)
		}
		want := msorders.PickingUnit{
			OrderID: "ord-2", PositionID: "pos-2", InternalCode: "00009999",
			ProductName: "Товар без каталога", Weighted: false, WeightKg: 1,
			ProducedOn: nil, BestBefore: bestBefore,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("scanPickingUnit = %+v, want %+v", got, want)
		}
	})
}

func TestScanPickingUnitBadRow(t *testing.T) {
	if _, err := scanPickingUnit(fakeRow{vals: []any{"ord-1", "pos-1"}}); err == nil {
		t.Fatal("scanPickingUnit на короткой строке: ошибки нет")
	}
}

// TestScanPickingUnitColumnCount — список колонок в константе и порядок Scan обязаны
// совпадать: арность сверяется, чтобы рассинхрон (AGENTS.md) не молчал.
func TestScanPickingUnitColumnCount(t *testing.T) {
	row := &captureRow{}
	if _, err := scanPickingUnit(row); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("scanPickingUnit: %v, want обёртку pgx.ErrNoRows", err)
	}

	want := countColumns(orderPickingColumns)
	if row.dests != want {
		t.Errorf("scan-хелпер разбирает %d колонок, в списке — %d", row.dests, want)
	}
	// 9 колонок: заказ и позиция (2), код склада (1), товар каталога и его снимок (2),
	// тип учёта и вес единицы (2), даты выработки и срока (2).
	if want != 9 {
		t.Errorf("в списке колонок %d, want 9", want)
	}
}

func TestCollectPickingUnits(t *testing.T) {
	produced := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	first := time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)
	second := time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC)

	t.Run("строки в порядке выборки, NULL-даты не роняют разбор", func(t *testing.T) {
		// Порядок строк — как отдал SQL (internal_code, best_before, id): хелпер
		// его не переставляет, иначе ответ на /sroki «прыгал» бы.
		rows := &fakeRows{rows: [][]any{
			{"ord-1", "pos-1", "00001234", "pr-1", "Молоко", true, 1.2345, produced, first},
			{"ord-1", "pos-1", "00001234", "pr-1", "Молоко", true, 2.5, nil, second},
		}}
		got, err := collectPickingUnits(rows)
		if err != nil {
			t.Fatalf("collectPickingUnits: %v", err)
		}
		want := []msorders.PickingUnit{
			{
				OrderID: "ord-1", PositionID: "pos-1", InternalCode: "00001234",
				ProductID: "pr-1", ProductName: "Молоко", Weighted: true, WeightKg: 1.2345,
				ProducedOn: &produced, BestBefore: first,
			},
			{
				OrderID: "ord-1", PositionID: "pos-1", InternalCode: "00001234",
				ProductID: "pr-1", ProductName: "Молоко", Weighted: true, WeightKg: 2.5,
				BestBefore: second,
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("collectPickingUnits = %+v, want %+v", got, want)
		}
	})

	t.Run("заказа без записей — пустой срез", func(t *testing.T) {
		got, err := collectPickingUnits(&fakeRows{})
		if err != nil {
			t.Fatalf("collectPickingUnits: %v", err)
		}
		if got == nil {
			t.Fatal("срез nil, want пустой")
		}
		if len(got) != 0 {
			t.Errorf("в срезе %d единиц, want 0", len(got))
		}
	})

	t.Run("короткая строка выборки", func(t *testing.T) {
		if _, err := collectPickingUnits(&fakeRows{rows: [][]any{{"ord-1"}}}); err == nil {
			t.Fatal("collectPickingUnits на короткой строке: ошибки нет")
		}
	})

	t.Run("ошибка выборки после строк", func(t *testing.T) {
		rows := &fakeRows{
			rows: [][]any{{"ord-1", "pos-1", "00001234", "pr-1", "Молоко", true, 1.0, nil, first}},
			err:  errors.New("обрыв связи"),
		}
		if _, err := collectPickingUnits(rows); !errors.Is(err, rows.err) {
			t.Fatalf("collectPickingUnits: %v, want обёртку ошибки выборки", err)
		}
	})
}

// TestPickingEmptyInputSkipsDB — пустой вход не должен ходить в БД. Нулевой PGClient
// (Pool == nil) здесь и есть проверка: если бы метод дошёл до пула, тест паниковал бы,
// а так он обязан вернуть nil.
func TestPickingEmptyInputSkipsDB(t *testing.T) {
	pg := &PGClient{}
	ctx := context.Background()

	if err := pg.AppendOrderPicking(ctx, nil); err != nil {
		t.Errorf("AppendOrderPicking(nil) = %v, want nil", err)
	}
	if err := pg.AppendOrderPicking(ctx, []msorders.PickingUnit{}); err != nil {
		t.Errorf("AppendOrderPicking(пустой) = %v, want nil", err)
	}
	if err := pg.ClearOrderPickingProducts(ctx, "ord-1", nil); err != nil {
		t.Errorf("ClearOrderPickingProducts(nil) = %v, want nil", err)
	}
	// Count 0 — «вернулось ноль единиц»: удалять нечего, запрос не нужен.
	if err := pg.RemoveOrderPickingUnits(ctx, msorders.PickingReturn{OrderID: "ord-1"}); err != nil {
		t.Errorf("RemoveOrderPickingUnits(count=0) = %v, want nil", err)
	}
	// Замена без позиций и без единиц — нечего делать ни удалением, ни вставкой.
	if err := pg.ReplaceOrderPicking(ctx, msorders.PickingReplace{OrderID: "ord-1"}); err != nil {
		t.Errorf("ReplaceOrderPicking(пустая замена) = %v, want nil", err)
	}
}

// TestOrderPickingSQL — проверяем тексты запросов: без БД это единственный способ
// поймать регрессию в конструкции (условия, порядок, лимит, приведение дат).
func TestOrderPickingSQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "вставка единицы",
			sql:  orderPickingInsertSQL,
			want: []string{
				"NULLIF($4, '')",       // пустой product_id домена → NULL, а не нарушение FK
				"$8::date", "$9::date", // даты без приведения сравниваются как timestamptz
			},
		},
		{
			name: "замена по позициям",
			sql:  orderPickingDeleteByPositionsSQL,
			want: []string{"order_id = $1", "position_id = ANY($2)"},
		},
		{
			name: "очистка заказа",
			sql:  orderPickingDeleteOrderSQL,
			want: []string{"order_id = $1"},
		},
		{
			name: "очистка по товарам",
			sql:  orderPickingDeleteByProductsSQL,
			want: []string{"order_id = $1", "product_id = ANY($2)"},
		},
		{
			name: "возврат единиц в сроки",
			sql:  orderPickingRemoveUnitsSQL,
			want: []string{
				"internal_code = $2",
				"best_before = $3::date",
				"weight = $4::numeric",
				"ORDER BY id DESC", // снимаем ровно вернувшиеся (последние) строки
				"LIMIT $5",
			},
		},
		{
			name: "строки заказа",
			sql:  orderPickingByOrderSQL,
			want: []string{
				orderPickingColumns,
				"WHERE order_id = $1",
				"ORDER BY internal_code, best_before, id",
			},
		},
		{
			name: "ретеншен",
			sql:  orderPickingCleanupSQL,
			want: []string{"touched_at < $1::date"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.want {
				if !strings.Contains(tc.sql, want) {
					t.Errorf("в запросе нет %q", want)
				}
			}
		})
	}

	// touched_at ставит DEFAULT CURRENT_DATE схемы — в INSERT его быть не должно,
	// иначе дата последней записи зависела бы от вызывающего кода.
	if strings.Contains(orderPickingInsertSQL, "touched_at") {
		t.Error("touched_at пишется в INSERT, а его должен ставить DEFAULT CURRENT_DATE")
	}
}

// TestOrderPickingInsertCoversScanColumns — колонки чтения и записи не должны
// расходиться: новая колонка в SELECT без колонки в INSERT читалась бы как NULL.
func TestOrderPickingInsertCoversScanColumns(t *testing.T) {
	for _, col := range strings.Split(orderPickingColumns, ",") {
		col = strings.TrimSpace(col)
		if col == "" {
			continue
		}
		if !strings.Contains(orderPickingInsertSQL, col) {
			t.Errorf("колонка %q есть в SELECT, но её нет в INSERT", col)
		}
	}
}

// TestOrderPickingSchemaSQL — состав таблицы заморожен: проверяем текстом, что в
// схеме есть все колонки, четыре индекса и представление агрегации, а представление
// снимается РАНЬШЕ таблицы (иначе повторное применение файла упрётся в зависимость
// view от таблицы и DROP TABLE упадёт).
func TestOrderPickingSchemaSQL(t *testing.T) {
	schema := readSQLFile(t, "order_picking_schema.sql")

	for _, want := range []string{
		"DROP VIEW IF EXISTS order_picking_aggregated;",
		"DROP TABLE IF EXISTS order_picking;",
		"position_id   TEXT NOT NULL",
		"internal_code TEXT NOT NULL",
		"weighted      BOOLEAN NOT NULL",
		"produced_on   DATE",
		"best_before   DATE NOT NULL",
		"touched_at    DATE NOT NULL DEFAULT CURRENT_DATE",
		"REFERENCES products(id) ON DELETE SET NULL",
		"CREATE INDEX order_picking_order_idx",
		"CREATE INDEX order_picking_order_position_idx",
		"CREATE INDEX order_picking_touched_idx",
		"CREATE INDEX order_picking_product_idx",
		"CREATE VIEW order_picking_aggregated",
		// товар в агрегации — код склада: у удалённого из каталога товара
		// product_id = NULL, и группировка по нему слила бы разные товары.
		"GROUP BY order_id, internal_code, best_before",
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("в order_picking_schema.sql нет %q", want)
		}
	}

	if strings.Index(schema, "DROP VIEW IF EXISTS order_picking_aggregated") >
		strings.Index(schema, "DROP TABLE IF EXISTS order_picking") {
		t.Error("DROP VIEW идёт после DROP TABLE: повторное применение файла упрётся в зависимость view от таблицы")
	}
}

// TestOrderPickingMigrationSQL — миграция живой БД: добавляет новые колонки и
// индексы идемпотентно, таблицу НЕ сносит (журнал в проде терять нельзя) и
// пересоздаёт представление под новый состав колонок.
func TestOrderPickingMigrationSQL(t *testing.T) {
	mig := readSQLFile(t, "order_picking_journal_migration.sql")

	for _, want := range []string{
		"order_picking_schema.sql", // шапка: нет таблицы — применять схему
		"ВЛАДЕЛЕЦ",
		"ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS position_id",
		"ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS internal_code",
		"ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS weighted",
		"ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS touched_at",
		"DEFAULT CURRENT_DATE",
		"CREATE INDEX IF NOT EXISTS order_picking_order_position_idx",
		"CREATE INDEX IF NOT EXISTS order_picking_touched_idx",
		"DROP VIEW IF EXISTS order_picking_aggregated", // CREATE OR REPLACE не меняет порядок колонок
		"CREATE VIEW order_picking_aggregated",
		"GROUP BY order_id, internal_code, best_before",
	} {
		if !strings.Contains(mig, want) {
			t.Errorf("в order_picking_journal_migration.sql нет %q", want)
		}
	}

	if strings.Contains(mig, "DROP TABLE") {
		t.Error("в миграции живой БД есть DROP TABLE: файл снёс бы журнал")
	}
}

// readSQLFile читает SQL-файл пакета: go test запускается с рабочим каталогом
// пакета, поэтому путь — только имя файла. Postgres на VM нет, и текст схемы —
// единственная доступная проверка её состава.
func readSQLFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

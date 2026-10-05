package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestScanPriceCursor — разбор строки курсора обновителя цен. Закрепляем новый
// контракт: Exists=false ТОЛЬКО на ErrNoRows («курсора ещё нет» — первый запуск, не
// ошибка), NULL в last_full_scan_at отдаётся пустой строкой (полного прохода не
// было → сравнение «!= сегодня» истинно и запускает проход), а дата
// форматируется DateOnly. Строгость подделки важна: NULL в string отклоняется
// (fakerows_test.go), поэтому дата читается через *time.Time, а не time.Time.
func TestScanPriceCursor(t *testing.T) {
	next := time.Date(2026, time.October, 5, 12, 30, 0, 0, time.UTC)
	day := time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		row          pgx.Row
		wantNext     time.Time
		wantFullScan string
		wantOK       bool
		wantErr      bool
	}{
		{
			name:         "строка есть, дата задана → ok=true и DateOnly",
			row:          fakeRow{vals: []any{next, day}},
			wantNext:     next,
			wantFullScan: "2026-10-05",
			wantOK:       true,
		},
		{
			// NULL в DATE — «полного прохода ещё не было»; пустая строка
			// (а не ошибка скана) — контракт scanPriceCursor.
			name:         "строка есть, NULL-дата → пустая строка, ok=true",
			row:          fakeRow{vals: []any{next, nil}},
			wantNext:     next,
			wantFullScan: "",
			wantOK:       true,
		},
		{
			// captureRow.Scan возвращает pgx.ErrNoRows — подделка пустой выборки.
			name:    "ErrNoRows → ok=false без ошибки",
			row:     &captureRow{},
			wantOK:  false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scanPriceCursor(tt.row)
			if tt.wantErr {
				if err == nil {
					t.Fatal("scanPriceCursor: ошибки нет, а ожидалась")
				}
				return
			}
			if err != nil {
				t.Fatalf("scanPriceCursor: %v", err)
			}
			if got.Exists != tt.wantOK {
				t.Errorf("Exists = %v, want %v", got.Exists, tt.wantOK)
			}
			if !got.Next.Equal(tt.wantNext) {
				t.Errorf("Next = %s, want %s", got.Next, tt.wantNext)
			}
			if got.LastFullScan != tt.wantFullScan {
				t.Errorf("LastFullScan = %q, want %q", got.LastFullScan, tt.wantFullScan)
			}
		})
	}
}

// TestPriceCursorSQL — read/write курсора: одна строка id=1, запись — upsert с
// обновлением next_scan_at, МСК-даты полного прохода (last_full_scan_at) и
// updated_at. Пустая строка даты пишется как NULL (NULLIF) и конвертируется в
// date без timestamptz-сдвига — иначе календарные сутки уехали бы. SQL на VM не
// гоняем (Postgres только на препроде), так что тексты — единственная страховка
// от опечатки в таблице/колонке.
func TestPriceCursorSQL(t *testing.T) {
	for _, frag := range []string{
		"SELECT next_scan_at, last_full_scan_at FROM product_price_cursor WHERE id = 1",
	} {
		if !strings.Contains(productPriceCursorGetSQL, frag) {
			t.Errorf("productPriceCursorGetSQL: %q — нет фрагмента %q", productPriceCursorGetSQL, frag)
		}
	}

	for _, frag := range []string{
		"INSERT INTO product_price_cursor (id, next_scan_at, last_full_scan_at)",
		"VALUES (1, $1, NULLIF($2, '')::date)",
		"ON CONFLICT (id) DO UPDATE SET next_scan_at = EXCLUDED.next_scan_at",
		"last_full_scan_at = EXCLUDED.last_full_scan_at",
		"updated_at = now()",
	} {
		if !strings.Contains(productPriceCursorSetSQL, frag) {
			t.Errorf("productPriceCursorSetSQL: нет фрагмента %q", frag)
		}
	}
}

// TestPriceCursorSQLMatchesSchema — сверка SQL-кода курсора с .sql-схемой: и
// список колонок, и их типы. Postgres на VM нет, поэтому это единственная
// связка «код ↔ схема»: переименование или смена типа колонки иначе всплыли бы
// только на препроде при первом тике.
func TestPriceCursorSQLMatchesSchema(t *testing.T) {
	cols := declaredColumns(t, "product_price_cursor_schema.sql", "product_price_cursor")

	// Колонки, к которым обращаются Get/Set.
	for _, col := range []string{"id", "next_scan_at", "last_full_scan_at", "updated_at"} {
		if _, ok := cols[col]; !ok {
			t.Errorf("SQL курсора использует %q, а в product_price_cursor_schema.sql колонки нет (есть: %v)", col, cols)
		}
	}

	// Типы, на которые опирается код: next — момент (timestamptz), full scan —
	// МСК-ДАТА (NULLIF($2,'')::date), updated — now().
	if got := cols["last_full_scan_at"]; got != "DATE" {
		t.Errorf("last_full_scan_at = %q, want DATE: код кладёт МСК-календарь (NULLIF($2,'')::date)", got)
	}
	if got := cols["next_scan_at"]; got != "TIMESTAMPTZ" {
		t.Errorf("next_scan_at = %q, want TIMESTAMPTZ", got)
	}
	if got := cols["id"]; got != "SMALLINT" {
		t.Errorf("id = %q, want SMALLINT (единственная строка id=1)", got)
	}

	schema := readSQLFile(t, "product_price_cursor_schema.sql")
	if !strings.Contains(schema, "CHECK (id = 1)") {
		t.Error("схема не закрепляет единственную строку: нет CHECK (id = 1)")
	}
	if !strings.Contains(schema, "DROP TABLE IF EXISTS product_price_cursor;") {
		t.Error("нет идемпотентного DROP TABLE IF EXISTS product_price_cursor;")
	}

	// Оба запроса адресуют ту же таблицу: переименование развело бы код и схему.
	for name, sql := range map[string]string{
		"productPriceCursorGetSQL": productPriceCursorGetSQL,
		"productPriceCursorSetSQL": productPriceCursorSetSQL,
	} {
		if !strings.Contains(sql, "product_price_cursor") {
			t.Errorf("%s: не обращается к таблице product_price_cursor", name)
		}
	}
}

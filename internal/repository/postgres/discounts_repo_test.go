package postgres

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"

	"github.com/jackc/pgx/v5"
)

// ptr — указатель на значение (NULL-колонки в снапшоте — *T).
func ptr[T any](v T) *T { return &v }

// fakeRow — подделка pgx.Row для проверки scan-хелперов без БД: Scan
// раскладывает заранее заданные значения по указателям (nil — SQL NULL).
// Остальные методы интерфейса хелперу не нужны — берутся у встроенного
// nil-интерфейса.
type fakeRow struct {
	pgx.Row

	vals []any
}

func (r fakeRow) Scan(dest ...any) error {
	if len(dest) != len(r.vals) {
		return fmt.Errorf("подделка Scan: колонок %d, значений %d", len(dest), len(r.vals))
	}
	for i, v := range r.vals {
		if err := scanValue(dest[i], v); err != nil {
			return err
		}
	}
	return nil
}

// scanValue кладёт src в указатель dst — как pgx: nil даёт нулевое значение
// (SQL NULL), число/строка приводится к типу указателя, а под указатель
// (**int16 и т.п.) значение аллоцируется.
func scanValue(dst, src any) error {
	dv := reflect.ValueOf(dst)
	if dv.Kind() != reflect.Pointer || dv.IsNil() {
		return fmt.Errorf("scan dest не указатель: %T", dst)
	}
	elem := dv.Elem()
	if src == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}
	if elem.Kind() == reflect.Pointer {
		p := reflect.New(elem.Type().Elem())
		if err := scanValue(p.Interface(), src); err != nil {
			return err
		}
		elem.Set(p)
		return nil
	}
	sv := reflect.ValueOf(src)
	if !sv.Type().ConvertibleTo(elem.Type()) {
		return fmt.Errorf("scan %T → %s: тип несовместим", src, elem.Type())
	}
	elem.Set(sv.Convert(elem.Type()))
	return nil
}

// captureRow запоминает число аргументов Scan и сразу возвращает ошибку —
// нужен, чтобы сверить арность списка колонок с арностью scan-хелпера.
type captureRow struct {
	pgx.Row

	dests int
}

func (r *captureRow) Scan(dest ...any) error {
	r.dests = len(dest)
	return pgx.ErrNoRows
}

// TestScanDiscountInput — разбор строки снапшота: дни периода берутся из
// track_weekly, NULL-оборот даёт nil и PeriodDays 0, отрицательный оборот
// (возвраты задним числом) хранится честно, NULL-срок годности — nil.
func TestScanDiscountInput(t *testing.T) {
	bb := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		vals []any
		want discounts.Input
	}{
		{
			name: "недельный товар — период 7 дней",
			vals: []any{"p-week", "Молоко 3,2%", "Молочка", false, 14, true, bb, 24, 12.5},
			want: discounts.Input{
				ProductID: "p-week", Name: "Молоко 3,2%", GroupName: "Молочка",
				ShelfLife: ptr[int16](14), TrackWeekly: true, BestBefore: bb, Qty: 24,
				Turnover: ptr(12.5), PeriodDays: 7,
			},
		},
		{
			name: "месячный товар — период 30 дней",
			vals: []any{"p-month", "Сыр", "Молочка", true, 90, false, bb, 8, 40.25},
			want: discounts.Input{
				ProductID: "p-month", Name: "Сыр", GroupName: "Молочка", ShortList: true,
				ShelfLife: ptr[int16](90), BestBefore: bb, Qty: 8,
				Turnover: ptr(40.25), PeriodDays: 30,
			},
		},
		{
			name: "нет завершённого периода — ни оборота, ни дней",
			vals: []any{"p-new", "Новинка", "Разное", false, 30, true, bb, 5, nil},
			want: discounts.Input{
				ProductID: "p-new", Name: "Новинка", GroupName: "Разное",
				ShelfLife: ptr[int16](30), TrackWeekly: true, BestBefore: bb, Qty: 5,
			},
		},
		{
			name: "срок годности не задан — NULL",
			vals: []any{"p-null", "Без срока", "Разное", false, nil, false, bb, 3, 1.5},
			want: discounts.Input{
				ProductID: "p-null", Name: "Без срока", GroupName: "Разное",
				BestBefore: bb, Qty: 3, Turnover: ptr(1.5), PeriodDays: 30,
			},
		},
		{
			name: "отрицательный оборот (возвраты задним числом)",
			vals: []any{"p-ret", "Творог", "Молочка", false, 7, false, bb, 12, -3.5},
			want: discounts.Input{
				ProductID: "p-ret", Name: "Творог", GroupName: "Молочка",
				ShelfLife: ptr[int16](7), BestBefore: bb, Qty: 12,
				Turnover: ptr(-3.5), PeriodDays: 30,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanDiscountInput(fakeRow{vals: tc.vals})
			if err != nil {
				t.Fatalf("scanDiscountInput: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("scanDiscountInput = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestScanDiscountInputBadRow — несовпадение колонок и значений не молчит:
// ошибка оборачивается (errors.Is на ErrNoRows от подделки).
func TestScanDiscountInputBadRow(t *testing.T) {
	if _, err := scanDiscountInput(fakeRow{vals: []any{"p1", "Молоко"}}); err == nil {
		t.Fatal("scanDiscountInput на короткой строке: ошибки нет")
	}
}

// countColumns считает элементы списка SELECT по запятым верхнего уровня:
// запятые внутри вызовов (`COALESCE(w.qty, m.qty)`) колонками не считаются.
func countColumns(list string) int {
	depth, n := 0, 1
	for _, r := range list {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		}
	}
	return n
}

// TestScanDiscountInputColumnCount — список колонок в константе и порядок Scan
// обязаны совпадать: проверяем арность, чтобы рассинхрон (AGENTS.md) не молчал.
func TestScanDiscountInputColumnCount(t *testing.T) {
	row := &captureRow{}
	if _, err := scanDiscountInput(row); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("scanDiscountInput: %v, want обёртку pgx.ErrNoRows", err)
	}

	want := countColumns(discountInputColumns)
	if row.dests != want {
		t.Errorf("scan-хелпер разбирает %d колонок, в списке — %d", row.dests, want)
	}
	if want == 0 {
		t.Fatal("список колонок пуст")
	}
}

// TestCountColumns — запятые внутри вызовов колонками не считаются.
func TestCountColumns(t *testing.T) {
	tests := []struct {
		name string
		list string
		want int
	}{
		{"одна колонка", "id", 1},
		{"две колонки", "id, name", 2},
		{"вложенный вызов", "id, COALESCE(a, b)", 2},
		{"вложенные вызовы и префиксы", "s.id, f(a, g(b, c)), p.name", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countColumns(tc.list); got != tc.want {
				t.Errorf("countColumns(%q) = %d, want %d", tc.list, got, tc.want)
			}
		})
	}
}

// TestDiscountInputQueryKeepsPeriodsClosed — незакрытый период в снапшот не
// попадает: сравниваются даты (date_trunc … ::date) и строго раньше начала
// текущей недели/месяца; сегодня — параметр $1, а не «сейчас» в SQL.
func TestDiscountInputQueryKeepsPeriodsClosed(t *testing.T) {
	for _, want := range []string{
		"w.week_start < date_trunc('week', $1::date)::date",
		"m.month_start < date_trunc('month', $1::date)::date",
	} {
		if !strings.Contains(discountInputQuery, want) {
			t.Errorf("в discountInputQuery нет %q", want)
		}
	}
}

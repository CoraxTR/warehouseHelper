package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Подделки строк pgx.Rows/pgx.Row — общий тестовый харнесс репозитория: тесты
// scan-хелперов идут без БД (на VM Postgres нет, проверка SQL — на препроде).
//
// Файл общий, а не внутри теста одного модуля, потому что строгость подделки —
// часть контракта: см. scanValue.

// scannerType — тип sql.Scanner: приёмник, который читает SQL NULL сам
// (sql.NullString и т.п.). Подделка Scan пропускает NULL под него, как pgx.
var scannerType = reflect.TypeFor[sql.Scanner]()

// ptr — указатель на значение (NULL-колонки в снапшотах — *T).
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

// fakeRows — подделка pgx.Rows: по Next отдаёт заранее заданные строки.
// Остальные методы интерфейса (Close, FieldDescriptions и прочее) хелперу не
// нужны — берутся у встроенного nil-интерфейса.
type fakeRows struct {
	pgx.Rows

	rows [][]any
	read int
	err  error
}

func (r *fakeRows) Next() bool {
	if r.read >= len(r.rows) {
		return false
	}
	r.read++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.read == 0 || r.read > len(r.rows) {
		return errors.New("подделка Scan вызвана до Next")
	}
	vals := r.rows[r.read-1]
	if len(dest) != len(vals) {
		return fmt.Errorf("подделка Scan: колонок %d, значений %d", len(dest), len(vals))
	}
	for i, v := range vals {
		if err := scanValue(dest[i], v); err != nil {
			return err
		}
	}
	return nil
}

func (r *fakeRows) Err() error { return r.err }

// scanValue кладёт src в указатель dst — как pgx: число/строка приводится к
// типу указателя, а под указатель (**int16, **string и т.п.) значение
// аллоцируется.
//
// NULL (nil) допустим только под nil-приёмник — указатель, интерфейс, срез,
// карта или sql.Scanner: NULL в обычный string/int у pgx — ошибка
// («cannot scan NULL into *string»), и подделка обязана вести себя так же.
// Мягкая версия (NULL → нулевое значение) пропускала в тестах ровно тот класс
// ошибок, который валит прод: 14.09.2026 снапшот скидок читал nullable-колонку
// (discount_source, group_name) прямо в string и падал на первой же строке без
// метки, а набор тестов этого не видел. Так что здесь намеренная строгость,
// а не удобство.
func scanValue(dst, src any) error {
	dv := reflect.ValueOf(dst)
	if dv.Kind() != reflect.Pointer || dv.IsNil() {
		return fmt.Errorf("scan dest не указатель: %T", dst)
	}
	elem := dv.Elem()
	if src == nil {
		if !nullTarget(elem) {
			return fmt.Errorf("cannot scan NULL into %T", dst)
		}
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

// nullTarget — можно ли положить SQL NULL в значение типа elem: nil-приёмники
// (*T, интерфейс, срез, карта, функция, канал) и всё, что умеет читать себя из
// NULL само (sql.NullString и прочие sql.Scanner — как в pgx).
func nullTarget(elem reflect.Value) bool {
	nilableKinds := []reflect.Kind{
		reflect.Pointer, reflect.Interface, reflect.Slice,
		reflect.Map, reflect.Func, reflect.Chan,
	}
	if slices.Contains(nilableKinds, elem.Kind()) {
		return true
	}

	return reflect.PointerTo(elem.Type()).Implements(scannerType)
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

// TestScanValueNullStrict — подделка Scan обязана повторять pgx на SQL NULL:
// NULL в обычный string/int — ошибка (ровно так снапшот скидок падал на живой
// БД 14.09.2026), NULL под *T, **T и sql.Scanner — норма. Без этой строгости
// тест на scan-хелпер не видит nullable-колонку, читаемую в string, и ошибка
// уходит в прод: NULL-приёмник проверяется не аккуратностью автора, а драйвером.
func TestScanValueNullStrict(t *testing.T) {
	var (
		plain  string
		num    int16
		optStr *string
		optNum *int16
		viaSQL sql.NullString
	)

	if err := scanValue(&plain, nil); err == nil {
		t.Error("NULL в string: ошибки нет, а pgx здесь падает")
	}
	if err := scanValue(&num, nil); err == nil {
		t.Error("NULL в int16: ошибки нет, а pgx здесь падает")
	}
	if err := scanValue(&optStr, nil); err != nil {
		t.Errorf("NULL в *string: %v", err)
	}
	if err := scanValue(&optNum, nil); err != nil {
		t.Errorf("NULL в *int16: %v", err)
	}
	if err := scanValue(&viaSQL, nil); err != nil {
		t.Errorf("NULL в sql.NullString: %v", err)
	}
	if optStr != nil || optNum != nil {
		t.Errorf("NULL под *T должен давать nil, получили %v / %v", optStr, optNum)
	}
}

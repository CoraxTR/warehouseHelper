// Парная сборка батча «внешний код поставщика → товар» из параллельных
// массивов формы (external_code[] + product_id[]). Чистая логика без БД/сети —
// вынесена в доменный пакет модуля приёмки, чтобы её можно было покрыть
// юнит-тестами (в пакете delivery/http тесты невозможны: template.Must паникует).
package receiving

import "strings"

// CodeRow — одна строка батча «внешний код → товар» после парной сборки.
// Errors пуст — строка готова к записи; иначе причины показывают оператору.
type CodeRow struct {
	Row          int      // номер строки в отправленном батче, 1..N (для сообщений)
	ExternalCode string   // внешний код (обрезан по краям)
	ProductID    string   // id товара каталога
	Errors       []string // ошибки строки (пусто = строка валидна)
}

// Valid сообщает, что строку можно писать (нет ошибок парной валидации).
func (r CodeRow) Valid() bool { return len(r.Errors) == 0 }

// PairCodeRows собирает батч строк из параллельных массивов формы:
// codes[i] ↔ productIDs[i]. Длины выравниваются по максимуму (недостающие
// значения — пустые). Полностью пустые пары (и код, и товар пусты после
// TrimSpace) пропускаются: такие строки оставляют в форме «на будущее», в
// батч они не идут. Пустой вход → nil.
//
// Ошибки строк собираются в CodeRow.Errors списком (не только первая):
//   - «внешний код без товара» — код заполнен, товар не выбран;
//   - «товар без внешнего кода» — товар выбран, код не введён;
//   - «дубликат внешнего кода в батче» — код уже встречался выше в этом батче
//     (первое вхождение остаётся валидным: оператор видит одну рабочую строку,
//     а не полный отказ батча из-за случайного дубля).
//
// Функция чистая — без обращений к БД/сети.
func PairCodeRows(codes, productIDs []string) []CodeRow {
	n := max(len(codes), len(productIDs))
	if n == 0 {
		return nil
	}

	seen := make(map[string]bool, n)
	rows := make([]CodeRow, 0, n)

	for i := 0; i < n; i++ {
		code := codeValueAt(codes, i)
		product := codeValueAt(productIDs, i)
		if code == "" && product == "" {
			continue // полностью пустая строка формы — в батч не уходит
		}

		row := CodeRow{Row: i + 1, ExternalCode: code, ProductID: product}

		if code == "" {
			row.Errors = append(row.Errors, "товар без внешнего кода")
		} else if product == "" {
			row.Errors = append(row.Errors, "внешний код без товара")
		}

		if code != "" {
			if seen[code] {
				row.Errors = append(row.Errors, "дубликат внешнего кода в батче")
			}

			seen[code] = true
		}

		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return nil
	}

	return rows
}

// codeValueAt возвращает i-й элемент без краевых пробелов; нет элемента — "".
func codeValueAt(vals []string, i int) string {
	if i >= len(vals) {
		return ""
	}

	return strings.TrimSpace(vals[i])
}

package registry

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// RefGoCheckAgainstOrder — заказ из реестра перевозчика. Денежные суммы
// хранятся в копейках (значение «100,00» из ячейки → 10000).
type RefGoCheckAgainstOrder struct {
	RefGoNumber                 string
	CashFact                    int64
	TerminalFact                int64
	TaxFact                     int64
	DeliveryPrice               int64
	AdditionalPayments          int64
	ReducedTimeIntervalPayments int64
}

// xlsximporter — импортёр xlsx-реестра перевозчика.
type xlsximporter struct{}

// NewxlsxImporter создаёт импортёр реестра перевозчика.
func NewxlsxImporter() *xlsximporter {
	return &xlsximporter{}
}

// ParseRefGoCheckFile разбирает xlsx-реестр перевозчика.
//
// Чтение начинается с 3-й строки первого листа:
//   - E: номер заказа; если значение содержит «Забор» — это забор, счётчик
//     заборов увеличивается, заказ не создаётся; три пустые строки подряд
//     завершают разбор;
//   - M: наличные, N: терминал, O: стоимость доставки, T: доп. услуги,
//     U: сокращение интервала, V: комиссия перевозчика.
//
// Возвращает количество заборов и заказы, сгруппированные по номеру строки
// реестра (ключ мапы — номер строки листа).
func (x *xlsximporter) ParseRefGoCheckFile(data []byte) (int64, map[int64]RefGoCheckAgainstOrder, error) {
	file, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return 0, nil, fmt.Errorf("не удалось открыть xlsx: %w", err)
	}
	defer file.Close()

	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		return 0, nil, errors.New("в файле нет листов")
	}

	rows, err := file.GetRows(sheets[0])
	if err != nil {
		return 0, nil, fmt.Errorf("не удалось прочитать лист %q: %w", sheets[0], err)
	}

	var retrievesCount int64
	orders := make(map[int64]RefGoCheckAgainstOrder)

	emptyStreak := 0
	for i := 2; i < len(rows); i++ {
		row := rows[i]

		refGoNumber := cellString(row, 4)
		if refGoNumber == "" {
			emptyStreak++
			if emptyStreak >= 3 {
				break
			}

			continue
		}
		emptyStreak = 0

		if strings.Contains(refGoNumber, "Забор") {
			retrievesCount++

			continue
		}

		order := RefGoCheckAgainstOrder{RefGoNumber: refGoNumber}
		if v := cellString(row, 12); v != "" {
			order.CashFact = cellToKopecks(v)
		}
		if v := cellString(row, 13); v != "" {
			order.TerminalFact = cellToKopecks(v)
		}
		order.DeliveryPrice = cellToKopecks(cellString(row, 14))
		order.AdditionalPayments = cellToKopecks(cellString(row, 19))
		order.ReducedTimeIntervalPayments = cellToKopecks(cellString(row, 20))
		order.TaxFact = cellToKopecks(cellString(row, 21))

		orders[int64(i)+1] = order
	}

	return retrievesCount, orders, nil
}

// cellString возвращает содержимое ячейки без пробелов по краям.
func cellString(row []string, col int) string {
	if col >= len(row) {
		return ""
	}

	return strings.TrimSpace(row[col])
}

// cellToKopecks приводит число из ячейки к копейкам: «100,00» → 10000.
// Нечисловые значения и пустые ячейки дают 0.
func cellToKopecks(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, ",", ".")

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}

	return int64(math.Round(v * 100))
}

// ValidateTaxFact проверяет корректность комиссии перевозчика за приём
// оплаты. cashTax и cardTax — проценты комиссии за наличный и терминальный
// расчёт. Возвращает 0, если расхождение не превышает 0,01 рубля, иначе —
// размер расхождения в рублях.
func (o *RefGoCheckAgainstOrder) ValidateTaxFact(cashTax, cardTax float64) float64 {
	cashTaxComputed := (float64(o.CashFact) / 100 * cashTax) / 100
	terminalTaxComputed := (float64(o.TerminalFact) / 100 * cardTax) / 100

	diff := math.Abs(cashTaxComputed + terminalTaxComputed - float64(o.TaxFact)/100)
	if diff <= 0.01 {
		return 0
	}

	return diff
}

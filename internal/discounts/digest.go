package discounts

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Row — строка отчёта/окна по паре (товар, срок), канал general.
//
// Percent — победившая скидка (см. Resolve), Source — её источник.
// Coeff — коэффициент избытка (> 1) для строк источника «избыток», иначе 0.
// DaysLeft — остаток дней до срока на момент расчёта (печать и правило «≥ 2 дн»).
type Row struct {
	ProductID  string
	Name       string
	BestBefore time.Time
	Percent    int16
	Source     Source
	Coeff      float64
	DaysLeft   int
}

// Digest — собранный отчёт по скидкам: две секции, ручные скидки идут первыми.
type Digest struct {
	Date      time.Time
	Discounts []Row // ручные + по сроку годности
	Surplus   []Row // все строки с источником «избыток»
}

// sourceRanks — группа строки в порядке отчёта: ручные → сроковые → избыточные.
var sourceRanks = map[Source]int{
	SourceManual:  0,
	SourceExpiry:  1,
	SourceSurplus: 2,
}

// sortRank — группа строки в порядке отчёта. Строки без источника (SourceNone)
// и любые будущие источники уезжают в конец: в отчёт они не попадают.
func sortRank(s Source) int {
	if rank, ok := sourceRanks[s]; ok {
		return rank
	}
	return 3
}

// Sort — порядок строк для экрана и дайджеста:
// ручные (срок ↑) → по сроку годности (срок ↑) → избыточные (коэф ↓).
// Сортировка устойчивая: строки с равным ключом сохраняют исходный порядок
// (порядок выборки из репозитория, а внутри лота — порядок срока).
func Sort(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		return Less(rows[i], rows[j])
	})
}

// Less — сравнение строк отчёта: то же правило, что у Sort (ранг источника,
// затем срок ↑ или коэффициент ↓ у избытка). Наружу — для тех, кто сортирует
// свои позиции этим же порядком (план ТГ-слота, slot.go), чтобы правило
// порядка жило в одном месте.
func Less(a, b Row) bool {
	ra, rb := sortRank(a.Source), sortRank(b.Source)
	if ra != rb {
		return ra < rb
	}
	if a.Source == SourceSurplus {
		return a.Coeff > b.Coeff
	}
	return a.BestBefore.Before(b.BestBefore)
}

// BuildDigest — разложить строки в две секции отчёта: «Позиции в скидках»
// (ручные + по сроку) и «Позиции с избытком». Порядок внутри секций задаёт Sort;
// входной срез не меняется (сортируется копия). Строки без источника скидки
// в отчёт не попадают. Дата отчёта (Digest.Date) проставляется вызывающим —
// своих часов пакет не заводит.
func BuildDigest(rows []Row) Digest {
	sorted := make([]Row, len(rows))
	copy(sorted, rows)
	Sort(sorted)
	var d Digest
	for _, r := range sorted {
		if r.Source == SourceSurplus {
			d.Surplus = append(d.Surplus, r)
			continue
		}
		if r.Source == SourceManual || r.Source == SourceExpiry {
			d.Discounts = append(d.Discounts, r)
		}
		// строки без источника скидки (SourceNone) в отчёт не попадают
	}
	return d
}

// Text — текст дайджеста для ТГ-слота и страницы «Скидки».
// Формат фиксирован golden-тестом: заголовок «Дайджест по скидкам · дата»,
// затем секции через пустую строку. Пустая секция печатается одной строкой
// («Позиции в скидках: нет» / «Позиции с избытком: нет») — без заголовка списка.
// Числа: срок — 02.01, процент — «30%», коэффициент — один знак, запятая.
func (d Digest) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Дайджест по скидкам · %s\n\n", d.Date.Format("02.01.2006"))

	if len(d.Discounts) == 0 {
		b.WriteString("Позиции в скидках: нет\n")
	} else {
		b.WriteString("Позиции в скидках:\n")
		for i, r := range d.Discounts {
			fmt.Fprintf(&b, "%d. %s (до %s) — %d%%\n", i+1, r.Name, r.BestBefore.Format("02.01"), r.Percent)
		}
	}
	b.WriteString("\n")

	if len(d.Surplus) == 0 {
		b.WriteString("Позиции с избытком: нет\n")
	} else {
		fmt.Fprintf(&b, "Позиции с избытком (Доступны для допродажи со скидкой %d %%):\n", SurplusPercent())
		for i, r := range d.Surplus {
			fmt.Fprintf(&b, "%d. %s (до %s) — %d%% (коэф %s)\n",
				i+1, r.Name, r.BestBefore.Format("02.01"), r.Percent, FormatCoeff(r.Coeff))
		}
	}
	return b.String()
}

// FormatCoeff — коэффициент избытка одним знаком после запятой с запятой как
// десятичным разделителем (как остальные числа отчёта). Экспорт — чтобы страница
// «Скидки» печатала коэффициент тем же форматом, а не своей копией правила.
func FormatCoeff(v float64) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', 1, 64), ".", ",", 1)
}

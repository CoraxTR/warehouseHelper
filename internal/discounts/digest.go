package discounts

import (
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Row — строка отчёта/окна по паре (товар, срок), канал general.
//
// Percent — скидка строки (Source — её источник). Что именно в Percent, решает
// тот, кто собирает строку: по кандидату дня (окно страницы «Скидки», очередь
// допродажи, подсветка «Сроков») или по факту из БД (дайджест: скидка дня, до
// которой ещё не дошёл план, в отчёт не попадает — решение владельца 29.09.2026).
// Coeff — коэффициент избытка (> 1) для строк источника «избыток», иначе 0.
// DaysLeft — остаток дней до срока на момент расчёта (печать и правило «≥ 2 дн»).
//
// SurplusGroup — пара входит в группу избытка товара (в избытке она сама или
// более далёкий срок): скидка по избытку ложится и на неё. Dates заполняет
// BuildDigest у строки-группы — это перечисление сроков группы; у обычной строки
// дата одна (BestBefore).
//
// Telegram — скидка действует из ТГ-колонки: её значение строго выше
// эффективной скидки сайта (сайт ещё не догнал — подписчики видят больше).
// Как только подъём (16:00) доведёт general до плана, флаг снимается сам, и
// строка печатается своим источником ((Срок)/(Избыток)/(Ручная)) — решение
// владельца 24.09.2026.
//
// Qty — количество, которое попало в скидку (шт): у ручных и сроковых пар —
// остаток пары, у избыточных — план продаж по паре (сколько надо продать).
// 0 — печатать количество нечем (избыточная пара вне раскладки).
type Row struct {
	ProductID  string
	Name       string
	BestBefore time.Time
	Percent    int16
	Source     Source
	// Permanent — у пары действует постоянная скидка (поставщика,
	// products.permanent_discount): позиция стоит на сайте, но место в окне
	// НЕ занимает (решение владельца 03.11.2026). Окно пропускает такие строки
	// мимо счётчика ёмкости. Признак ставится по наличию постоянной у пары, а
	// не по метке источника строки: ступень по сроку выше постоянной меняет
	// значение, но не делает позицию слотовой (иначе слот занят фантомом).
	Permanent    bool
	Telegram     bool
	Coeff        float64
	DaysLeft     int
	SurplusGroup bool
	Dates        []time.Time
	Qty          int64
}

// Digest — собранный отчёт по скидкам: активные позиции и «доступно для
// допродажи». Sections активных не больше ёмкости (Cap) — активные и есть окно
// ёмкости, остальное менеджеры держат в уме («устная» скидка), решение владельца
// 23.09.2026.
type Digest struct {
	Date      time.Time
	Discounts []Row // активные: ручные, по сроку и группы избытка — по приоритету, не больше Cap
	Surplus   []Row // за ёмкостью: «доступно для допродажи» (и сроковые, и избыточные)
	Cap       int   // ёмкость активных, с которой собран отчёт (печать заголовка)
}

// sourceRanks — группа строки в порядке отчёта: ручные → сроковые → избыточные.
var sourceRanks = map[Source]int{
	SourceManual:         0,
	SourceTelegramManual: 0, // ручная ТГ — тоже решение менеджера (29.09.2026)
	SourceExpiry:         1,
	SourceSurplus:        2,
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
	slices.SortStableFunc(rows, Compare)
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

// Compare — Less в форме компаратора для slices.SortFunc/SortStableFunc:
// −1/0/+1. Тот же порядок, что у Sort: ранг источника, срок ↑ или коэф ↓.
func Compare(a, b Row) int {
	switch {
	case Less(a, b):
		return -1
	case Less(b, a):
		return 1
	default:
		return 0
	}
}

// BuildDigest — разложить строки в две секции отчёта: активные (не больше
// capacity строк, по одной на товар) и «доступно для допродажи» — всё остальное
// в порядке приоритета. Порядок внутри секций задаёт Sort; входной срез не
// меняется (сортируется копия). Строки без источника скидки в отчёт не попадают.
// Группы избытка (Row.SurplusGroup) свёрнуты в одну позицию с перечислением
// сроков — группа занимает ОДИН слот ёмкости, а скидка в строке — максимальная по
// группе (она у ближайшего срока), решение владельца 23.09.2026.
//
// Ёмкость — 12 строк = 12 товаров (решение владельца 25.11.2026): второй лот того
// же товара в актуальные не попадает, а ждёт в дополнительном списке своей
// строкой и поднимается по приоритету, когда ближний лот продастся. capacity <= 0
// — ёмкость не ограничена. Подъём следующей позиции из «допродажи» в активные
// отдельного кода не требует: состав секций пересчитывается по приоритету каждый
// раз. Дата отчёта (Digest.Date) проставляется вызывающим — своих часов пакет не
// заводит.
func BuildDigest(rows []Row, capacity int) Digest {
	return digestOf(WindowRows(rows), capacity)
}

// WindowRows — позиции окна ёмкости без правила «одна карточка товара»: группы
// избытка свёрнуты, постоянные едут сверх окна. Ёмкость здесь не режется —
// её применяет вызывающий (digestOf для отчёта, windowOf для окна реестра).
// Нужен там, где вопрос «что в работе», а не «что стоит в актуальных»:
// подсветка страницы «Сроки» (Registry.ActiveLots раскрывает группу в пары).
func WindowRows(rows []Row) []Row {
	active := mergeGroups(rows)
	Sort(active)

	return active
}

// digestOf — разложить позиции по двум секциям: активные (не больше capacity
// строк, по одной на товар) и «доступно для допродажи» — всё остальное в порядке
// приоритета: пары, не влезшие в ёмкость, и прочие лоты товара, у которого
// позиция в актуальных уже есть (решение владельца 25.11.2026: ёмкость — 12
// строк = 12 товаров, а дальние лоты ждут в дополнительном списке и поднимаются
// по приоритету, когда ближний лот продаётся).
//
// Постоянные скидки место в окне не занимают (решение владельца 03.11.2026):
// ёмкость съедают только слотовые строки, постоянные едут сверх окна — они и так
// стоят на сайте, и вытеснять ими лестницу или избыток нельзя.
func digestOf(rows []Row, capacity int) Digest {
	slots, permanent := SplitPermanent(rows)

	d := Digest{Cap: capacity}
	taken := make([]bool, len(slots))
	seen := make(map[string]struct{}, len(slots))

	for i, r := range slots {
		if capacity > 0 && len(d.Discounts) >= capacity {
			break
		}
		if _, ok := seen[r.ProductID]; ok {
			continue // товар уже в актуальных — этот лот ждёт в дополнительном
		}
		seen[r.ProductID] = struct{}{}
		taken[i] = true
		d.Discounts = append(d.Discounts, r)
	}

	for i, r := range slots {
		if taken[i] {
			continue
		}
		d.Surplus = append(d.Surplus, r)
	}
	d.Discounts = append(d.Discounts, permanent...)

	return d
}

// SplitPermanent — строки отчёта на слотовые и постоянные. Порядок внутри
// каждой группы сохраняется (вход уже отсортирован), поэтому постоянные идут
// хвостом окна своим приоритетом.
func SplitPermanent(rows []Row) (slots, permanent []Row) {
	slots = make([]Row, 0, len(rows))
	permanent = make([]Row, 0)
	for _, r := range rows {
		if r.Permanent {
			permanent = append(permanent, r)
			continue
		}
		slots = append(slots, r)
	}

	return slots, permanent
}

// mergeGroups — строки отчёта, где пары одного товара из его группы избытка
// свёрнуты в одну позицию. Строке-группе источник ставится «избыток» (она и есть
// позиция по избытку) — это же и её место в приоритете отчёта.
func mergeGroups(rows []Row) []Row {
	out := make([]Row, 0, len(rows))
	groupAt := make(map[string]int)

	for _, r := range rows {
		// Строки без источника скидки (SourceNone) в отчёт не попадают.
		if r.Source == SourceNone {
			continue
		}
		// В группу идут только строки, у которых избыток — победившая скидка
		// (Row.SurplusGroup ставит расчёт именно таким парам, здесь — та же
		// проверка на всякий случай): строка со ступенью по сроку не
		// сворачивается с избыточной и не получает чужой источник.
		if !r.SurplusGroup || r.Source != SourceSurplus {
			out = append(out, r)
			continue
		}
		if i, ok := groupAt[r.ProductID]; ok {
			mergeIntoGroup(&out[i], r)
			continue
		}
		g := r
		g.Source = SourceSurplus
		g.Dates = []time.Time{r.BestBefore}
		groupAt[r.ProductID] = len(out)
		out = append(out, g)
	}

	for i := range out {
		if len(out[i].Dates) > 1 {
			slices.SortFunc(out[i].Dates, func(a, b time.Time) int { return a.Compare(b) })
		}
	}
	return out
}

// mergeIntoGroup — свести пару группы в строку-группу: ключом строки остаётся
// самый близкий срок, скидка — максимальная по группе, коэффициент — наибольший
// (им избыток и меряется).
func mergeIntoGroup(g *Row, r Row) {
	g.Dates = append(g.Dates, r.BestBefore)
	// Количество строки-группы — её план продаж целиком: сумма планов пар
	// (сколько надо продать, чтобы коэффициент группы стал < 1).
	g.Qty += r.Qty
	if r.BestBefore.Before(g.BestBefore) {
		g.BestBefore = r.BestBefore
		g.DaysLeft = r.DaysLeft
	}
	if r.Percent > g.Percent {
		g.Percent = r.Percent
	}
	if r.Coeff > g.Coeff {
		g.Coeff = r.Coeff
	}
}

// Text — текст дайджеста для ТГ-слота и страницы «Скидки».
// Формат фиксирован golden-тестом: заголовок «Дайджест по скидкам · дата»,
// затем секции через пустую строку. Пустая секция печатается одной строкой
// («Позиции в скидках: нет» / «Доступно для допродажи: нет») — без заголовка
// списка. Строка позиции: «1. (ТГ) Название (9 шт до 22.11) — 30% (коэф 1,1)».
// Метка канала — (ТГ)/(Срок)/(Избыток)/(Ручная); количество печатается, когда
// известно (нет — скобка идёт сразу со сроком). Числа: сроки — 02.01 (у группы
// перечисление), процент — «30%», коэффициент — один знак, запятая.
func (d Digest) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Дайджест по скидкам · %s\n\n", d.Date.Format("02.01.2006"))

	if len(d.Discounts) == 0 {
		b.WriteString("Позиции в скидках: нет\n")
	} else {
		b.WriteString("Позиции в скидках:\n")
		for i, r := range d.Discounts {
			b.WriteString(rowLine(i+1, r))
		}
	}
	b.WriteString("\n")

	if len(d.Surplus) == 0 {
		b.WriteString("Доступно для допродажи: нет\n")
	} else {
		fmt.Fprintf(&b, "Доступно для допродажи (сверх %d активных):\n", d.Cap)
		for i, r := range d.Surplus {
			b.WriteString(rowLine(i+1, r))
		}
	}
	return b.String()
}

// MessageMarkdown — текст дайджеста для rich-сообщения (sendRichMessage,
// markdown): заголовок H1 и таблица позиций вместо простыни строк.
//
// Секции «Доступно для допродажи» здесь нет (решение владельца 25.11.2026):
// менеджеры ставят на сайт только активные позиции, а «скидка в памяти» живёт
// на странице «Скидки» (там секции и так таблицами). Text() остаётся для
// страницы и не меняется.
//
// Разметка — rich markdown Telegram: HTML-таблица с рамками (атрибуты bordered,
// compact) и экранированные названия: в rich-режиме названия разбираются как
// HTML, а «Z&P» в имени товара иначе ломает сообщение.
func (d Digest) MessageMarkdown() string {
	var b strings.Builder
	b.WriteString("# Актуальные скидки для сайта\n\n")

	if len(d.Discounts) == 0 {
		b.WriteString("*Скидок нет*\n")

		return b.String()
	}

	fmt.Fprintf(&b, "*%s · %d %s*\n\n", d.Date.Format("02.01.2006"), len(d.Discounts), positionsWord(len(d.Discounts)))
	b.WriteString("<table bordered compact>\n")
	b.WriteString("<tr><th>Товар</th><th>Срок</th><th>шт</th><th>Скидка</th><th>Канал</th></tr>\n")
	for _, r := range d.Discounts {
		qty := "—"
		if r.Qty > 0 {
			qty = strconv.FormatInt(r.Qty, 10)
		}
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td><td><b>%d %%</b></td><td>%s</td></tr>\n",
			html.EscapeString(r.Name), DatesText(r), qty, r.Percent, channelLabel(r))
	}
	b.WriteString("</table>\n")

	return b.String()
}

// positionsWord — «1 позиция / 2 позиции / 5 позиций»: число в шапке сообщения
// читают люди, а не машины.
func positionsWord(n int) string {
	mod100, mod10 := n%100, n%10
	switch {
	case mod100 >= 11 && mod100 <= 14:
		return "позиций"
	case mod10 == 1:
		return "позиция"
	case mod10 >= 2 && mod10 <= 4:
		return "позиции"
	default:
		return "позиций"
	}
}

// rowLine — строка позиции отчёта: номер, метка канала, название, количество и
// срок в скобках, скидка, у избытка — коэффициент.
func rowLine(n int, r Row) string {
	qty := ""
	if r.Qty > 0 {
		qty = fmt.Sprintf("%d шт ", r.Qty)
	}
	return fmt.Sprintf("%d. (%s) %s (%s%s) — %d%%%s\n",
		n, channelLabel(r), r.Name, qty, DatesText(r), r.Percent, coeffText(r))
}

// channelLabel — метка канала/источника строки: (ТГ) — скидка живёт в ТГ-колонке
// (сайт ещё не догнал), иначе источник скидки сайта. Ручную ТГ отдельно не
// подписываем: владельцу важно поведение скидки, а не её происхождение
// (решение 29.09.2026).
func channelLabel(r Row) string {
	if r.Telegram {
		return "ТГ"
	}
	switch r.Source {
	case SourceNone:
		return "" // строка без источника скидки в отчёт не попадает
	case SourceManual:
		return "Ручная"
	case SourceTelegramManual:
		return "ТГ" // ручная ТГ на сайте ещё не стоит — значение в ТГ-колонке
	case SourceExpiry:
		return "Срок"
	case SourceSurplus:
		return "Избыток"
	case SourcePermanent:
		// Постоянная скидка поставщика: в окне её нет, но в отчёте и в подписи
		// строки её надо отличать от лестницы и избытка (03.11.2026).
		return "Постоянная"
	}
	return "" // недостижимо: все значения Source перечислены выше
}

// DatesText — сроки строки для печати: у группы избытка перечисление
// («до 15.10, 22.10»), у обычной строки один срок. Экспорт — чтобы страница
// «Скидки» печатала сроки тем же правилом, что дайджест.
func DatesText(r Row) string {
	if len(r.Dates) == 0 {
		return "до " + r.BestBefore.Format("02.01")
	}
	parts := make([]string, 0, len(r.Dates))
	for _, dt := range r.Dates {
		parts = append(parts, dt.Format("02.01"))
	}
	return "до " + strings.Join(parts, ", ")
}

// coeffText — хвост строки с коэффициентом: он есть только у избыточных позиций
// (в т.ч. у строки-группы).
func coeffText(r Row) string {
	if r.Source != SourceSurplus {
		return ""
	}
	return " (коэф " + FormatCoeff(r.Coeff) + ")"
}

// FormatCoeff — коэффициент избытка одним знаком после запятой с запятой как
// десятичным разделителем (как остальные числа отчёта). Экспорт — чтобы страница
// «Скидки» печатала коэффициент тем же форматом, а не своей копией правила.
func FormatCoeff(v float64) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', 1, 64), ".", ",", 1)
}

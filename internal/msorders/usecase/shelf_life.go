// Команда /sroki: текст сроков годности по номеру заказа.
//
// Поток: один GET в МС (поиск заказа по номеру) → строки журнала подбора из
// своей таблицы → средние веса штучных из каталога → сборка текста. Сам текст
// собирает чистая функция buildShelfLifeText: её и проверяют табличные тесты,
// сценарий (поиск, швы, отправка) — на фейках.
//
// Журнал хранит даты выработки и срока годности подобранных единиц: позиции без
// строк журнала в ответ НЕ попадают (решение владельца) — в тексте только те
// партии, что реально лежат в журнале.
package usecase

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/msorders"
)

// ErrEmptyOrderNumber — команда /sroki без номера заказа (после обрезки пробелов).
var ErrEmptyOrderNumber = errors.New("укажите номер заказа: /sroki 19379")

// errJournalNotConnected — шов журнала подбора не подключён (сборка без di.go):
// дат взять неоткуда, но команда не должна падать — отвечаем понятной ошибкой.
var errJournalNotConnected = errors.New("журнал подбора не подключён")

// telegramLimit — лимит текста одного сообщения Telegram (символов): длиннее —
// режем по целым группам и дописываем сноску со числом убранных групп.
const telegramLimit = 4096

// ShelfLifeText — текст ответа на команду /sroki <номер заказа>: сроки годности
// подобранных единиц заказа по журналу подбора, сгруппированные по товару и
// партии (код склада + выработка + срок годности).
//
// Номер заказа каждый год начинается заново, поэтому из одноимённых заказов
// берётся самый свежий по Moment. Заказ не найден или журнал пуст — короткий
// текст «данных нет» без ошибки: команда отвечает владельцу, а не роняет бота.
func (uc *UseCase) ShelfLifeText(ctx context.Context, number string) (string, error) {
	done := metrics.Track(trackPkg, "ShelfLife")
	defer done()

	number = strings.TrimSpace(number)
	if number == "" {
		return "", ErrEmptyOrderNumber
	}

	orders, err := uc.ms.SearchCustomerOrdersByName(ctx, number)
	if err != nil {
		return "", fmt.Errorf("поиск заказа %s: %w", number, err)
	}
	if len(orders) == 0 {
		return shelfLifeNoData(number), nil
	}

	// Свежие сверху: МС не гарантирует порядок при фильтре по номеру.
	sortByMomentDesc(orders)
	order := orders[0]

	if uc.journal == nil {
		return "", errJournalNotConnected
	}

	units, err := uc.journal.OrderPickingByOrder(ctx, order.ID)
	if err != nil {
		return "", fmt.Errorf("журнал подбора заказа %s: %w", order.ID, err)
	}
	if len(units) == 0 {
		return shelfLifeNoData(number), nil
	}

	weights := uc.pieceAverageWeights(ctx, units)

	return buildShelfLifeText(order.Name, order.Moment, units, weights), nil
}

// ReplyShelfLife — ответ на /sroki в чат отправителя команды (шов ChatSender).
//
// Прав не проверяем — команду принимает бот, любой участник вправе увидеть
// сроки. Отправлять некуда (шов не подключён) — текст уходит в лог, приём как
// в discounts.ReplyDigest: команда не должна падать из-за конфига.
func (uc *UseCase) ReplyShelfLife(ctx context.Context, chatID int64, number string) error {
	text, err := uc.ShelfLifeText(ctx, number)
	if err != nil {
		return err
	}

	if uc.chat == nil {
		slog.Info(fmt.Sprintf("msorders: ответ на /sroki в чат %d (канал не подключён): %s", chatID, text))

		return nil
	}

	if err := uc.chat.SendDetails(ctx, chatID, text); err != nil {
		return fmt.Errorf("ответ на /sroki в чат %d: %w", chatID, err)
	}

	return nil
}

// pieceAverageWeights — средние веса штучных единиц заказа (products.id → кг):
// нужны для общего веса групп. Весовые не запрашиваются — их вес уже в кг, —
// как и строки без товара каталога (product_id пуст).
//
// Чтение вторичное: ошибка каталога не роняет ответ, текст просто уйдёт без
// веса штучных групп (клиент залогировал).
func (uc *UseCase) pieceAverageWeights(ctx context.Context, units []msorders.PickingUnit) map[string]float64 {
	if uc.catalog == nil {
		return nil
	}

	ids := make([]string, 0, len(units))
	seen := make(map[string]struct{}, len(units))

	for _, u := range units {
		if u.Weighted || u.ProductID == "" {
			continue
		}
		if _, ok := seen[u.ProductID]; ok {
			continue
		}

		seen[u.ProductID] = struct{}{}

		ids = append(ids, u.ProductID)
	}

	if len(ids) == 0 {
		return nil
	}

	weights, err := uc.catalog.LoadProductAverageWeights(ctx, ids)
	if err != nil {
		slog.Error("msorders: средние веса штучных для /sroki не получены", "err", err)

		return nil
	}

	return weights
}

// shelfLifeNoData — ответ, когда заказа в МС нет или журнал пуст.
func shelfLifeNoData(number string) string {
	return fmt.Sprintf("По заказу %s данных о сроках нет", number)
}

// buildShelfLifeText собирает текст ответа /sroki: шапка с номером и датой
// заказа, затем группы подобранных единиц. Пустой журнал — «данных нет».
func buildShelfLifeText(number, moment string, units []msorders.PickingUnit, avgWeights map[string]float64) string {
	groups := shelfLifeGroups(units)
	if len(groups) == 0 {
		return shelfLifeNoData(number)
	}

	blocks := make([]string, len(groups))
	for i := range groups {
		blocks[i] = shelfLifeGroupText(groups[i], avgWeights)
	}

	return shelfLifeFit(shelfLifeHeader(number, moment), groups, blocks)
}

// shelfLifeHeader — шапка текста: «Заказ 19379 от 25.09.2026». Даты в МС нет —
// шапка только с номером.
func shelfLifeHeader(number, moment string) string {
	if date := shortDate(moment); date != dash {
		return fmt.Sprintf("Заказ %s от %s", number, date)
	}

	return "Заказ " + number
}

// shelfLifeFit — текст в пределах лимита Telegram: длиннее — режем по целым
// группам с конца и дописываем сноску «… (обрезано: ещё N групп)».
func shelfLifeFit(header string, groups []shelfLifeGroup, blocks []string) string {
	if text := shelfLifeJoin(header, groups, blocks); utf8.RuneCountInString(text) <= telegramLimit {
		return text
	}

	for keep := len(blocks) - 1; keep >= 0; keep-- {
		cropped := shelfLifeJoin(header, groups[:keep], blocks[:keep]) +
			fmt.Sprintf("\n… (обрезано: ещё %d групп)", len(blocks)-keep)
		if utf8.RuneCountInString(cropped) <= telegramLimit {
			return cropped
		}
	}

	// Даже шапка не влезла: отдаём её со сноской (текст группы не рвём).
	return header + fmt.Sprintf("\n… (обрезано: ещё %d групп)", len(blocks))
}

// shelfLifeJoin — шапка, пустая строка, блоки групп: разные товары (код склада)
// разделяются пустой строкой, группы одного товара идут подряд (см. образец).
func shelfLifeJoin(header string, groups []shelfLifeGroup, blocks []string) string {
	var b strings.Builder

	b.WriteString(header)
	b.WriteString("\n\n")

	for i := range blocks {
		if i > 0 {
			b.WriteString("\n")
			if groups[i-1].code != groups[i].code {
				b.WriteString("\n")
			}
		}

		b.WriteString(blocks[i])
	}

	return b.String()
}

// shelfLifeGroup — группа строк журнала одного товара одной партии: ключ — код
// склада + выработка + срок годности. Название товара печатается перед каждой
// группой (снимок на момент подбора берём у первой единицы группы).
type shelfLifeGroup struct {
	code       string
	name       string
	producedOn *time.Time
	bestBefore time.Time
	units      []msorders.PickingUnit
}

// shelfLifeGroups — раскладка единиц журнала по группам: порядок единиц внутри
// группы — порядок журнала, группы — по коду склада (одинаковые товары подряд),
// затем по сроку годности (ближайший первым), затем по выработке.
func shelfLifeGroups(units []msorders.PickingUnit) []shelfLifeGroup {
	index := make(map[string]int, len(units))
	groups := make([]shelfLifeGroup, 0, len(units))

	for _, u := range units {
		key := u.InternalCode + "\x00" + shelfLifeProducedKey(u.ProducedOn) + "\x00" +
			u.BestBefore.Format(time.RFC3339)

		if i, ok := index[key]; ok {
			groups[i].units = append(groups[i].units, u)

			continue
		}

		index[key] = len(groups)

		groups = append(groups, shelfLifeGroup{
			code:       u.InternalCode,
			name:       u.ProductName,
			producedOn: u.ProducedOn,
			bestBefore: u.BestBefore,
			units:      []msorders.PickingUnit{u},
		})
	}

	slices.SortStableFunc(groups, func(a, b shelfLifeGroup) int {
		if c := cmp.Compare(a.code, b.code); c != 0 {
			return c
		}
		if c := a.bestBefore.Compare(b.bestBefore); c != 0 {
			return c
		}

		return shelfLifeProducedTime(a.producedOn).Compare(shelfLifeProducedTime(b.producedOn))
	})

	return groups
}

// shelfLifeGroupText — блок группы: название товара и три строки под ним (даты с
// весами единиц, общий вес, количество). Строка веса может отсутствовать.
func shelfLifeGroupText(g shelfLifeGroup, avgWeights map[string]float64) string {
	var b strings.Builder

	b.WriteString(g.name)
	b.WriteString("\n  ")
	b.WriteString(shelfLifeDatesText(g))
	b.WriteString(" · ")
	b.WriteString(shelfLifeUnitsMeasure(g))

	if weight, ok := shelfLifeGroupWeight(g, avgWeights); ok {
		b.WriteString("\n  Общий вес группы: ")
		b.WriteString(weight)
	}

	b.WriteString("\n  Кол-во в группе: ")
	b.WriteString(strconv.Itoa(len(g.units)))
	b.WriteString(" шт")

	return b.String()
}

// shelfLifeDatesText — «10.09.2026 → 17.09.2026»; выработка не известна (NULL) —
// «— → 17.09.2026».
func shelfLifeDatesText(g shelfLifeGroup) string {
	produced := dash
	if g.producedOn != nil {
		produced = g.producedOn.Format("02.01.2006")
	}

	return produced + " → " + g.bestBefore.Format("02.01.2006")
}

// shelfLifeUnitsMeasure — правая часть строки дат: весовые — веса единиц через
// « | » («0,657 | 0,712 кг», «кг» один раз в конце), штучные — «3 шт».
func shelfLifeUnitsMeasure(g shelfLifeGroup) string {
	if len(g.units) > 0 && g.units[0].Weighted {
		weights := make([]string, 0, len(g.units))
		for _, u := range g.units {
			weights = append(weights, kgText(u.WeightKg))
		}

		return strings.Join(weights, " | ") + " кг"
	}

	return strconv.Itoa(len(g.units)) + " шт"
}

// shelfLifeGroupWeight — строка «Общий вес группы». Весовые — сумма весов единиц
// (вес кусков в журнале точный). Штучные — сумма средних весов единиц со знаком
// «≈»; среднего веса нет — строку не печатаем (false).
func shelfLifeGroupWeight(g shelfLifeGroup, avgWeights map[string]float64) (string, bool) {
	if len(g.units) == 0 {
		return "", false
	}

	if g.units[0].Weighted {
		var sum float64
		for _, u := range g.units {
			sum += u.WeightKg
		}

		return kgText(sum) + " кг", true
	}

	var (
		sum   float64
		known bool
	)

	for _, u := range g.units {
		if u.ProductID == "" {
			continue
		}

		w, ok := avgWeights[u.ProductID]
		if !ok {
			continue
		}

		sum += w
		known = true
	}

	if !known {
		return "", false
	}

	return "≈ " + kgText(sum) + " кг", true
}

// kgText — килограммы в тексте /sroki: три знака после запятой, хвостовые нули
// срезаются, разделитель — запятая («0,657»). Единица измерения дописывается
// вызывающим: в строке дат весовых «кг» стоит один раз в конце перечисления.
func kgText(kg float64) string {
	s := strings.TrimRight(strconv.FormatFloat(kg, 'f', 3, 64), "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}

	return strings.Replace(s, ".", ",", 1)
}

// shelfLifeProducedKey — ключ группировки по выработке (nil — «не известна»).
func shelfLifeProducedKey(t *time.Time) string {
	if t == nil {
		return ""
	}

	return t.Format(time.RFC3339)
}

// shelfLifeProducedTime — выработка для сортировки: неизвестная идёт раньше
// известных (группа «не известна» — нулевое время).
func shelfLifeProducedTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}

	return *t
}

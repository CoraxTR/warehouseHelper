// Письмо складу — второе сообщение 14:00: позиции ТГ-рассылки готовым текстом
// для подписчиков (запрос владельца 15.10.2026; первое сообщение — окно скидок
// с метками каналов, см. BuildDigest).
//
// Одна позиция плана — один блок: номер, название ссылкой на карточку, цена и
// скидка, срок годности, доступное количество. Разные сроки одного товара дают
// разные блоки: раскладку по срокам уже сделал план дня (добор из избытка —
// FIFO, см. SurplusSalePlan), каждая пара раскладки в нём своя позиция.
package discounts

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Шапка, примечание и две ссылки хвоста письма — данные владельца (15.10.2026).
// Значения меняются редко, поэтому живут рядом с форматом текста, а не в
// настройках.
const (
	mailingHeader = "Привет, друзья! Вот что мы приготовили на сегодня:"
	// mailingStockNote — строка про остаток по акции: поле сайта на момент
	// публикации проставится равным «Доступно по акции», отдельного числа у нас
	// нет.
	mailingStockNote = `Остаток по акции: На момент публикации будет = "Доступно по акции"`
	// mailChannelURL — канал рассылок; в тексте печатается сам адрес.
	mailChannelURL = "https://t.me/steakhomeru"
	// mailSpecialURL — страница акций: в тексте короткий адрес, в ссылке полный
	// (с www и слешем на конце).
	mailSpecialURL  = "https://www.steakhome.ru/catalog/special/"
	mailSpecialText = "steakhome.ru/catalog/special"
	// mailUTM — метка источника ссылки карточки: переход из ТГ-рассылки.
	mailUTM = "utm_source=tg_sale"
)

// MailingItem — позиция письма: одна пара (товар, срок) плана дня. Скидка и
// количество в позиции положительные (их дают правила слота), поэтому блок
// печатается всегда — цены может не быть, но позицию из письма не выбрасываем.
type MailingItem struct {
	Name       string
	SiteURL    string // products.site_url (ручное поле); пусто — название без ссылки
	UOM        string // единица измерения (кг/шт); пусто — цена без «/»
	SalePrice  *int64 // цена продажи из МС, копейки; nil — МС цену не отдала
	Percent    int16  // скидка позиции (значение ТГ-колонки = скидка дня)
	Qty        int64  // сколько идёт в ТГ по этой паре
	BestBefore time.Time
}

// BuildMailing собирает текст письма: шапка, блоки позиций по порядку, хвост с
// двумя ссылками. Пустой список — пустая строка: сообщения нет, и вызывающему
// нечего отправлять.
func BuildMailing(items []MailingItem) string {
	if len(items) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(mailingHeader)
	b.WriteString("\n\n")
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n") // пустая строка между блоками
		}
		b.WriteString(mailingBlock(i+1, it))
	}
	fmt.Fprintf(&b, "\nНа сегодня это все. Для заказа пишите %s\n", mailLink(mailChannelURL, mailChannelURL))
	fmt.Fprintf(&b, "Все актуальные предложения здесь %s\n", mailLink(mailSpecialURL, mailSpecialText))

	return b.String()
}

// mailingBlock — блок позиции: номер, название со ссылкой и цена со скидкой,
// срок годности, доступное количество и примечание про остаток по акции.
func mailingBlock(n int, it MailingItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d. %s | %s\n", n, mailingName(it), mailingPrice(it))
	fmt.Fprintf(&b, "Срок годности: %s\n", it.BestBefore.Format("02.01"))
	fmt.Fprintf(&b, "Доступно по акции: %d шт.\n", it.Qty)
	b.WriteString(mailingStockNote)
	b.WriteString("\n")

	return b.String()
}

// mailingPrice — цена в первой строке блока: полная цена, скидка и цена со
// скидкой. Цены в карточке нет (МС не отдала или ноль) — печатаем одну скидку:
// блок позиции в письме остаётся (решение владельца 15.10.2026).
func mailingPrice(it MailingItem) string {
	if it.SalePrice == nil || *it.SalePrice <= 0 {
		return fmt.Sprintf("%d%%", it.Percent)
	}
	price := *it.SalePrice

	return fmt.Sprintf("%s - %d%% => %s",
		priceText(price, it.UOM), it.Percent,
		priceText(discountPrice(price, it.Percent), it.UOM))
}

// mailingName — название позиции: ссылкой на карточку, если адрес известен, иначе
// обычным текстом. Экранирует разметку сам ссылочный хелпер (mailLink): канал
// склада отправляет сообщение parse_mode=HTML, и незакрытый «&» в названии товара
// сломал бы разбор всего сообщения. Двойного экранирования быть не должно —
// иначе подписчик увидит «&amp;».
func mailingName(it MailingItem) string {
	if url := mailingURL(it.SiteURL); url != "" {
		return mailLink(url, it.Name)
	}

	return html.EscapeString(it.Name)
}

// mailingURL — адрес карточки с меткой источника. Пусто или без схемы http/https
// — ссылки нет: относительный адрес в Telegram не откроется, и название без
// ссылки лучше битой ссылки.
func mailingURL(siteURL string) string {
	url := strings.TrimSpace(siteURL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ""
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}

	return url + sep + mailUTM
}

// mailLink — ссылка HTML-разметки: адрес и текст экранируются ЗДЕСЬ (в адресе
// бывают «&» — параметры запроса карточки; в названии товара — «&», «<»). Текст
// передаётся сырым: экранирование в вызывающем коде дало бы «&amp;amp;».
func mailLink(url, text string) string {
	return `<a href="` + html.EscapeString(url) + `">` + html.EscapeString(text) + `</a>`
}

// FormatPrice — цена в рублях с копейками: копейки → «1249,50» (запятая —
// десятичный разделитель, как во всех числах модуля). Цены в БД неотрицательны
// (CHECK в схеме), знак не печатается.
func FormatPrice(kopecks int64) string {
	return fmt.Sprintf("%d,%02d", kopecks/100, kopecks%100)
}

// priceText — цена с единицей измерения: «1249,50 руб./кг.». Единицы нет — цена
// без «/» («1249,50 руб.»).
func priceText(kopecks int64, uom string) string {
	if uom == "" {
		return FormatPrice(kopecks) + " руб."
	}

	return FormatPrice(kopecks) + " руб./" + uom + "."
}

// discountPrice — цена со скидкой в копейках. Дробную копейку отбрасываем, а не
// округляем (решение владельца 15.10.2026): цена печатается с копейками, и
// подписчик не увидит округлённого вверх числа. Скидка больше 100 % (схема
// держит 0..100) не уводит цену в минус.
func discountPrice(kopecks int64, percent int16) int64 {
	pct := min(int64(percent), 100)

	return kopecks * (100 - pct) / 100
}

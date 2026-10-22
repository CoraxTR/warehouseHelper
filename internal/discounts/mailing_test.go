package discounts

import (
	"strings"
	"testing"
	"time"
)

// Письмо складу (второе сообщение 14:00): точный текст блока и хвоста, поведение
// без цены и без адреса карточки, экранирование разметки. Тексты печатаются
// parse_mode=HTML, поэтому спецсимволы в названии товара и в адресе карточки
// обязаны быть экранированы — иначе Telegram не разберёт сообщение целиком.

func mailingTestDay() time.Time {
	return time.Date(2026, time.November, 13, 0, 0, 0, 0, time.UTC)
}

// Точный текст: шапка, блок с ценой и ссылкой на карточку (метка источника),
// хвост двумя строками. Числа: цена с копейками, запятая-разделитель, срок —
// 02.01.
func TestBuildMailingText(t *testing.T) {
	price := int64(124950)
	got := BuildMailing([]MailingItem{{
		Name:       "Сыр Тома",
		SiteURL:    "https://www.steakhome.ru/catalog/element/syr/",
		UOM:        "кг",
		SalePrice:  &price,
		Percent:    20,
		Qty:        2,
		BestBefore: mailingTestDay(),
	}})

	want := `Привет, друзья! Вот что мы приготовили на сегодня:

1. <a href="https://www.steakhome.ru/catalog/element/syr/?utm_source=tg_sale">Сыр Тома</a> | 1249,50 руб./кг. - 20% => 999,60 руб./кг.
Срок годности: 13.11.2026
Доступно по акции: 2 шт.
Остаток по акции: 2 шт.

На сегодня это все. Для заказа пишите <a href="https://t.me/steakhomeru">https://t.me/steakhomeru</a>
Все актуальные предложения здесь <a href="https://www.steakhome.ru/catalog/special/">steakhome.ru/catalog/special</a>
`
	if got != want {
		t.Errorf("письмо складу:\n%s\n\nwant:\n%s", got, want)
	}
}

// Позиции без цены карточка не отдала — блок остаётся, печатается одна скидка
// (решение владельца 15.10.2026). Адреса нет — название без ссылки.
func TestBuildMailingWithoutPriceAndURL(t *testing.T) {
	got := BuildMailing([]MailingItem{{
		Name:       "Колбаса Докторская",
		UOM:        "кг",
		Percent:    20,
		Qty:        7,
		BestBefore: mailingTestDay(),
	}})

	for _, want := range []string{
		"1. Колбаса Докторская | 20%\n",
		"Срок годности: 13.11.2026\n",
		"Доступно по акции: 7 шт.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в письме нет %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "руб.") {
		t.Errorf("цена без данных карточки всё равно напечатана:\n%s", got)
	}
	if strings.Contains(got, "<a href=") && !strings.Contains(got, mailChannelURL) {
		t.Errorf("ссылка на карточку без адреса:\n%s", got)
	}
}

// Порядок блоков и нумерация — как пришёл список: каждая позиция плана (в том
// числе каждый срок одного товара) свой блок под своим номером.
func TestBuildMailingNumbersEveryItem(t *testing.T) {
	price := int64(10000)
	items := make([]MailingItem, 0, 3)
	for i := range 3 {
		items = append(items, MailingItem{
			Name:       "Томагавк",
			UOM:        "шт",
			SalePrice:  &price,
			Percent:    20,
			Qty:        int64(i + 1),
			BestBefore: mailingTestDay().AddDate(0, 0, i),
		})
	}

	got := BuildMailing(items)
	// Курсор по тексту: проверяем и наличие строк, и их порядок.
	prev := 0
	for i, want := range []string{
		"1. Томагавк |",
		"Срок годности: 13.11.2026",
		"Доступно по акции: 1 шт.",
		"2. Томагавк |",
		"Срок годности: 14.11.2026",
		"Доступно по акции: 2 шт.",
		"3. Томагавк |",
		"Срок годности: 15.11.2026",
		"Доступно по акции: 3 шт.",
	} {
		// Курсор: проверяем и наличие, и порядок (блок 2 идёт после блока 1,
		// срок второго — после количества первого).
		idx := strings.Index(got[prev:], want)
		if idx < 0 {
			t.Fatalf("в письме нет %q (шаг %d):\n%s", want, i, got)
		}
		prev += idx + len(want)
	}
}

// Пустой список — пустая строка: сообщения нет (метку дня и отправку решает
// вызывающий).
func TestBuildMailingEmpty(t *testing.T) {
	if got := BuildMailing(nil); got != "" {
		t.Errorf("письмо по пустому плану: %q, want пустую строку", got)
	}
}

// Метка источника: адрес с готовыми параметрами получает «&», «голый» адрес —
// «?». Адрес без схемы ссылкой не становится: относительный адрес в Telegram не
// откроется.
func TestMailingURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"голый адрес", "https://www.steakhome.ru/catalog/element/syr/",
			"https://www.steakhome.ru/catalog/element/syr/?utm_source=tg_sale"},
		{"адрес с параметрами", "https://www.steakhome.ru/catalog/element/syr/?id=7",
			"https://www.steakhome.ru/catalog/element/syr/?id=7&utm_source=tg_sale"},
		{"пробелы по краям", "  https://steakhome.ru/x  ", "https://steakhome.ru/x?utm_source=tg_sale"},
		{"без схемы", "steakhome.ru/x", ""},
		{"пусто", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mailingURL(tc.url); got != tc.want {
				t.Errorf("mailingURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// Разметка: «&», «<» и «>» в названии товара и в адресе карточки экранируются —
// иначе Telegram не разберёт HTML и сообщение не уйдёт.
func TestBuildMailingEscapesMarkup(t *testing.T) {
	price := int64(5990)
	got := BuildMailing([]MailingItem{{
		Name:       "Чай «Ассам» & <листовой>",
		SiteURL:    "https://steakhome.ru/x?a=1&b=2",
		UOM:        "шт",
		SalePrice:  &price,
		Percent:    10,
		Qty:        1,
		BestBefore: mailingTestDay(),
	}})

	// Двойное экранирование («&amp;amp;») — та же ошибка: подписчик увидит
	// «&amp;» вместо «&».
	if strings.Contains(got, "&amp;amp;") || strings.Contains(got, "& <") {
		t.Errorf("разметка не экранирована или экранирована дважды:\n%s", got)
	}
	for _, want := range []string{"Ассам", "&amp;", "&lt;листовой&gt;", "a=1&amp;b=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("в письме нет %q:\n%s", want, got)
		}
	}
}

// Цена со скидкой: копейки печатаются как есть, дробную копейку отбрасываем (не
// округляем — решение владельца 15.10.2026).
func TestDiscountPrice(t *testing.T) {
	tests := []struct {
		name    string
		kopecks int64
		percent int16
		want    int64
	}{
		{"ровные копейки", 124950, 20, 99960},
		{"дробную копейку отбрасываем", 59990, 15, 50991}, // 50991,5
		{"скидка 100 %", 10000, 100, 0},
		{"скидка 0 %", 10000, 0, 10000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := discountPrice(tc.kopecks, tc.percent); got != tc.want {
				t.Errorf("discountPrice(%d, %d) = %d, want %d", tc.kopecks, tc.percent, got, tc.want)
			}
		})
	}
}

// Формат цены: целые рубли не теряются, копейки — двумя знаками, запятая.
func TestFormatPrice(t *testing.T) {
	tests := []struct {
		kopecks int64
		want    string
	}{
		{124950, "1249,50"},
		{124900, "1249,00"},
		{5, "0,05"},
		{0, "0,00"},
	}
	for _, tc := range tests {
		if got := FormatPrice(tc.kopecks); got != tc.want {
			t.Errorf("FormatPrice(%d) = %q, want %q", tc.kopecks, got, tc.want)
		}
	}
}

// Запрос фото обязан сказать складу, что снимок нужен ОТВЕТОМ: force_reply у
// сообщения больше нет (22.10.2026), и без этой фразы фото, отправленное просто
// в чат, к позиции не привязать.
func TestPhotoRequestAsksToReply(t *testing.T) {
	text := PhotoRequest("<Колбаса>", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))

	if !strings.Contains(text, "ОТВЕТОМ") {
		t.Errorf("в запросе нет просьбы ответить: %q", text)
	}
	if strings.Contains(text, "<Колбаса>") || !strings.Contains(text, "&lt;Колбаса&gt;") {
		t.Errorf("название не экранировано: %q", text)
	}
}

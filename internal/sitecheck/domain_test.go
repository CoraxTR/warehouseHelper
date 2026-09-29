package sitecheck

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
)

// feedXML собирает фид в форме сайта: шапка с датой + офферы как есть.
func feedXML(date, offers string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><yml_catalog date="` + date + `"><shop>` +
		`<name>Steak@home</name><url>https://www.steakhome.ru</url>` +
		`<categories><category id="128">Скидки %</category></categories>` +
		`<offers>` + offers + `</offers></shop></yml_catalog>`
}

// offer собирает оффер фида: url, price, oldprice (пусто — без зачёркнутой цены),
// available (пусто — атрибута нет).
func offer(url, price, oldPrice, available string) string {
	s := `<offer id="1"`
	if available != "" {
		s += ` available="` + available + `"`
	}
	s += `><url>` + url + `</url><price>` + price + `</price>`
	if oldPrice != "" {
		s += `<oldprice>` + oldPrice + `</oldprice>`
	}
	s += `<currencyId>RUB</currencyId><categoryId>128</categoryId><name>Позиция</name></offer>`

	return s
}

func TestParseFeed(t *testing.T) {
	body := feedXML("2026-09-29 17:23",
		offer("https://www.steakhome.ru/catalog/element/tenderloin/", "10788", "17980", "true")+
			offer("https://www.steakhome.ru/catalog/element/sertifikat/", "15000", "", "false")+
			offer("https://www.steakhome.ru/catalog/element/sous/", "590", "", "")+
			offer("", "100", "", "true")+
			offer("https://www.steakhome.ru/catalog/element/broken/", "abc", "900", "true"))

	feed, err := ParseFeed([]byte(body))
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}

	want := time.Date(2026, 9, 29, 17, 23, 0, 0, time.Local)
	if !feed.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %s, want %s (время сайта в локальной зоне процесса)", feed.CreatedAt, want)
	}

	// Позиция без url в сверку не идёт вовсе: сравнивать нечего.
	if len(feed.Items) != 4 {
		t.Fatalf("позиций в фиде: %d, want 4 (без позиции без url)", len(feed.Items))
	}

	item := feed.Items[0]
	if item.URL != "https://www.steakhome.ru/catalog/element/tenderloin/" || item.Price != 10788 {
		t.Errorf("первая позиция: %+v", item)
	}
	if item.OldPrice == nil || *item.OldPrice != 17980 {
		t.Errorf("зачёркнутая цена: %v, want 17980", item.OldPrice)
	}
	if !item.Available {
		t.Error("available=true должно читаться как доступное")
	}

	if feed.Items[1].Available {
		t.Error("available=false должно читаться как «нет в торговом предложении»")
	}
	if feed.Items[2].OldPrice != nil {
		t.Errorf("без oldprice скидки нет, получено %v", feed.Items[2].OldPrice)
	}
	if !feed.Items[2].Available {
		t.Error("без атрибута available позиция считается доступной")
	}
	// Битая цена не отменяет позицию, но и скидку по ней не сверяем.
	broken := feed.Items[3]
	if broken.Price != 0 || broken.OldPrice != nil {
		t.Errorf("битая цена: %+v, want price 0 и без скидки", broken)
	}
}

func TestParseFeedBrokenXML(t *testing.T) {
	if _, err := ParseFeed([]byte("<yml_catalog date=")); err == nil {
		t.Fatal("ожидалась ошибка разбора битого XML")
	}
}

func TestParseFeedNoDate(t *testing.T) {
	feed, err := ParseFeed([]byte(feedXML("", offer("https://www.steakhome.ru/catalog/element/x/", "100", "", "true"))))
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if !feed.CreatedAt.IsZero() {
		t.Errorf("без времени составления ожидалось нулевое, получено %s", feed.CreatedAt)
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"как в фиде", "https://www.steakhome.ru/catalog/element/ribeye/", "steakhome.ru/catalog/element/ribeye"},
		{"без www и слеша", "https://steakhome.ru/catalog/element/ribeye", "steakhome.ru/catalog/element/ribeye"},
		{"с utm-метками", "https://www.steakhome.ru/catalog/element/ribeye/?utm_source=ya#top", "steakhome.ru/catalog/element/ribeye"},
		{"http и пробелы", "  http://WWW.Steakhome.ru/catalog/element/ribeye/  ", "steakhome.ru/catalog/element/ribeye"},
		{"пусто", "   ", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeURL(tc.in); got != tc.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIndexFeed(t *testing.T) {
	idx := IndexFeed([]FeedItem{
		{URL: "https://www.steakhome.ru/catalog/element/a/", Price: 100},
		{URL: ""},
		{URL: "https://steakhome.ru/catalog/element/a", Price: 90},
		{URL: "https://www.steakhome.ru/catalog/element/b/?utm_source=x", Price: 50},
	})

	if len(idx) != 2 {
		t.Fatalf("в карте %d позиций, want 2 (пустой url пропущен, дубль не добавлен)", len(idx))
	}
	if got := idx["steakhome.ru/catalog/element/a"].Price; got != 100 {
		t.Errorf("дубль url: цена %v, want 100 (берём первый)", got)
	}
	if got := idx["steakhome.ru/catalog/element/b"].Price; got != 50 {
		t.Errorf("utm-метки мешают сравнению: цена %v, want 50", got)
	}
}

func TestFeedItemDiscount(t *testing.T) {
	tests := []struct {
		name string
		item FeedItem
		want int16
	}{
		{"сорок процентов", FeedItem{Price: 10788, OldPrice: new(17980.0)}, 40},
		{"десять процентов", FeedItem{Price: 900, OldPrice: new(1000.0)}, 10},
		{"округление до целого", FeedItem{Price: 333, OldPrice: new(1000.0)}, 67},
		{"без зачёркнутой цены", FeedItem{Price: 100}, 0},
		{"цена не ниже зачёркнутой", FeedItem{Price: 1000, OldPrice: new(1000.0)}, 0},
		{"без цены", FeedItem{OldPrice: new(1000.0)}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.Discount(); got != tc.want {
				t.Errorf("Discount() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	const url = "https://www.steakhome.ru/catalog/element/ribeye/"

	idx := IndexFeed([]FeedItem{
		{URL: url, Price: 800, OldPrice: new(1000.0), Available: true},                              // скидка 20 %
		{URL: "https://www.steakhome.ru/catalog/element/no-stock/", Price: 100, Available: true},    // есть на сайте, нет по факту
		{URL: "https://www.steakhome.ru/catalog/element/no-offer/", Price: 100, Available: false},   // не в торговом предложении
		{URL: "https://www.steakhome.ru/catalog/element/plain-price/", Price: 100, Available: true}, // без зачёркнутой цены
	})
	twenty := int16(20)

	tests := []struct {
		name string
		tgt  Target
		pos  Position
		want []Notice
	}{
		{
			name: "скидка совпала — молчим",
			tgt:  Target{Name: "Рибай", SiteURL: url},
			pos:  Position{InStock: true, Discount: &twenty},
		},
		{
			name: "скидка на сайте не та",
			tgt:  Target{Name: "Рибай", SiteURL: url},
			pos:  Position{InStock: true},
			want: []Notice{{Kind: domain.TaskKindSiteDiscount, Text: "Рибай: Не поменяли скидку на сайте"}},
		},
		{
			name: "скидки на сайте нет, а по базе есть",
			tgt:  Target{Name: "Плейн", SiteURL: "https://www.steakhome.ru/catalog/element/plain-price/"},
			pos:  Position{InStock: true, Discount: &twenty},
			want: []Notice{{Kind: domain.TaskKindSiteDiscount, Text: "Не поставили скидку 20% на Плейн"}},
		},
		{
			name: "скидки нет ни на сайте, ни по базе — молчим",
			tgt:  Target{Name: "Плейн", SiteURL: "https://www.steakhome.ru/catalog/element/plain-price/"},
			pos:  Position{InStock: true},
		},
		{
			name: "по базе ручная скидка 0 % — это «скидки нет», не «не поставили»",
			tgt:  Target{Name: "Плейн", SiteURL: "https://www.steakhome.ru/catalog/element/plain-price/"},
			pos:  Position{InStock: true, Discount: new(int16(0))},
		},
		{
			name: "позиции нет в торговом предложении",
			tgt:  Target{Name: "Соус", SiteURL: "https://steakhome.ru/catalog/element/no-offer"},
			pos:  Position{InStock: true},
			want: []Notice{{Kind: domain.TaskKindSiteOffer, Text: "Соус: Нужно добавить товар в торговое предложение"}},
		},
		{
			name: "нет в предложении и нет остатка — только про предложение",
			tgt:  Target{Name: "Соус", SiteURL: "https://steakhome.ru/catalog/element/no-offer"},
			pos:  Position{InStock: false},
			want: []Notice{{Kind: domain.TaskKindSiteOffer, Text: "Соус: Нужно добавить товар в торговое предложение"}},
		},
		{
			name: "есть на сайте, нет по факту",
			tgt:  Target{Name: "Вырезка", SiteURL: "https://www.steakhome.ru/catalog/element/no-stock/"},
			pos:  Position{InStock: false},
			want: []Notice{{Kind: domain.TaskKindSiteRemove, Text: "Вырезка не убрали с сайта"}},
		},
		{
			name: "есть по факту, нет в фиде",
			tgt:  Target{Name: "Колбаса", SiteURL: "https://www.steakhome.ru/catalog/element/kolyasa/"},
			pos:  Position{InStock: true, Discount: &twenty},
			want: []Notice{{Kind: domain.TaskKindSiteReturn, Text: "Колбаса не вернули на сайт"}},
		},
		{
			name: "нет по факту и нет в фиде — молчим",
			tgt:  Target{Name: "Колбаса", SiteURL: "https://www.steakhome.ru/catalog/element/kolyasa/"},
			pos:  Position{InStock: false},
		},
		{
			name: "url человека и url фида — разные записи одного адреса",
			tgt:  Target{Name: "Рибай", SiteURL: "https://steakhome.ru/catalog/element/ribeye?utm_source=tg"},
			pos:  Position{InStock: true, Discount: &twenty},
		},
		{
			name: "два расхождения по одной позиции",
			tgt:  Target{Name: "Рибай", SiteURL: url},
			pos:  Position{InStock: false},
			want: []Notice{
				{Kind: domain.TaskKindSiteDiscount, Text: "Рибай: Не поменяли скидку на сайте"},
				{Kind: domain.TaskKindSiteRemove, Text: "Рибай не убрали с сайта"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(tc.tgt, tc.pos, idx)
			if strings.Join(noticeStrings(got), " | ") != strings.Join(noticeStrings(tc.want), " | ") {
				t.Errorf("Check = %v, want %v", noticeStrings(got), noticeStrings(tc.want))
			}
		})
	}
}

// noticeStrings — уведомления в виде «вид: текст» для сравнения в тестах.
func noticeStrings(notices []Notice) []string {
	out := make([]string, 0, len(notices))
	for _, n := range notices {
		out = append(out, fmt.Sprintf("%s: %s", n.Kind, n.Text))
	}

	return out
}

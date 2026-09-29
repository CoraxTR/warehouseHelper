// Пакет sitecheck — модуль «Проверка сайта» (steakhome.ru).
//
// Раз в час читает фид сайта (catalog_export/feed_for_search1.php: url, price,
// oldprice, available) и сверяет его с базой: у позиций каталога, которым
// человек задал url на сайте, ищет то, что на сайте не сделали. Расхождения
// уходят задачами в общий канал (модуль «Внутренние задачи»): не поменяли
// скидку, позиция не в торговом предложении, позицию не вернули на сайт, не
// убрали с сайта. Владелец решает по задачам, модуль сам ничего не меняет.
//
// Чистая логика (разбор фида, сравнение url, правила сверки) — здесь, без БД и
// сети: входы получает швами (usecase), фид качает адаптер в app (правило
// проекта: модули не импортируют net/http).
package sitecheck

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/domain"
)

// Feed — разобранный фид сайта: время составления и позиции (offer).
type Feed struct {
	// CreatedAt — время составления из шапки фида (yml_catalog date, локальное
	// время сайта, до минут). Фид обновляется в 0 минут каждого часа; по этому
	// времени поллер понимает, что получил именно свежий фид.
	CreatedAt time.Time
	// Items — позиции фида в порядке файла. Группы и разделы (categories) не
	// нужны: сверка идёт по url позиций.
	Items []FeedItem
}

// FeedItem — позиция фида: поля, нужные сверке.
type FeedItem struct {
	URL       string
	Price     float64
	OldPrice  *float64 // цена до скидки; nil — на сайте позиция без зачёркнутой цены (скидки нет)
	Available bool     // available="false" — позиции нет в торговом предложении
}

// Discount — скидка позиции на сайте, % (округление до целого). 0 — скидки нет
// (в том числе когда цены нет или она не ниже зачёркнутой). Сравнивается со
// скидкой сайта по базе (Position.Discount).
func (i FeedItem) Discount() int16 {
	if i.OldPrice == nil || *i.OldPrice <= 0 || i.Price <= 0 || i.Price >= *i.OldPrice {
		return 0
	}

	return int16(math.Round((*i.OldPrice - i.Price) / *i.OldPrice * 100))
}

// Target — позиция каталога, которой задан url на сайте: вход сверки (шов
// каталога). Позиции без url в проверку не попадают вовсе — их на сайте либо
// нет, либо человек url ещё не заполнил (решение владельца, 29.09.2026).
type Target struct {
	ProductID string
	Name      string
	SiteURL   string
}

// Position — состояние позиции по внутренним данным (шов модуля «Сроки»):
// остаток по факту и скидка сайта позиции (максимум эффективной скидки канала
// general по её лотам). Позиции нет в срезе — остатка нет и скидки нет.
type Position struct {
	ProductID string
	InStock   bool
	Discount  *int16
}

// Rule — что человек не сделал на сайте. Вид расхождения = вид задачи ленты
// (domain.TaskKind), поэтому здесь только тексты.
const (
	ruleDiscount = "%s: Не поменяли скидку на сайте"
	ruleNotSet   = "Не поставили скидку %d%% на %s"
	ruleOffer    = "%s: Нужно добавить товар в торговое предложение"
	ruleReturn   = "%s не вернули на сайт"
	ruleRemove   = "%s не убрали с сайта"
)

// Notice — уведомление человеку: текст в общий канал и вид задачи ленты.
type Notice struct {
	Kind domain.TaskKind
	Text string
}

// Check — расхождения одной позиции: что надо сделать на сайте.
//
// Правила (решение владельца, 29.09.2026):
//
//	позиции нет в фиде, а остаток есть            → «не вернули на сайт»
//	позиция в фиде, зачёркнутая цена есть,
//	  скидка не равна скидке сайта по базе        → «Не поменяли скидку на сайте»
//	позиция в фиде, зачёркнутой цены нет,
//	  а по базе скидка есть                        → «Не поставили скидку X% на …»
//	позиция в фиде, available="false"             → «Нужно добавить товар
//	                                                 в торговое предложение»
//	позиция в фиде, available="true", остатка нет → «не убрали с сайта»
//
// Позиция без остатка и без url на сайте — не проверяется. Позиция, которой нет
// в фиде и нет остатка, — тоже не проверяется: на сайте её и не должно быть.
// Позиция с available="false" проверку «не убрали с сайта» не проходит — она и
// так не предлагается к продаже.
//
// Скидка: зачёркнутая цена на сайте — значение скидки сайта; её нет — на сайте
// скидки нет вовсе, и по базе скидка быть не должна (решение владельца: «не
// поменяли» — когда значение не то, «не поставили» — когда на сайте скидки нет,
// а по базе она есть). Оба случая — один вид задачи (скидка на сайте не та).
func Check(t Target, pos Position, idx Index) []Notice {
	item, ok := idx[NormalizeURL(t.SiteURL)]
	if !ok {
		if pos.InStock {
			return []Notice{{Kind: domain.TaskKindSiteReturn, Text: fmt.Sprintf(ruleReturn, t.Name)}}
		}

		return nil
	}

	var out []Notice

	base := discountPercent(pos.Discount)
	switch {
	case item.OldPrice == nil && base > 0:
		out = append(out, Notice{Kind: domain.TaskKindSiteDiscount, Text: fmt.Sprintf(ruleNotSet, base, t.Name)})
	case item.OldPrice != nil && item.Discount() != base:
		out = append(out, Notice{Kind: domain.TaskKindSiteDiscount, Text: fmt.Sprintf(ruleDiscount, t.Name)})
	}

	if !item.Available {
		return append(out, Notice{Kind: domain.TaskKindSiteOffer, Text: fmt.Sprintf(ruleOffer, t.Name)})
	}

	if !pos.InStock {
		out = append(out, Notice{Kind: domain.TaskKindSiteRemove, Text: fmt.Sprintf(ruleRemove, t.Name)})
	}

	return out
}

// Index — позиции фида по нормализованному url.
type Index map[string]FeedItem

// IndexFeed собирает карту фида по url. Одинаковые url (в фиде их быть не
// должно) — берём первый: сверка идёт по адресу, дубль внутри фида ничего не
// добавляет, а порядок позиций фида стабилен.
func IndexFeed(items []FeedItem) Index {
	idx := make(Index, len(items))
	for _, it := range items {
		key := NormalizeURL(it.URL)
		if key == "" {
			continue
		}
		if _, ok := idx[key]; ok {
			continue
		}
		idx[key] = it
	}

	return idx
}

// NormalizeURL — ключ сравнения адресов: без схемы, без «www.», без хвостового
// слеша, без параметров запроса и якоря, в нижнем регистре.
//
// В фиде адреса идут с хостом www.steakhome.ru и хвостовым слешем, а в карточку
// позиции url вписывает человек — как скопировал из браузера (может быть без
// www, с utm-метками). Сравнивать адреса как есть нельзя: не сойдётся там, где
// позиция на сайте есть.
func NormalizeURL(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+len("://"):]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	// Регистр снят ДО отсечения «www.»: адреса копируют и с большой буквы
	// (WWW.Steakhome.ru), а «www.» в другом регистре не отсеклось бы.
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimSuffix(s, "/")

	return s
}

// discountPercent — скидка позиции по базе для сравнения: nil и 0 равнозначны
// «скидки нет» (та же трактовка, что в модуле скидок и в состояниях по дням).
func discountPercent(p *int16) int16 {
	if p == nil || *p <= 0 {
		return 0
	}

	return *p
}

// ymlCatalog — шапка фида: сайт (Bitrix) отдаёт YML, «shops.dtd».
type ymlCatalog struct {
	Date string `xml:"date,attr"`
	Shop struct {
		Offers struct {
			Offers []ymlOffer `xml:"offer"`
		} `xml:"offers"`
	} `xml:"shop"`
}

// ymlOffer — позиция фида. Цены и флаги приходят строками: пустая или битая
// цена не должна ронять разбор всего фида.
type ymlOffer struct {
	Available string `xml:"available,attr"`
	URL       string `xml:"url"`
	Price     string `xml:"price"`
	OldPrice  string `xml:"oldprice"`
}

// feedDateLayout — формат времени составления в шапке фида («2026-09-29 17:23»).
const feedDateLayout = "2006-01-02 15:04"

// ParseFeed разбирает фид сайта: время составления и позиции.
//
// Терпимость к мусору важнее строгости: сверка — фоновая задача, и одна битая
// позиция не должна отменять проверку всего фида. Поэтому позиция без url
// пропускается (сравнивать нечего), позиция с битой ценой остаётся без
// зачёркнутой цены (скидку по ней просто не сверяем), а нечитаемое время
// составления читается как нулевое — поллер по нему поймёт, что фид не свежий,
// и продолжит опрос. Ошибку возвращаем только на битом XML.
func ParseFeed(data []byte) (Feed, error) {
	var doc ymlCatalog
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Feed{}, fmt.Errorf("разбор фида сайта: %w", err)
	}

	feed := Feed{Items: make([]FeedItem, 0, len(doc.Shop.Offers.Offers))}
	// Время сайта локальное (МСК на проде): сравнивать его с часами процесса
	// можно только в той же зоне, в UTC «17:23» не сошлось бы ни с чем.
	if t, err := time.ParseInLocation(feedDateLayout, strings.TrimSpace(doc.Date), time.Local); err == nil {
		feed.CreatedAt = t
	}

	for _, o := range doc.Shop.Offers.Offers {
		item := FeedItem{
			URL: strings.TrimSpace(o.URL),
			// available по умолчанию true: атрибут можно не указывать, а
			// отсутствие атрибута не значит «позиции нет в предложении».
			Available: !strings.EqualFold(strings.TrimSpace(o.Available), "false"),
		}
		if item.URL == "" {
			continue
		}
		if price, err := strconv.ParseFloat(strings.TrimSpace(o.Price), 64); err == nil && price > 0 {
			item.Price = price
		}
		if old, err := strconv.ParseFloat(strings.TrimSpace(o.OldPrice), 64); err == nil && old > 0 && item.Price > 0 {
			item.OldPrice = &old
		}
		feed.Items = append(feed.Items, item)
	}

	return feed, nil
}

package usecase

import (
	"context"
	"strings"
	"testing"

	"warehouseHelper/internal/discounts"
)

// Письмо складу — второе сообщение шага 14:00 (mailing.go): позиции плана дня
// текстом для подписчиков. Одна позиция плана — один блок, цена и адрес карточки
// берутся из товара (Input → PairState).

// catalogInput — данные карточки для письма: цена продажи (копейки), единица
// измерения и адрес позиции на сайте.
func catalogInput(price int64, uom, siteURL string) func(*discounts.Input) {
	return func(in *discounts.Input) {
		in.SalePrice = &price
		in.UOM = uom
		in.SiteURL = siteURL
	}
}

// Письмо уходит вторым сообщением после списка позиций слота: метка дня и
// отправка те же, текст — блок позиции с ценой, скидкой и хвостом.
func TestRunSlotPlanSendsMailingToWarehouse(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p1", "Колбаса", day(9), 100, shelfLifeInput(26),
			catalogInput(259900, "кг", "https://www.steakhome.ru/catalog/element/sausage/")),
	)

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}

	if len(h.chat.texts) != 1 {
		t.Fatalf("сообщения в чат склада: %q, want одно (список слота)", h.chat.texts)
	}
	if len(h.chat.mailing) != 1 {
		t.Fatalf("письма складу: %d, want одно: %q", len(h.chat.mailing), h.chat.mailing)
	}
	text := h.chat.mailing[0]

	for _, want := range []string{
		"Привет, друзья! Вот что мы приготовили на сегодня:",
		`1. <a href="https://www.steakhome.ru/catalog/element/sausage/?utm_source=tg_sale">Колбаса</a> | 2599,00 руб./кг. - 20% => 2079,20 руб./кг.`,
		"Срок годности: 23.09",
		"Доступно по акции: 100 шт.",
		// Остаток по акции — то же число, что доступное (решение владельца
		// 15.10.2026): поле сайта проставится равным плану публикации.
		"Остаток по акции: 100 шт.",
		`На сегодня это все. Для заказа пишите <a href="https://t.me/steakhomeru">https://t.me/steakhomeru</a>`,
		`Все актуальные предложения здесь <a href="https://www.steakhome.ru/catalog/special/">steakhome.ru/catalog/special</a>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в письме нет %q:\n%s", want, text)
		}
	}
	// Список слота и письмо — разные сообщения: склад получает и то, и другое.
	if text == h.chat.texts[0] {
		t.Error("письмо складу совпало со списком позиций слота")
	}
}

// Позиция добора из избытка идёт в письмо со своим планом продаж и своим сроком:
// раскладку по срокам делает план, письмо её только печатает.
func TestRunSlotPlanMailingSurplusPlanQty(t *testing.T) {
	h := newSlotHarness(recalcNow(1),
		lotInput("p2", "Сыр", day(11), 100, catalogInput(219000, "кг", "")),
	)
	// Оборот 150 шт/мес → 5 шт/день; D=10 → продастся 50, избыток 50 + 1 = 51 шт.
	h.turn.rates = map[string]float64{"p2": 150}

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	if len(h.chat.mailing) != 1 {
		t.Fatalf("письма складу: %d, want одно", len(h.chat.mailing))
	}

	text := h.chat.mailing[0]
	if !strings.Contains(text, "Доступно по акции: 51 шт.") {
		t.Errorf("план продаж добора не попал в письмо:\n%s", text)
	}
	if !strings.Contains(text, "Срок годности: 25.09") {
		t.Errorf("срок позиции добора не попал в письмо:\n%s", text)
	}
	// Адреса карточки нет — название печатается текстом, цена остаётся.
	if strings.Contains(text, "<a href=\"\"") || strings.Contains(text, "steakhome.ru/catalog/element") {
		t.Errorf("ссылка на карточку без адреса:\n%s", text)
	}
}

// Позиций плана нет (слот пуст) — шаг молчит и письма не шлёт: сообщения о
// пустом слоте склад не получает.
func TestRunSlotPlanEmptySlotSendsNoMailing(t *testing.T) {
	h := newSlotHarness(recalcNow(1), lotInput("p1", "Колбаса", day(9), 100)) // срока нет — ступени нет

	if err := h.uc.RunSlotPlan(context.Background(), h.now, 4); err != nil {
		t.Fatalf("план дня: %v", err)
	}
	if len(h.chat.mailing) != 0 {
		t.Errorf("письма складу на пустом слоте: %q, want ни одного", h.chat.mailing)
	}
	if len(h.chat.texts) != 0 {
		t.Errorf("сообщения в чат склада на пустом слоте: %q, want ни одного", h.chat.texts)
	}
}

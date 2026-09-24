// Тесты команды /sroki: табличные — на чистую сборку текста, сценарные — на
// фейках (поиск в МС, журнал подбора, отправка в чат). Реальных запросов нет.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"warehouseHelper/internal/msclient/client"
	"warehouseHelper/internal/msorders"
)

// fakeShelfLifeJournal — фейк PickingJournal: строки по заказам, ошибка чтения
// и перехват id, по которым журнал читали.
type fakeShelfLifeJournal struct {
	units     map[string][]msorders.PickingUnit
	err       error
	requested []string
}

func (f *fakeShelfLifeJournal) OrderPickingByOrder(_ context.Context, orderID string) ([]msorders.PickingUnit, error) {
	f.requested = append(f.requested, orderID)
	if f.err != nil {
		return nil, f.err
	}

	return f.units[orderID], nil
}

// Заглушки записи журнала: /sroki только читает; вызов здесь — сигнал, что
// сценарий ответа залез не в свой шов.

func (f *fakeShelfLifeJournal) ReplaceOrderPicking(context.Context, msorders.PickingReplace) error {
	return errors.New("ReplaceOrderPicking не нужен в тестах /sroki")
}

func (f *fakeShelfLifeJournal) AppendOrderPicking(context.Context, []msorders.PickingUnit) error {
	return errors.New("AppendOrderPicking не нужен в тестах /sroki")
}

func (f *fakeShelfLifeJournal) RemoveOrderPickingUnits(context.Context, msorders.PickingReturn) error {
	return errors.New("RemoveOrderPickingUnits не нужен в тестах /sroki")
}

func (f *fakeShelfLifeJournal) ClearOrderPicking(context.Context, string, []string) error {
	return errors.New("ClearOrderPicking не нужен в тестах /sroki")
}

func (f *fakeShelfLifeJournal) ClearOrderPickingProducts(context.Context, string, []string) error {
	return errors.New("ClearOrderPickingProducts не нужен в тестах /sroki")
}

func (f *fakeShelfLifeJournal) CleanupOrderPicking(context.Context, time.Time) (int64, error) {
	return 0, errors.New("CleanupOrderPicking не нужен в тестах /sroki")
}

// fakeShelfLifeChat — фейк ChatSender: запоминает чаты и тексты.
type fakeShelfLifeChat struct {
	chats []int64
	texts []string
	err   error
}

func (f *fakeShelfLifeChat) SendDetails(_ context.Context, chatID int64, text string) error {
	f.chats = append(f.chats, chatID)
	f.texts = append(f.texts, text)

	return f.err
}

// shelfLifeDay — UTC-полночь даты (даты журнала — UTC-полночь).
func shelfLifeDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// shelfLifeDayPtr — та же дата указателем (выработка; nil — не известна).
func shelfLifeDayPtr(y int, m time.Month, d int) *time.Time {
	t := shelfLifeDay(y, m, d)

	return &t
}

// slUnit — строка журнала подбора для тестов /sroki.
func slUnit(code, name string, weighted bool, weightKg float64, producedOn *time.Time, bestBefore time.Time) msorders.PickingUnit {
	return msorders.PickingUnit{
		OrderID:      "order-1",
		PositionID:   "pos-" + code,
		InternalCode: code,
		ProductID:    "prod-" + code,
		ProductName:  name,
		Weighted:     weighted,
		WeightKg:     weightKg,
		ProducedOn:   producedOn,
		BestBefore:   bestBefore,
	}
}

// shelfLifeUseCase — UseCase для /sroki: клиент МС, каталог и швы журнала/чата.
func shelfLifeUseCase(ms OrderClient, catalog CatalogReader, journal PickingJournal, chat ChatSender) *UseCase {
	uc := NewUseCase(ms, catalog, nil, nil, nil)
	uc.SetPickingJournal(journal)
	uc.SetChatSender(chat)

	return uc
}

func TestBuildShelfLifeText(t *testing.T) {
	sep10 := shelfLifeDayPtr(2026, 9, 10)
	sep17 := shelfLifeDay(2026, 9, 17)
	oct10 := shelfLifeDayPtr(2026, 10, 10)
	oct17 := shelfLifeDay(2026, 10, 17)

	tests := []struct {
		name       string
		number     string
		moment     string
		units      []msorders.PickingUnit
		avgWeights map[string]float64
		want       string
	}{
		{
			name:   "весовой: две партии одного товара",
			number: "19379",
			moment: "2026-09-25 14:03:00.000",
			units: []msorders.PickingUnit{
				slUnit("10000001", "Грудка ЦБ охл", true, 0.657, sep10, sep17),
				slUnit("10000001", "Грудка ЦБ охл", true, 0.712, sep10, sep17),
				slUnit("10000001", "Грудка ЦБ охл", true, 0.650, oct10, oct17),
				slUnit("10000001", "Грудка ЦБ охл", true, 0.722, oct10, oct17),
			},
			want: `Заказ 19379 от 25.09.2026

Грудка ЦБ охл
  10.09.2026 → 17.09.2026 · 0,657 | 0,712 кг
  Общий вес группы: 1,369 кг
  Кол-во в группе: 2 шт
Грудка ЦБ охл
  10.10.2026 → 17.10.2026 · 0,65 | 0,722 кг
  Общий вес группы: 1,372 кг
  Кол-во в группе: 2 шт`,
		},
		{
			name:   "весовой: хвостовые нули веса срезаются",
			number: "19379",
			moment: "2026-09-25 14:03:00.000",
			units: []msorders.PickingUnit{
				slUnit("10000001", "Грудка ЦБ охл", true, 0.650, sep10, sep17),
				slUnit("10000001", "Грудка ЦБ охл", true, 0.200, sep10, sep17),
			},
			want: `Заказ 19379 от 25.09.2026

Грудка ЦБ охл
  10.09.2026 → 17.09.2026 · 0,65 | 0,2 кг
  Общий вес группы: 0,85 кг
  Кол-во в группе: 2 шт`,
		},
		{
			name:       "штучный: вес группы по средним весам",
			number:     "19379",
			moment:     "2026-09-25 14:03:00.000",
			avgWeights: map[string]float64{"prod-20000002": 0.55},
			units: []msorders.PickingUnit{
				slUnit("20000002", "Сыр Гауда", false, 1, sep10, sep17),
				slUnit("20000002", "Сыр Гауда", false, 1, sep10, sep17),
				slUnit("20000002", "Сыр Гауда", false, 1, sep10, sep17),
			},
			want: `Заказ 19379 от 25.09.2026

Сыр Гауда
  10.09.2026 → 17.09.2026 · 3 шт
  Общий вес группы: ≈ 1,65 кг
  Кол-во в группе: 3 шт`,
		},
		{
			name:   "штучный: нет выработки и нет среднего веса — без строки веса",
			number: "19379",
			moment: "2026-09-25 14:03:00.000",
			units: []msorders.PickingUnit{
				slUnit("20000002", "Сыр Гауда", false, 1, nil, sep17),
			},
			want: `Заказ 19379 от 25.09.2026

Сыр Гауда
  — → 17.09.2026 · 1 шт
  Кол-во в группе: 1 шт`,
		},
		{
			name:   "весовой: единственная единица без выработки",
			number: "19379",
			moment: "2026-09-25 14:03:00.000",
			units: []msorders.PickingUnit{
				slUnit("10000001", "Грудка ЦБ охл", true, 0.657, nil, sep17),
			},
			want: `Заказ 19379 от 25.09.2026

Грудка ЦБ охл
  — → 17.09.2026 · 0,657 кг
  Общий вес группы: 0,657 кг
  Кол-во в группе: 1 шт`,
		},
		{
			name:   "разные товары: порядок групп и пустая строка между товарами",
			number: "19379",
			moment: "2026-09-25 14:03:00.000",
			units: []msorders.PickingUnit{
				slUnit("30000003", "Товар В", false, 1, nil, shelfLifeDay(2026, 9, 20)),
				slUnit("10000001", "Товар А", true, 0.4, sep10, shelfLifeDay(2026, 10, 5)),
				slUnit("10000001", "Товар А", true, 0.45, sep10, sep17),
			},
			want: `Заказ 19379 от 25.09.2026

Товар А
  10.09.2026 → 17.09.2026 · 0,45 кг
  Общий вес группы: 0,45 кг
  Кол-во в группе: 1 шт
Товар А
  10.09.2026 → 05.10.2026 · 0,4 кг
  Общий вес группы: 0,4 кг
  Кол-во в группе: 1 шт

Товар В
  — → 20.09.2026 · 1 шт
  Кол-во в группе: 1 шт`,
		},
		{
			name:   "шапка без даты заказа",
			number: "19379",
			moment: "",
			units: []msorders.PickingUnit{
				slUnit("10000001", "Грудка ЦБ охл", true, 0.657, sep10, sep17),
			},
			want: `Заказ 19379

Грудка ЦБ охл
  10.09.2026 → 17.09.2026 · 0,657 кг
  Общий вес группы: 0,657 кг
  Кол-во в группе: 1 шт`,
		},
		{
			name:   "журнал пуст: nil",
			number: "19379",
			moment: "2026-09-25 14:03:00.000",
			want:   "По заказу 19379 данных о сроках нет",
		},
		{
			name:   "журнал пуст: пустой слайс",
			number: "19379",
			moment: "2026-09-25 14:03:00.000",
			units:  []msorders.PickingUnit{},
			want:   "По заказу 19379 данных о сроках нет",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildShelfLifeText(tc.number, tc.moment, tc.units, tc.avgWeights)
			if got != tc.want {
				t.Errorf("buildShelfLifeText() =\n%s\n\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// Длинный журнал режется по целым группам: текст влезает в лимит Telegram, у
// последней оставленной группы все строки на месте, а сноска называет число
// убранных групп.
func TestBuildShelfLifeTextTruncated(t *testing.T) {
	const total = 80

	units := make([]msorders.PickingUnit, 0, total)
	for i := range total {
		units = append(units, slUnit(
			"10000001", "Грудка ЦБ охл", true, 0.5,
			shelfLifeDayPtr(2026, 8, 1),
			shelfLifeDay(2026, 9, 1).AddDate(0, 0, i),
		))
	}

	text := buildShelfLifeText("19379", "2026-09-25 14:03:00.000", units, nil)

	if n := utf8.RuneCountInString(text); n > telegramLimit {
		t.Errorf("длина текста %d, want <= %d", n, telegramLimit)
	}
	if !strings.HasPrefix(text, "Заказ 19379 от 25.09.2026\n\n") {
		t.Errorf("шапка потерялась:\n%s", text)
	}
	if !strings.Contains(text, "шт\n… (обрезано: ещё") {
		t.Errorf("текст обрезан не по целой группе:\n%s", text)
	}

	m := regexp.MustCompile(`\n… \(обрезано: ещё (\d+) групп\)$`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("нет сноски об обрезке в конце:\n%s", text)
	}

	dropped, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("сноска без числа групп: %q", m[1])
	}

	kept := strings.Count(text, "Кол-во в группе: ")
	if kept+dropped != total {
		t.Errorf("групп в тексте %d + обрезано %d != всего %d", kept, dropped, total)
	}
	if kept == 0 || dropped == 0 {
		t.Errorf("обрезки не было: оставлено %d, убрано %d", kept, dropped)
	}

	// В обрезанный текст следующая группа уже не влезла бы — иначе резали бы меньше.
	next := utf8.RuneCountInString(shelfLifeGroupText(shelfLifeGroups(units)[kept], nil))
	footnote := utf8.RuneCountInString(fmt.Sprintf("\n… (обрезано: ещё %d групп)", dropped))
	if n := utf8.RuneCountInString(text) - footnote + 1 + next; n <= telegramLimit {
		t.Errorf("в текст влезала ещё одна группа: %d <= %d", n, telegramLimit)
	}
}

// Одноимённых заказов каждый год по одному: отвечаем по самому свежему, годовое
// совпадение номера не путает сроки.
func TestShelfLifeTextPicksFreshestOrder(t *testing.T) {
	ms := &fakeOrderSearch{orders: []client.MSOrder{
		{ID: "old", Name: "19379", Moment: "2025-09-24 09:00:00.000"},
		{ID: "fresh", Name: "19379", Moment: "2026-09-25 09:00:00.000"},
	}}
	journal := &fakeShelfLifeJournal{units: map[string][]msorders.PickingUnit{
		"fresh": {slUnit("10000001", "Грудка ЦБ охл", true, 0.657,
			shelfLifeDayPtr(2026, 9, 10), shelfLifeDay(2026, 9, 17))},
	}}
	uc := shelfLifeUseCase(ms, nil, journal, nil)

	text, err := uc.ShelfLifeText(context.Background(), " 19379 ")
	if err != nil {
		t.Fatalf("ShelfLifeText: %v", err)
	}

	if ms.gotName != "19379" {
		t.Errorf("в МС ушёл номер %q, want %q", ms.gotName, "19379")
	}
	if len(journal.requested) != 1 || journal.requested[0] != "fresh" {
		t.Errorf("журнал читали по %v, want [fresh]", journal.requested)
	}
	if want := "Заказ 19379 от 25.09.2026\n\nГрудка ЦБ охл\n" +
		"  10.09.2026 → 17.09.2026 · 0,657 кг\n" +
		"  Общий вес группы: 0,657 кг\n" +
		"  Кол-во в группе: 1 шт"; text != want {
		t.Errorf("текст =\n%s\n\nwant:\n%s", text, want)
	}
}

func TestShelfLifeTextOrdersNotFound(t *testing.T) {
	ms := &fakeOrderSearch{}
	journal := &fakeShelfLifeJournal{}
	uc := shelfLifeUseCase(ms, nil, journal, nil)

	text, err := uc.ShelfLifeText(context.Background(), "19379")
	if err != nil {
		t.Fatalf("ShelfLifeText: %v", err)
	}
	if want := "По заказу 19379 данных о сроках нет"; text != want {
		t.Errorf("текст = %q, want %q", text, want)
	}
	if len(journal.requested) != 0 {
		t.Errorf("журнал читали впустую: %v", journal.requested)
	}
}

func TestShelfLifeTextEmptyJournal(t *testing.T) {
	ms := &fakeOrderSearch{orders: []client.MSOrder{
		{ID: "o1", Name: "19379", Moment: "2026-09-25 09:00:00.000"},
	}}
	journal := &fakeShelfLifeJournal{}
	uc := shelfLifeUseCase(ms, nil, journal, nil)

	text, err := uc.ShelfLifeText(context.Background(), "19379")
	if err != nil {
		t.Fatalf("ShelfLifeText: %v", err)
	}
	if want := "По заказу 19379 данных о сроках нет"; text != want {
		t.Errorf("текст = %q, want %q", text, want)
	}
}

func TestShelfLifeTextEmptyNumber(t *testing.T) {
	ms := &fakeOrderSearch{}
	uc := shelfLifeUseCase(ms, nil, &fakeShelfLifeJournal{}, nil)

	for _, tc := range []struct{ name, number string }{
		{"пусто", ""},
		{"пробелы", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := uc.ShelfLifeText(context.Background(), tc.number)
			if !errors.Is(err, ErrEmptyOrderNumber) {
				t.Errorf("err = %v, want ErrEmptyOrderNumber", err)
			}
			if text != "" {
				t.Errorf("текст = %q, want пусто", text)
			}
		})
	}

	if ms.calls != 0 {
		t.Errorf("пустой номер ушёл в МС: %d вызовов", ms.calls)
	}
}

func TestShelfLifeTextErrors(t *testing.T) {
	order := []client.MSOrder{{ID: "o1", Name: "19379", Moment: "2026-09-25 09:00:00.000"}}

	tests := []struct {
		name     string
		ms       *fakeOrderSearch
		journal  PickingJournal
		wantPart string
	}{
		{
			name:     "ошибка поиска в МС",
			ms:       &fakeOrderSearch{err: errors.New("МС недоступен")},
			journal:  &fakeShelfLifeJournal{},
			wantPart: "поиск заказа 19379: МС недоступен",
		},
		{
			name:     "шов журнала не подключён",
			ms:       &fakeOrderSearch{orders: order},
			journal:  nil,
			wantPart: "журнал подбора не подключён",
		},
		{
			name:     "ошибка чтения журнала",
			ms:       &fakeOrderSearch{orders: order},
			journal:  &fakeShelfLifeJournal{err: errors.New("БД упала")},
			wantPart: "журнал подбора заказа o1: БД упала",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uc := shelfLifeUseCase(tc.ms, nil, tc.journal, nil)

			text, err := uc.ShelfLifeText(context.Background(), "19379")
			if err == nil {
				t.Fatalf("err = nil, want ошибку (%s)", tc.wantPart)
			}
			if !strings.Contains(err.Error(), tc.wantPart) {
				t.Errorf("err = %q, want часть %q", err.Error(), tc.wantPart)
			}
			if text != "" {
				t.Errorf("текст = %q, want пусто при ошибке", text)
			}
		})
	}
}

// Ошибка каталога не роняет ответ: весовых групп она не касается вовсе, а
// штучные уйдут без строки веса. В МС уходят только id штучных строк.
func TestShelfLifeTextCatalogError(t *testing.T) {
	ms := &fakeOrderSearch{orders: []client.MSOrder{
		{ID: "o1", Name: "19379", Moment: "2026-09-25 09:00:00.000"},
	}}
	journal := &fakeShelfLifeJournal{units: map[string][]msorders.PickingUnit{
		"o1": {
			slUnit("10000001", "Грудка ЦБ охл", true, 0.657, shelfLifeDayPtr(2026, 9, 10), shelfLifeDay(2026, 9, 17)),
			slUnit("20000002", "Сыр Гауда", false, 1, shelfLifeDayPtr(2026, 9, 10), shelfLifeDay(2026, 9, 17)),
		},
	}}
	catalog := &fakeCatalog{avgErr: errors.New("каталог недоступен")}
	uc := shelfLifeUseCase(ms, catalog, journal, nil)

	text, err := uc.ShelfLifeText(context.Background(), "19379")
	if err != nil {
		t.Fatalf("ShelfLifeText: %v", err)
	}

	if len(catalog.avgIDs) != 1 || catalog.avgIDs[0] != "prod-20000002" {
		t.Errorf("средние веса запрошены для %v, want [prod-20000002] (весовые не запрашиваются)", catalog.avgIDs)
	}
	if !strings.Contains(text, "Общий вес группы: 0,657 кг") {
		t.Errorf("весовая группа потеряла вес:\n%s", text)
	}
	if strings.Contains(text, "≈") {
		t.Errorf("при ошибке каталога строки веса штучных быть не должно:\n%s", text)
	}
	if !strings.Contains(text, "Кол-во в группе: 1 шт") {
		t.Errorf("штучная группа потерялась:\n%s", text)
	}
}

func TestReplyShelfLifeSendsToChat(t *testing.T) {
	ms := &fakeOrderSearch{orders: []client.MSOrder{
		{ID: "o1", Name: "19379", Moment: "2026-09-25 09:00:00.000"},
	}}
	journal := &fakeShelfLifeJournal{units: map[string][]msorders.PickingUnit{
		"o1": {slUnit("10000001", "Грудка ЦБ охл", true, 0.657,
			shelfLifeDayPtr(2026, 9, 10), shelfLifeDay(2026, 9, 17))},
	}}
	chat := &fakeShelfLifeChat{}
	uc := shelfLifeUseCase(ms, nil, journal, chat)

	if err := uc.ReplyShelfLife(context.Background(), 4242, "19379"); err != nil {
		t.Fatalf("ReplyShelfLife: %v", err)
	}
	if len(chat.chats) != 1 || chat.chats[0] != 4242 {
		t.Errorf("чаты = %v, want [4242]", chat.chats)
	}

	want, err := uc.ShelfLifeText(context.Background(), "19379")
	if err != nil {
		t.Fatalf("ShelfLifeText: %v", err)
	}
	if len(chat.texts) != 1 || chat.texts[0] != want {
		t.Errorf("отправлен текст %v, want %q", chat.texts, want)
	}
}

// Канал не подключён: текст уходит в лог, ошибки нет — приём как в
// discounts.ReplyDigest.
func TestReplyShelfLifeNilChat(t *testing.T) {
	ms := &fakeOrderSearch{orders: []client.MSOrder{
		{ID: "o1", Name: "19379", Moment: "2026-09-25 09:00:00.000"},
	}}
	uc := shelfLifeUseCase(ms, nil, &fakeShelfLifeJournal{}, nil)

	if err := uc.ReplyShelfLife(context.Background(), 7, "19379"); err != nil {
		t.Errorf("ReplyShelfLife без шва чата: %v, want nil", err)
	}
}

func TestReplyShelfLifeErrors(t *testing.T) {
	sendErr := errors.New("Telegram недоступен")

	t.Run("ошибка отправки", func(t *testing.T) {
		ms := &fakeOrderSearch{orders: []client.MSOrder{
			{ID: "o1", Name: "19379", Moment: "2026-09-25 09:00:00.000"},
		}}
		chat := &fakeShelfLifeChat{err: sendErr}
		uc := shelfLifeUseCase(ms, nil, &fakeShelfLifeJournal{}, chat)

		err := uc.ReplyShelfLife(context.Background(), 42, "19379")
		if !errors.Is(err, sendErr) {
			t.Errorf("err = %v, want обёртку над %v", err, sendErr)
		}
		if !strings.Contains(err.Error(), "ответ на /sroki в чат 42") {
			t.Errorf("err = %q, want упоминание чата 42", err.Error())
		}
	})

	t.Run("ошибка до отправки", func(t *testing.T) {
		chat := &fakeShelfLifeChat{}
		uc := shelfLifeUseCase(&fakeOrderSearch{err: errors.New("МС недоступен")}, nil, &fakeShelfLifeJournal{}, chat)

		if err := uc.ReplyShelfLife(context.Background(), 42, "19379"); err == nil {
			t.Error("err = nil, want ошибку поиска")
		}
		if len(chat.texts) != 0 {
			t.Errorf("в чат ушёл текст при ошибке: %v", chat.texts)
		}
	})
}

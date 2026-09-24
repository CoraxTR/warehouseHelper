package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"

	"github.com/jackc/pgx/v5"
)

// Лоты подделок: id товара (как в МС — строка) для строк выборки.
const (
	testLotMilk   = "p-milk"
	testLotCheese = "p-cheese"
)

// TestScanDiscountInput — разбор строки снапшота: дни периода берутся из
// товарного признака track_weekly (7 или 30, нуля больше нет), а оборота в
// снапшоте нет ВООБЩЕ — это данные модуля средних продаж, и расчёт берёт их его
// же методами (шов Turnover), а не из этого запроса. NULL-срок годности даёт nil,
// скидки лота ложатся по своим полям: plain/ручные — с NULL в nil, метка
// источника («что снимать при уходе избытка») — строкой, ноль переносится как
// есть (трактовку «0 = NULL» держит домен, а не репозиторий).
func TestScanDiscountInput(t *testing.T) {
	bb := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		vals []any
		want discounts.Input
	}{
		{
			name: "недельный товар — период 7 дней",
			vals: []any{"p-week", "Молоко 3,2%", "Молочка", false, 14, true, bb, 24,
				int16(20), nil, nil, int16(15), "expiry"},
			want: discounts.Input{
				ProductID: "p-week", Name: "Молоко 3,2%", GroupName: "Молочка",
				ShelfLife: new(int16(14)), TrackWeekly: true, BestBefore: bb, Qty: 24,
				PeriodDays:   7,
				GeneralPlain: new(int16(20)), TelegramManual: new(int16(15)), DiscountSource: "expiry",
			},
		},
		{
			name: "месячный товар — период 30 дней",
			vals: []any{"p-month", "Сыр", "Молочка", true, 90, false, bb, 8,
				nil, int16(10), int16(30), nil, ""},
			want: discounts.Input{
				ProductID: "p-month", Name: "Сыр", GroupName: "Молочка", ShortList: true,
				ShelfLife: new(int16(90)), BestBefore: bb, Qty: 8,
				PeriodDays:    30,
				TelegramPlain: new(int16(10)), GeneralManual: new(int16(30)),
			},
		},
		{
			name: "нет данных о продажах — дни периода всё равно из товарного признака",
			vals: []any{"p-new", "Новинка", "Разное", false, 30, true, bb, 5,
				nil, nil, nil, nil, nil},
			want: discounts.Input{
				ProductID: "p-new", Name: "Новинка", GroupName: "Разное",
				ShelfLife: new(int16(30)), TrackWeekly: true, BestBefore: bb, Qty: 5,
				PeriodDays: 7,
			},
		},
		{
			name: "срок годности не задан — NULL",
			vals: []any{"p-null", "Без срока", "Разное", false, nil, false, bb, 3,
				nil, nil, nil, nil, nil},
			want: discounts.Input{
				ProductID: "p-null", Name: "Без срока", GroupName: "Разное",
				BestBefore: bb, Qty: 3, PeriodDays: 30,
			},
		},
		{
			name: "нулевая скидка — ноль, а не NULL (трактовка — в домене)",
			vals: []any{"p-zero", "Йогурт", "Молочка", false, 5, false, bb, 4,
				int16(0), nil, int16(0), nil, "surplus"},
			want: discounts.Input{
				ProductID: "p-zero", Name: "Йогурт", GroupName: "Молочка",
				ShelfLife: new(int16(5)), BestBefore: bb, Qty: 4,
				PeriodDays:   30,
				GeneralPlain: new(int16(0)), GeneralManual: new(int16(0)), DiscountSource: "surplus",
			},
		},
		{
			// Обе nullable TEXT-колонки снапшота NULL: группа товара не задана
			// (products.group_name), метки источника нет (discount_source).
			// Регресс: Scan читал их прямо в string и падал на живой БД
			// («cannot scan NULL into *string»), а подделка Scan клала NULL в
			// string как пустую строку — тест этого не видел (см. scanValue).
			name: "группы нет и метки нет — NULL в обеих TEXT-колонках",
			vals: []any{"p-nogroup", "Без группы", nil, false, 20, true, bb, 7,
				nil, int16(15), nil, nil, nil},
			want: discounts.Input{
				ProductID: "p-nogroup", Name: "Без группы", GroupName: "",
				ShelfLife: new(int16(20)), TrackWeekly: true, BestBefore: bb, Qty: 7,
				PeriodDays:    7,
				TelegramPlain: new(int16(15)), DiscountSource: "",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanDiscountInput(fakeRow{vals: tc.vals})
			if err != nil {
				t.Fatalf("scanDiscountInput: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("scanDiscountInput = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestScanDiscountInputBadRow — несовпадение колонок и значений не молчит:
// ошибка оборачивается (errors.Is на ErrNoRows от подделки).
func TestScanDiscountInputBadRow(t *testing.T) {
	if _, err := scanDiscountInput(fakeRow{vals: []any{"p1", "Молоко"}}); err == nil {
		t.Fatal("scanDiscountInput на короткой строке: ошибки нет")
	}
}

// countColumns считает элементы списка SELECT по запятым верхнего уровня:
// запятые внутри вызовов (`COALESCE(a, b)`) колонками не считаются.
func countColumns(list string) int {
	depth, n := 0, 1
	for _, r := range list {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		default:
			// прочие символы на структуру SELECT не влияют
		}
	}
	return n
}

// TestScanDiscountInputColumnCount — список колонок в константе и порядок Scan
// обязаны совпадать: проверяем арность, чтобы рассинхрон (AGENTS.md) не молчал.
func TestScanDiscountInputColumnCount(t *testing.T) {
	row := &captureRow{}
	if _, err := scanDiscountInput(row); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("scanDiscountInput: %v, want обёртку pgx.ErrNoRows", err)
	}

	want := countColumns(discountInputColumns)
	if row.dests != want {
		t.Errorf("scan-хелпер разбирает %d колонок, в списке — %d", row.dests, want)
	}
	// 13 колонок: товарные признаки (6), срок и остаток лота (2), четыре скидки
	// и метка источника (5). Колонки оборота здесь нет — оборот приходит швом
	// модуля средних продаж, а не из снапшота входа.
	if want != 13 {
		t.Errorf("в списке колонок %d, want 13 (оборота в снапшоте нет)", want)
	}
}

// TestCountColumns — запятые внутри вызовов колонками не считаются.
func TestCountColumns(t *testing.T) {
	tests := []struct {
		name string
		list string
		want int
	}{
		{"одна колонка", "id", 1},
		{"две колонки", "id, name", 2},
		{"вложенный вызов", "id, COALESCE(a, b)", 2},
		{"вложенные вызовы и префиксы", "s.id, f(a, g(b, c)), p.name", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countColumns(tc.list); got != tc.want {
				t.Errorf("countColumns(%q) = %d, want %d", tc.list, got, tc.want)
			}
		})
	}
}

// TestDiscountInputQueryHasNoTurnoverJoins — оборота в снапшоте входа нет: это
// данные модуля средних продаж, и расчёт берёт их его же методами (шов Turnover).
// Ни LATERAL-джойнов к таблицам оборота, ни параметров у запроса быть не должно:
// дни периода едут из товарного признака track_weekly, а не из окна оборота.
func TestDiscountInputQueryHasNoTurnoverJoins(t *testing.T) {
	for _, bad := range []string{"turnover", "LATERAL", "$1", "date_trunc"} {
		if strings.Contains(discountInputQuery, bad) {
			t.Errorf("в discountInputQuery осталось %q: оборота в снапшоте нет", bad)
		}
	}
	for _, want := range []string{"FROM product_stock s", "JOIN products p ON p.id = s.product_id"} {
		if !strings.Contains(discountInputQuery, want) {
			t.Errorf("в discountInputQuery нет %q", want)
		}
	}
}

// TestCollectLotPairs — набор пар лотов: пара собирается из строки выборки,
// дубликат схлопывается, пустая выборка даёт пустую карту (не nil), ошибки
// строки и выборки пробрасываются.
func TestCollectLotPairs(t *testing.T) {
	october := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	november := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)

	t.Run("дубликат пары схлопывается", func(t *testing.T) {
		rows := &fakeRows{rows: [][]any{
			{testLotMilk, october},
			{testLotCheese, november},
			{testLotMilk, october},
		}}
		got, err := collectLotPairs(rows)
		if err != nil {
			t.Fatalf("collectLotPairs: %v", err)
		}
		want := map[discounts.LotKey]struct{}{
			{ProductID: testLotMilk, BestBefore: october}:    {},
			{ProductID: testLotCheese, BestBefore: november}: {},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("collectLotPairs = %v, want %v", got, want)
		}
	})

	t.Run("пустая выборка — пустая карта", func(t *testing.T) {
		got, err := collectLotPairs(&fakeRows{})
		if err != nil {
			t.Fatalf("collectLotPairs: %v", err)
		}
		if got == nil {
			t.Fatal("карта nil, want пустую")
		}
		if len(got) != 0 {
			t.Errorf("в карте %d пар, want 0", len(got))
		}
	})

	t.Run("короткая строка выборки", func(t *testing.T) {
		if _, err := collectLotPairs(&fakeRows{rows: [][]any{{testLotMilk}}}); err == nil {
			t.Fatal("collectLotPairs на короткой строке: ошибки нет")
		}
	})

	t.Run("ошибка выборки после строк", func(t *testing.T) {
		rows := &fakeRows{rows: [][]any{{testLotMilk, october}}, err: errors.New("обрыв связи")}
		if _, err := collectLotPairs(rows); !errors.Is(err, rows.err) {
			t.Fatalf("collectLotPairs: %v, want обёртку ошибки выборки", err)
		}
	})
}

// TestCheckDigestItems — проверка позиций до вставки: причина только из Reason*
// (слова совпадают с CHECK в БД), скидка 0..100; пустой слот — не ошибка.
func TestCheckDigestItems(t *testing.T) {
	bestBefore := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	item := func(reason string, percent int16) discounts.DigestItem {
		return discounts.DigestItem{
			ProductID: testLotMilk, BestBefore: bestBefore, Percent: percent, Reason: reason,
		}
	}

	tests := []struct {
		name    string
		items   []discounts.DigestItem
		wantErr bool
	}{
		{"пустой слот — норма", nil, false},
		{"ручная", []discounts.DigestItem{item(discounts.ReasonManual, 20)}, false},
		{"по сроку 0 %", []discounts.DigestItem{item(discounts.ReasonExpiry, 0)}, false},
		{"избыток 100 %", []discounts.DigestItem{item(discounts.ReasonSurplus, 100)}, false},
		{"неизвестная причина", []discounts.DigestItem{item("none", 20)}, true},
		{"пустая причина", []discounts.DigestItem{item("", 20)}, true},
		{"скидка больше 100", []discounts.DigestItem{item(discounts.ReasonExpiry, 101)}, true},
		{"скидка отрицательная", []discounts.DigestItem{item(discounts.ReasonExpiry, -1)}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDigestItems(tc.items)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkDigestItems: %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, discounts.ErrBadDigestItem) {
				t.Errorf("checkDigestItems: %v, want обёртку discounts.ErrBadDigestItem", err)
			}
		})
	}
}

// TestLatestSentDigestSQL — «предыдущая рассылка» = последняя ОТПРАВЛЕННАЯ:
// собранная, но не отправленная рассылка не должна скрывать позиции от
// антидубля, а порядок выбирает самый свежий слот (дайджест 09:00 против плана
// 14:00 одного дня).
func TestLatestSentDigestSQL(t *testing.T) {
	for _, want := range []string{
		"sent_at IS NOT NULL",
		"ORDER BY planned_at DESC, id DESC",
		"LIMIT 1",
	} {
		if !strings.Contains(latestSentDigestSQL, want) {
			t.Errorf("в latestSentDigestSQL нет %q", want)
		}
	}
}

// TestDiscountInputColumnsIncludeDiscounts — снапшот расчёта обязан нести
// текущие скидки лота и метку источника: без них автоматика не знает, что уже
// стоит в БД («только вверх» для general, приоритет ручной, «что снимать» —
// по метке), и пишет вслепую.
func TestDiscountInputColumnsIncludeDiscounts(t *testing.T) {
	for _, want := range []string{
		"s.discount_general,", "s.discount_telegram,",
		"s.discount_general_manual,", "s.discount_telegram_manual,", "s.discount_source",
	} {
		if !strings.Contains(discountInputColumns, want) {
			t.Errorf("в discountInputColumns нет %q", want)
		}
	}
	// Оборота в снапшоте нет и не должно быть: его читает шов модуля средних продаж.
	if strings.Contains(discountInputColumns, "turnover") {
		t.Error("в discountInputColumns осталась колонка оборота")
	}
}

// TestDayFlagColumn — маркер дня → колонка discount_day_flags. Имена колонок
// подставляются в SQL, поэтому берутся только из белого списка: неизвестный
// маркер (в том числе попытка подсунуть SQL) — ошибка ДО запроса.
func TestDayFlagColumn(t *testing.T) {
	known := []struct {
		flag discounts.DayFlag
		want string
	}{
		{discounts.FlagSurplus, "surplus_done"},
		{discounts.FlagExpiry, "expiry_done"},
		{discounts.FlagDigestSent, "digest_sent"},
		{discounts.FlagPlan, "tg_plan_done"},
		{discounts.FlagRaise, "tg_raise_done"},
	}
	for _, tc := range known {
		got, err := dayFlagColumn(tc.flag)
		if err != nil {
			t.Errorf("dayFlagColumn(%q): %v", tc.flag, err)
			continue
		}
		if got != tc.want {
			t.Errorf("dayFlagColumn(%q) = %q, want %q", tc.flag, got, tc.want)
		}
	}

	for _, bad := range []discounts.DayFlag{"", "none", "surplus_done; DROP TABLE product_stock"} {
		if _, err := dayFlagColumn(bad); !errors.Is(err, discounts.ErrBadDayFlag) {
			t.Errorf("dayFlagColumn(%q) = %v, want обёртку discounts.ErrBadDayFlag", bad, err)
		}
	}
}

// TestCollectTodaySlot — срез слота дня «позиция плана»: порядок строк выборки
// сохраняется, пустой день даёт пустой (не nil) срез, короткая строка и ошибка
// выборки не молчат. Контроль вечернего подъёма: у сроковой позиции план продаж
// не считается — initial_qty/plan_qty NULL (nil), у добора из избытка оба поля
// заполнены. NULL подделка обязана отклонять (см. TestScanValueNullStrict):
// мягкое «NULL → 0» показало бы непроданную пару проданной (остаток 0 дошёл до
// InitialQty−PlanQty).
func TestCollectTodaySlot(t *testing.T) {
	october := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	november := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)

	t.Run("позиции слота: сроковая с NULL и добор из избытка", func(t *testing.T) {
		rows := &fakeRows{rows: [][]any{
			{testLotMilk, october, int16(20), discounts.ReasonExpiry, nil, nil},
			{testLotCheese, november, int16(10), discounts.ReasonSurplus, int64(24), int64(6)},
		}}
		got, err := collectTodaySlot(rows)
		if err != nil {
			t.Fatalf("collectTodaySlot: %v", err)
		}
		want := []discounts.SlotItem{
			{
				ProductID: testLotMilk, BestBefore: october,
				Percent: 20, Reason: discounts.ReasonExpiry,
			},
			{
				ProductID: testLotCheese, BestBefore: november,
				Percent: 10, Reason: discounts.ReasonSurplus,
				InitialQty: new(int64(24)), PlanQty: new(int64(6)),
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("collectTodaySlot = %+v, want %+v", got, want)
		}
	})

	t.Run("дня без публикации — пустой срез", func(t *testing.T) {
		got, err := collectTodaySlot(&fakeRows{})
		if err != nil {
			t.Fatalf("collectTodaySlot: %v", err)
		}
		if got == nil {
			t.Fatal("срез nil, want пустой")
		}
		if len(got) != 0 {
			t.Errorf("в срезе %d позиций, want 0", len(got))
		}
	})

	t.Run("короткая строка выборки", func(t *testing.T) {
		if _, err := collectTodaySlot(&fakeRows{rows: [][]any{{testLotMilk}}}); err == nil {
			t.Fatal("collectTodaySlot на короткой строке: ошибки нет")
		}
	})

	t.Run("ошибка выборки после строк", func(t *testing.T) {
		rows := &fakeRows{
			rows: [][]any{{testLotMilk, october, int16(20), discounts.ReasonExpiry, nil, nil}},
			err:  errors.New("обрыв связи"),
		}
		if _, err := collectTodaySlot(rows); !errors.Is(err, rows.err) {
			t.Fatalf("collectTodaySlot: %v, want обёртку ошибки выборки", err)
		}
	})
}

// TestScanTodaySlotItem — разбор строки слота: NULL в nullable-колонках контроля
// (initial_qty/plan_qty) даёт nil, а не ноль. Ноль означал бы другое состояние:
// «остаток пары 0 / план продаж 0» вместо «план продаж не считался».
func TestScanTodaySlotItem(t *testing.T) {
	october := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)

	got, err := scanTodaySlotItem(fakeRow{vals: []any{
		testLotMilk, october, int16(30), discounts.ReasonManual, nil, nil,
	}})
	if err != nil {
		t.Fatalf("scanTodaySlotItem: %v", err)
	}
	want := discounts.SlotItem{
		ProductID: testLotMilk, BestBefore: october,
		Percent: 30, Reason: discounts.ReasonManual,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanTodaySlotItem = %+v, want %+v", got, want)
	}
}

// TestTodaySlotColumnCount — список колонок слота в константе и порядок Scan в
// scanTodaySlotItem обязаны совпадать: арность сверяется, чтобы рассинхрон
// (AGENTS.md) не молчал.
func TestTodaySlotColumnCount(t *testing.T) {
	row := &captureRow{}
	if _, err := scanTodaySlotItem(row); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("scanTodaySlotItem: %v, want обёртку pgx.ErrNoRows", err)
	}

	want := countColumns(todaySlotColumns)
	if row.dests != want {
		t.Errorf("scan-хелпер разбирает %d колонок, в списке — %d", row.dests, want)
	}
	// 6 колонок: лот (2), скидка плана и её причина (2), контроль вечернего
	// подъёма (2): остаток пары на 14:00 и план продаж добора из избытка.
	if want != 6 {
		t.Errorf("в списке колонок %d, want 6", want)
	}
}

// TestTodaySlotSQL — слотом дня считаем последний ОТПРАВЛЕННЫЙ слот этого дня
// (план 14:00 идёт после дайджеста 09:00): по нему держат эскалацию 10→20 и по
// нему же поднимают general в 16:00. Отправленный слот важен — собранную, но не
// отправленную рассылку человек не видел. Выборка обязана нести и контроль
// вечернего подъёма: без initial_qty/plan_qty подъём 16:00 не знает, продана ли
// пара.
func TestTodaySlotSQL(t *testing.T) {
	for _, want := range []string{
		"sent_at IS NOT NULL",
		"planned_at = $1::date",
		"ORDER BY id DESC",
		"LIMIT 1",
		todaySlotColumns,
		"i.initial_qty",
		"i.plan_qty",
		"i.reason",
	} {
		if !strings.Contains(todaySlotSQL, want) {
			t.Errorf("в todaySlotSQL нет %q", want)
		}
	}
}

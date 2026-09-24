package usecase

import (
	"reflect"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// testDay — начало дня расчёта тестов (даты пар считаются от него прибавкой дней).
var testDay = time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)

// regPair — пара теста: товар, срок и источники скидки через опции.
// Applied (то, что стоит в БД) выставляется отдельно — окно, очередь и дайджест
// смотрят на Desired(), а не на Applied.
func regPair(pid, name string, bestBefore time.Time, opts ...func(*PairState)) PairState {
	p := PairState{
		Key:        discounts.LotKey{ProductID: pid, BestBefore: bestBefore},
		ProductID:  pid,
		Name:       name,
		BestBefore: bestBefore,
		DaysLeft:   int(bestBefore.Sub(testDay).Hours() / 24),
	}
	for _, opt := range opts {
		opt(&p)
	}
	return p
}

// manualOpt — ручная скидка канала сайта.
func manualOpt(v int16) func(*PairState) {
	return func(p *PairState) { p.Manual = &v }
}

// expiryOpt — ступень лестницы по сроку годности. Значение в тестах реестра не
// важно (порядок и переходы), поэтому оно одно на все случаи.
func expiryOpt() func(*PairState) {
	v := int16(30)
	return func(p *PairState) { p.Expiry = &v }
}

// surplusOpt — избыток: скидка за константу и коэффициент Q/(v×D).
func surplusOpt(coeff float64) func(*PairState) {
	return func(p *PairState) {
		v := discounts.SurplusPercent()
		p.Surplus = &v
		p.Coeff = coeff
		p.HasSurplus = true
	}
}

// appliedOpt — эффективная скидка, которая стоит в БД (product_stock).
func appliedOpt(v int16) func(*PairState) {
	return func(p *PairState) { p.Applied = &v }
}

// day — срок в днях от дня расчёта.
func day(n int) time.Time { return testDay.AddDate(0, 0, n) }

// Ручная скидка вытесняет и лестницу, и избыток: окно собирается по приоритету
// Desired (ручная → срок → избыток), избыток уходит за окно.
func TestRegistryWindowManualBeatsLadderAndSurplus(t *testing.T) {
	r := NewRegistry()
	r.Replace([]PairState{
		regPair("p-expiry", "Сроковый", day(6), expiryOpt()),
		regPair("p-surplus", "Избыточный", day(12), surplusOpt(2.5)),
		regPair("p-manual", "Ручной", day(9), manualOpt(40)),
	})

	rows := r.Window(2)
	if len(rows) != 2 {
		t.Fatalf("окно 2: %d строк, want 2", len(rows))
	}
	if rows[0].Source != discounts.SourceManual {
		t.Errorf("первая строка окна — %s, want manual", rows[0].Source)
	}
	if rows[1].Source != discounts.SourceExpiry {
		t.Errorf("вторая строка окна — %s, want expiry", rows[1].Source)
	}

	full := r.Window(0)
	if len(full) != 3 {
		t.Fatalf("окно без ёмкости: %d строк, want 3", len(full))
	}
	if full[2].Source != discounts.SourceSurplus {
		t.Errorf("третья строка окна — %s, want surplus", full[2].Source)
	}
}

// Порядок окна совпадает с discounts.Sort: ручные по сроку ↑ → сроковые ↑ →
// избыточные по коэффициенту ↓.
func TestRegistryWindowOrderMatchesSort(t *testing.T) {
	r := NewRegistry()
	r.Replace([]PairState{
		regPair("p-s1", "Избыток слабый", day(11), surplusOpt(1.2)),
		regPair("p-e", "Сроковый", day(6), expiryOpt()),
		regPair("p-m2", "Ручной поздний", day(9), manualOpt(40)),
		regPair("p-s2", "Избыток сильный", day(12), surplusOpt(2.5)),
		regPair("p-m1", "Ручной ранний", day(4), manualOpt(20)),
	})

	want := []discounts.Row{
		{ProductID: "p-m1", Name: "Ручной ранний", BestBefore: day(4), Percent: 20, Source: discounts.SourceManual, DaysLeft: 4},
		{ProductID: "p-m2", Name: "Ручной поздний", BestBefore: day(9), Percent: 40, Source: discounts.SourceManual, DaysLeft: 9},
		{ProductID: "p-e", Name: "Сроковый", BestBefore: day(6), Percent: 30, Source: discounts.SourceExpiry, DaysLeft: 6},
		{ProductID: "p-s2", Name: "Избыток сильный", BestBefore: day(12), Percent: 10, Source: discounts.SourceSurplus, Coeff: 2.5, DaysLeft: 12},
		{ProductID: "p-s1", Name: "Избыток слабый", BestBefore: day(11), Percent: 10, Source: discounts.SourceSurplus, Coeff: 1.2, DaysLeft: 11},
	}

	got := r.Window(0)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("окно:\n%+v\nwant:\n%+v", got, want)
	}

	// повторная сортировка порядка не меняет — окно уже в порядке discounts.Sort
	again := make([]discounts.Row, len(got))
	copy(again, got)
	discounts.Sort(again)
	if !reflect.DeepEqual(again, got) {
		t.Errorf("после discounts.Sort порядок другой:\n%+v\nбыло:\n%+v", again, got)
	}
}

// Слотов не добиваем: пустой реестр и реестр короче окна дают короткий список.
func TestRegistryWindowShorterThanSlots(t *testing.T) {
	r := NewRegistry()
	if got := r.Window(12); len(got) != 0 {
		t.Errorf("пустой реестр: %d строк, want 0", len(got))
	}

	r.Replace([]PairState{regPair("p1", "Один", day(5), expiryOpt())})
	if got := r.Window(12); len(got) != 1 {
		t.Errorf("одна активная пара: %d строк, want 1", len(got))
	}
}

// Пара без скидки в окно не попадает вообще (Desired() == nil).
func TestRegistryWindowSkipsInactivePairs(t *testing.T) {
	r := NewRegistry()
	r.Replace([]PairState{
		regPair("p-idle", "Без скидки", day(20)),
		regPair("p-zero", "Нулевая ручная", day(10), manualOpt(0)),
		regPair("p-active", "Активная", day(7), expiryOpt()),
	})

	got := r.Window(12)
	if len(got) != 1 {
		t.Fatalf("окно: %d строк, want 1 (%+v)", len(got), got)
	}
	if got[0].ProductID != "p-active" {
		t.Errorf("в окне %q, want p-active", got[0].ProductID)
	}
}

// Освободившийся слот занимает следующая по рангу: верхняя пара ушла из расчёта
// (Replace без неё) — её место в окне занимает избыток.
func TestRegistryWindowSlotFreedByNextRank(t *testing.T) {
	r := NewRegistry()
	manual := regPair("p-manual", "Ручной", day(9), manualOpt(40))
	expiry := regPair("p-expiry", "Сроковый", day(6), expiryOpt())
	surplus := regPair("p-surplus", "Избыточный", day(12), surplusOpt(2.5))
	r.Replace([]PairState{manual, expiry, surplus})

	if got := r.Window(2); got[0].Source != discounts.SourceManual || got[1].Source != discounts.SourceExpiry {
		t.Fatalf("до выпадения окно: %s, %s", got[0].Source, got[1].Source)
	}

	// ручная пара ушла из расчёта: слот занимает следующий по рангу — избыток
	r.Replace([]PairState{expiry, surplus})

	got := r.Window(2)
	if len(got) != 2 {
		t.Fatalf("после выпадения: %d строк, want 2", len(got))
	}
	if got[0].Source != discounts.SourceExpiry || got[1].Source != discounts.SourceSurplus {
		t.Errorf("после выпадения окно: %s, %s, want expiry, surplus", got[0].Source, got[1].Source)
	}
	if q := r.Queue(2); len(q) != 0 {
		t.Errorf("очередь после подстановки: %d строк, want 0", len(q))
	}
}

// Queue отдаёт только избыток за пределами окна: избыточные строки, вошедшие в
// окно, в очереди не дублируются.
func TestRegistryQueueSurplusBeyondWindow(t *testing.T) {
	r := NewRegistry()
	r.Replace([]PairState{
		regPair("p-manual", "Ручной", day(8), manualOpt(40)),
		regPair("p-expiry", "Сроковый", day(6), expiryOpt()),
		regPair("p-s3", "Избыток 3", day(9), surplusOpt(3.0)),
		regPair("p-s2", "Избыток 2", day(10), surplusOpt(2.0)),
		regPair("p-s1", "Избыток 1", day(11), surplusOpt(1.5)),
	})

	tests := []struct {
		window int
		want   int
	}{
		{2, 3},  // окно: ручной + сроковый — весь избыток в очереди
		{3, 2},  // в окно вошёл сильнейший избыток
		{4, 1},  // в очереди остался последний
		{5, 0},  // окно вместило всё
		{0, 0},  // ёмкости нет — окно вмещает всё
		{12, 0}, // окно шире реестра
		{-1, 0},
	}
	for _, tt := range tests {
		q := r.Queue(tt.window)
		if len(q) != tt.want {
			t.Errorf("Queue(%d): %d строк, want %d", tt.window, len(q), tt.want)
		}
		for _, row := range q {
			if row.Source != discounts.SourceSurplus {
				t.Errorf("Queue(%d): в очереди строка с источником %s", tt.window, row.Source)
			}
		}
	}

	// порядок очереди — как в окне: коэффициент ↓
	q := r.Queue(2)
	if len(q) == 3 && !(q[0].Coeff > q[1].Coeff && q[1].Coeff > q[2].Coeff) {
		t.Errorf("очередь не по коэффициенту ↓: %+v", q)
	}
}

// Replace возвращает изменения эффективной скидки по четырём переходам
// (нет→10, 10→20, 20→нет, без изменений → пусто) и учитывает исчезнувшие пары.
func TestRegistryReplaceTransitions(t *testing.T) {
	r := NewRegistry()

	// первый расчёт: у A скидки нет, у B/C/D/E стоит значение
	first := []PairState{
		regPair("A", "Нет скидки", day(7), expiryOpt()),
		regPair("B", "Десять", day(8), expiryOpt(), appliedOpt(10)),
		regPair("C", "Двадцать", day(9), expiryOpt(), appliedOpt(20)),
		regPair("D", "Без изменений", day(10), expiryOpt(), appliedOpt(15)),
		regPair("E", "Удалили", day(11), expiryOpt(), appliedOpt(10)),
	}
	// Первый снапшот процесса только закладывает базу сравнения: уведомлений он
	// не даёт — иначе после каждого старта в чат уходил бы залп «поставьте
	// скидку» по позициям, которые человек и так видит на сайте.
	if got := r.Replace(first); len(got) != 0 {
		t.Fatalf("первый снапшот: %d изменений, want 0 (%+v)", len(got), got)
	}

	// второй расчёт: A — нет→10, B — 10→20, C — 20→нет, D — без изменений,
	// E из расчёта ушла (её скидка уходит вместе с парой)
	second := []PairState{
		regPair("A", "Нет скидки", day(7), expiryOpt(), appliedOpt(10)),
		regPair("B", "Десять", day(8), expiryOpt(), appliedOpt(20)),
		regPair("C", "Двадцать", day(9), expiryOpt()),
		regPair("D", "Без изменений", day(10), expiryOpt(), appliedOpt(15)),
	}
	want := []Change{
		{ProductID: "A", Name: "Нет скидки", BestBefore: day(7), Next: new(int16(10))},
		{ProductID: "B", Name: "Десять", BestBefore: day(8), Prev: new(int16(10)), Next: new(int16(20))},
		{ProductID: "C", Name: "Двадцать", BestBefore: day(9), Prev: new(int16(20))},
		{ProductID: "E", Name: "Удалили", BestBefore: day(11), Prev: new(int16(10))},
	}
	got := r.Replace(second)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("изменения:\n%+v\nwant:\n%+v", got, want)
	}

	// третий расчёт без изменений — пусто
	if again := r.Replace(second); len(again) != 0 {
		t.Errorf("без изменений: %d изменений, want 0 (%+v)", len(again), again)
	}
}

// Порядок изменений стабилен: по ProductID, затем по сроку — уведомления не
// зависят от порядка обхода карт и порядка входа.
func TestRegistryReplaceChangeOrder(t *testing.T) {
	r := NewRegistry()
	early, late := day(3), day(20)

	// первый расчёт: скидки в БД нет ни у одной пары — изменений нет
	silent := []PairState{
		regPair("P1", "Товар", late, expiryOpt()),
		regPair("P0", "Первый", early, expiryOpt()),
		regPair("P1", "Товар", early, expiryOpt()),
	}
	if got := r.Replace(silent); len(got) != 0 {
		t.Fatalf("расчёт без скидок: %d изменений, want 0", len(got))
	}

	// второй расчёт: скидка появилась у всех трёх пар
	noisy := []PairState{
		regPair("P1", "Товар", late, expiryOpt(), appliedOpt(30)),
		regPair("P0", "Первый", early, expiryOpt(), appliedOpt(30)),
		regPair("P1", "Товар", early, expiryOpt(), appliedOpt(30)),
	}
	got := r.Replace(noisy)
	order := make([]string, 0, len(got))
	for _, c := range got {
		order = append(order, c.ProductID+" "+c.BestBefore.Format("02.01"))
	}
	wantOrder := []string{"P0 17.09", "P1 17.09", "P1 04.10"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("порядок изменений %v, want %v", order, wantOrder)
	}
}

// Ноль и NULL равнозначны «скидки нет»: переход 0 → NULL изменения не даёт.
func TestRegistryReplaceZeroEqualsNil(t *testing.T) {
	r := NewRegistry()

	if got := r.Replace([]PairState{regPair("A", "Ноль", day(5), expiryOpt(), appliedOpt(0))}); len(got) != 0 {
		t.Fatalf("0 в пустом реестре: %d изменений, want 0", len(got))
	}
	if got := r.Replace([]PairState{regPair("A", "Ноль", day(5), expiryOpt())}); len(got) != 0 {
		t.Fatalf("0 → NULL: %d изменений, want 0", len(got))
	}
}

// Digest делит активные строки по ёмкости окна (cap=2: ручная и сроковая в
// активных, избыточные — за ёмкостью) и печатает ровно тот же текст, что
// discounts.BuildDigest (golden).
func TestRegistryDigestSectionsAndGoldenText(t *testing.T) {
	r := NewRegistry()
	r.Replace([]PairState{
		regPair("p1", "Масло", day(11), surplusOpt(1.5)),
		regPair("p2", "Творог", day(8), manualOpt(40)),
		regPair("p3", "Сыр", day(5), expiryOpt()),
		regPair("p4", "Хлеб", day(12), surplusOpt(2.5)),
		regPair("p5", "Без скидки", day(20)),
	})

	now := time.Date(2026, time.September, 14, 9, 0, 0, 0, time.UTC)
	d := r.Digest(now, 2)

	if !d.Date.Equal(now) {
		t.Errorf("дата отчёта %v, want %v", d.Date, now)
	}
	if len(d.Discounts) != 2 || len(d.Surplus) != 2 {
		t.Fatalf("секции: активных %d, допродажа %d, want 2 и 2", len(d.Discounts), len(d.Surplus))
	}
	if d.Discounts[0].Source != discounts.SourceManual || d.Discounts[1].Source != discounts.SourceExpiry {
		t.Errorf("активные: %s, %s, want manual, expiry", d.Discounts[0].Source, d.Discounts[1].Source)
	}
	if d.Surplus[0].Coeff <= d.Surplus[1].Coeff {
		t.Errorf("за ёмкостью не по коэффициенту ↓: %+v", d.Surplus)
	}

	// тот же текст, что даёт доменный сборщик по независимо собранным строкам
	rows := []discounts.Row{
		{ProductID: "p1", Name: "Масло", BestBefore: day(11), Percent: 10, Source: discounts.SourceSurplus, Coeff: 1.5, DaysLeft: 11},
		{ProductID: "p2", Name: "Творог", BestBefore: day(8), Percent: 40, Source: discounts.SourceManual, DaysLeft: 8},
		{ProductID: "p3", Name: "Сыр", BestBefore: day(5), Percent: 30, Source: discounts.SourceExpiry, DaysLeft: 5},
		{ProductID: "p4", Name: "Хлеб", BestBefore: day(12), Percent: 10, Source: discounts.SourceSurplus, Coeff: 2.5, DaysLeft: 12},
	}
	want := discounts.BuildDigest(rows, 2)
	want.Date = now
	if got := d.Text(); got != want.Text() {
		t.Errorf("текст реестра и BuildDigest разошлись:\n%q\n%q", got, want.Text())
	}

	const golden = "Дайджест по скидкам · 14.09.2026\n\n" +
		"Позиции в скидках:\n" +
		"1. (Ручная) Творог (до 22.09) — 40%\n" +
		"2. (Срок) Сыр (до 19.09) — 30%\n" +
		"\n" +
		"Доступно для допродажи (сверх 2 активных):\n" +
		"1. (Избыток) Хлеб (до 26.09) — 10% (коэф 2,5)\n" +
		"2. (Избыток) Масло (до 25.09) — 10% (коэф 1,5)\n"
	if got := d.Text(); got != golden {
		t.Errorf("golden-текст:\n%q\nwant:\n%q", got, golden)
	}
}

// Обёртки UseCase: окно, очередь и отчёт под мутексом юзкейса, дата — из uc.now.
func TestUseCaseWindowQueueDigest(t *testing.T) {
	now := time.Date(2026, time.September, 14, 14, 0, 0, 0, time.UTC)
	uc := NewUseCase(nil, nil, nil, nil, nil, func() time.Time { return now })

	uc.reg.Replace([]PairState{
		regPair("p-manual", "Ручной", day(9), manualOpt(40)),
		regPair("p-surplus", "Избыточный", day(12), surplusOpt(2.5)),
	})

	window := uc.Window(1)
	if len(window) != 1 || window[0].Source != discounts.SourceManual {
		t.Fatalf("окно юзкейса: %+v", window)
	}
	queue := uc.Queue(1)
	if len(queue) != 1 || queue[0].Source != discounts.SourceSurplus {
		t.Fatalf("очередь юзкейса: %+v", queue)
	}
	if d := uc.Digest(1); !d.Date.Equal(now) {
		t.Errorf("дата отчёта %v, want %v", d.Date, now)
	}
}

// Метка канала и количество строки: (ТГ) — пока значение ТГ-колонки строго выше
// скидки сайта (подъём 16:00 доводит сайт до плана — метка гаснет сама), а
// количество — остаток пары, у избытка — план продаж по паре (у пары вне
// раскладки количества нет: печатать нечего). Решение владельца 24.09.2026.
func TestRowChannelLabelAndQty(t *testing.T) {
	tests := []struct {
		name         string
		pair         PairState
		wantTelegram bool
		wantQty      int64
	}{
		{
			"ТГ-колонка выше сайта — метка ТГ, количество — остаток пары",
			PairState{Expiry: new(int16(30)), Applied: new(int16(10)), TelegramPlain: new(int16(30)), Qty: 7},
			true, 7,
		},
		{
			"сайт догнал план (16:00) — метка гаснет",
			PairState{Expiry: new(int16(30)), Applied: new(int16(30)), TelegramPlain: new(int16(30)), Qty: 7},
			false, 7,
		},
		{
			"ручная ТГ выше ручной сайта — метка ТГ: ручная важнее plain",
			PairState{Manual: new(int16(30)), Applied: new(int16(30)), TelegramManual: new(int16(50)), Qty: 4},
			true, 4,
		},
		{
			"избыток — количество из плана продаж",
			PairState{Surplus: new(discounts.SurplusPercent()), Applied: new(discounts.SurplusPercent()), HasSurplus: true, Coeff: 2, Qty: 50, SurplusPlanQty: 24},
			false, 24,
		},
		{
			"избыток вне раскладки — количества нет",
			PairState{Surplus: new(discounts.SurplusPercent()), Applied: new(discounts.SurplusPercent()), HasSurplus: true, Coeff: 1.2, Qty: 50},
			false, 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.pair.Row()
			if row.Telegram != tc.wantTelegram {
				t.Errorf("метка канала: Telegram=%v, want %v", row.Telegram, tc.wantTelegram)
			}
			if row.Qty != tc.wantQty {
				t.Errorf("количество строки: %d, want %d", row.Qty, tc.wantQty)
			}
		})
	}
}

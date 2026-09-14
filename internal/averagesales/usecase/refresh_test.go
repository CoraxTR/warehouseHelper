package usecase

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/msclient/client"
)

// Пакетные обновления оборотов (шов модуля скидок): цена обновления — запрос
// на ПЕРИОД на пачку товаров, а не 13 запросов на товар.

var refreshNow = time.Date(2026, time.September, 14, 10, 0, 0, 0, time.Local) // понедельник

func newRefreshUC(repo *stubRepo, sales *stubSales, prods *stubProducts) *UseCase {
	uc := NewUseCase(repo, sales, prods)
	uc.now = func() time.Time { return refreshNow }

	return uc
}

func TestAveragesReadsWindowWithoutMS(t *testing.T) {
	sales := &stubSales{}
	week := currentPeriodStart(intervalWeek, refreshNow)
	repo := &stubRepo{weekly: []averagesales.TurnoverRow{ // по убыванию периода, как из БД
		{ProductID: "p1", PeriodStart: week, Qty: 5},
		{ProductID: "p1", PeriodStart: week.AddDate(0, 0, -7), Qty: 10},
		{ProductID: "p1", PeriodStart: week.AddDate(0, 0, -14), Qty: 20},
	}}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт", TrackWeekly: true},
	}}

	uc := newRefreshUC(repo, sales, prods)

	got, err := uc.Averages(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}
	if sales.calls != 0 {
		t.Errorf("Averages() сходил в МС %d раз, ожидалось 0 (только БД)", sales.calls)
	}
	// Неполное окно: завершённых 2 (< 5) → окно = 20, 10 + текущий 5 → 35/3.
	want := 35.0 / 3
	if math.Abs(got["p1"]-want) > 1e-9 {
		t.Errorf("Averages()[p1] = %v, want %v", got["p1"], want)
	}
}

func TestRefreshCurrentFetchesOnlyCurrentPeriod(t *testing.T) {
	sales := &stubSales{rowsFn: func(_, _ time.Time, _ string, _ client.ProfitFilter) []client.ProfitRow {
		return []client.ProfitRow{profitRow("p1", 8, 0)}
	}}
	month := currentPeriodStart(intervalMonth, refreshNow)
	repo := &stubRepo{monthly: []averagesales.TurnoverRow{
		{ProductID: "p1", PeriodStart: month, Qty: 8},
		{ProductID: "p1", PeriodStart: month.AddDate(0, -1, 0), Qty: 4},
	}}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}

	uc := newRefreshUC(repo, sales, prods)

	got, err := uc.RefreshCurrent(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("RefreshCurrent() error: %v", err)
	}
	if sales.calls != 1 {
		t.Errorf("RefreshCurrent() запросов в МС = %d, ожидался 1 (только текущий месяц)", sales.calls)
	}
	if len(repo.upsM) != 1 || !repo.upsM[0].PeriodStart.Equal(month) || repo.upsM[0].Qty != 8 {
		t.Errorf("RefreshCurrent() записал %v, ожидалась одна строка текущего месяца qty=8", repo.upsM)
	}
	// Завершённых 1 (< 12) → окно = 4 + текущий 8 → среднее 6.
	if math.Abs(got["p1"]-6) > 1e-9 {
		t.Errorf("RefreshCurrent()[p1] = %v, want 6", got["p1"])
	}
}

func TestRefreshWindowFetchesWholeWindow(t *testing.T) {
	sales := &stubSales{}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}

	uc := newRefreshUC(&stubRepo{}, sales, prods)

	if _, err := uc.RefreshWindow(context.Background(), []string{"p1"}); err != nil {
		t.Fatalf("RefreshWindow() error: %v", err)
	}
	// 12 завершённых месяцев + текущий — по одному запросу отчёта.
	if sales.calls != monthlyWindow+1 {
		t.Errorf("RefreshWindow() запросов в МС = %d, ожидалось %d", sales.calls, monthlyWindow+1)
	}
}

func TestRefreshWithoutProductsDoesNotTouchMS(t *testing.T) {
	sales := &stubSales{}
	uc := newRefreshUC(&stubRepo{}, sales, &stubProducts{})

	got, err := uc.RefreshCurrent(context.Background(), nil)
	if err != nil {
		t.Fatalf("RefreshCurrent(nil) error: %v", err)
	}
	if len(got) != 0 || sales.calls != 0 {
		t.Errorf("RefreshCurrent(nil) = %v, запросов в МС %d; ожидалась пустая карта без запросов", got, sales.calls)
	}
}

func TestAveragesWithoutSalesSkipsProduct(t *testing.T) {
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}
	uc := newRefreshUC(&stubRepo{}, &stubSales{}, prods)

	got, err := uc.Averages(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}
	if _, ok := got["p1"]; ok {
		t.Errorf("Averages() вернул товар без продаж: %v", got)
	}
}

// newMonthlyFixture — N месячных товаров с одной строкой текущего месяца и
// одним общим репозиторием-фикстурой (для проверок батч-чтения).
func newMonthlyFixture(n int, qty float64) (*stubRepo, *stubProducts, []string) {
	month := currentPeriodStart(intervalMonth, refreshNow)
	ids := make([]string, 0, n)
	repo := &stubRepo{}
	prods := &stubProducts{byID: make(map[string]averagesales.TurnoverProduct, n)}

	for i := range n {
		id := fmt.Sprintf("p%04d", i)
		ids = append(ids, id)
		prods.byID[id] = averagesales.TurnoverProduct{ID: id, UOM: "шт"}
		repo.monthly = append(repo.monthly, averagesales.TurnoverRow{
			ProductID: id, PeriodStart: month, Qty: qty,
		})
	}

	return repo, prods, ids
}

// Тысяча товаров — один батч-запрос окна (не запрос на товар), средние считаются
// по каждому товару из своей пачки.
func TestAveragesReadsWindowInBatches(t *testing.T) {
	const products = 1000

	repo, prods, ids := newMonthlyFixture(products, 7)
	sales := &stubSales{}
	uc := newRefreshUC(repo, sales, prods)

	got, err := uc.Averages(context.Background(), ids)
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}

	if len(repo.windowMIDs) != 1 || len(repo.windowMIDs[0]) != products {
		t.Fatalf("батч-чтений месячного окна = %d (id в первом: %d), want одно на %d товаров",
			len(repo.windowMIDs), len(repo.windowMIDs[0]), products)
	}
	if repo.lastCalls != 0 {
		t.Errorf("чтений окна «по товару» = %d, want 0 (N+1 нет)", repo.lastCalls)
	}
	if len(got) != products {
		t.Fatalf("Averages() вернул %d товаров, want %d", len(got), products)
	}
	for id, avg := range got {
		if math.Abs(avg-7) > 1e-9 {
			t.Fatalf("Averages()[%s] = %v, want 7 (текущий месяц, окно из одной строки)", id, avg)
		}
	}
}

// Число батч-запросов не растёт с числом товаров: 1 товар и 1000 товаров — по
// одному чтению ряда (внутри запроса товары идут пачками).
func TestAveragesBatchCallsDoNotGrowWithProducts(t *testing.T) {
	calls := func(n int) int {
		repo, prods, ids := newMonthlyFixture(n, 1)
		uc := newRefreshUC(repo, &stubSales{}, prods)

		if _, err := uc.Averages(context.Background(), ids); err != nil {
			t.Fatalf("Averages(%d товаров) error: %v", n, err)
		}

		return len(repo.windowMIDs)
	}

	if one, many := calls(1), calls(1000); one != 1 || many != 1 {
		t.Errorf("батч-чтений месячного окна: на 1 товар = %d, на 1000 товаров = %d, want 1 и 1", one, many)
	}
}

// Несколько товаров за один проход: свой ряд — один запрос, месячных два товара
// в одном запросе, недельный один; средние — по каждому товару.
func TestAveragesBatchesBothIntervals(t *testing.T) {
	month := currentPeriodStart(intervalMonth, refreshNow)
	week := currentPeriodStart(intervalWeek, refreshNow)
	repo := &stubRepo{
		monthly: []averagesales.TurnoverRow{ // период по убыванию, как из БД
			{ProductID: "m1", PeriodStart: month, Qty: 10},
			{ProductID: "m2", PeriodStart: month, Qty: 20},
		},
		weekly: []averagesales.TurnoverRow{
			{ProductID: "w1", PeriodStart: week, Qty: 4},
		},
	}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"m1": {ID: "m1", UOM: "шт"},
		"m2": {ID: "m2", UOM: "шт"},
		"w1": {ID: "w1", UOM: "шт", TrackWeekly: true},
	}}

	uc := newRefreshUC(repo, &stubSales{}, prods)

	got, err := uc.Averages(context.Background(), []string{"m1", "w1", "m2"})
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}

	if len(repo.windowMIDs) != 1 || len(repo.windowMIDs[0]) != 2 {
		t.Errorf("батч-чтений месячного окна = %v, want одно на m1+m2", repo.windowMIDs)
	}
	if len(repo.windowWIDs) != 1 || len(repo.windowWIDs[0]) != 1 {
		t.Errorf("батч-чтений недельного окна = %v, want одно на w1", repo.windowWIDs)
	}
	if repo.lastCalls != 0 {
		t.Errorf("чтений окна «по товару» = %d, want 0 (N+1 нет)", repo.lastCalls)
	}

	want := map[string]float64{"m1": 10, "m2": 20, "w1": 4}
	if len(got) != len(want) {
		t.Fatalf("Averages() = %v, want %v", got, want)
	}
	for id, v := range want {
		if math.Abs(got[id]-v) > 1e-9 {
			t.Errorf("Averages()[%s] = %v, want %v", id, got[id], v)
		}
	}
}

// Товар со строками только ЗА пределами окна (репозиторий их не возвращает) —
// как и товар без продаж: в карте средних отсутствует.
func TestAveragesSkipsProductOutsideWindow(t *testing.T) {
	month := currentPeriodStart(intervalMonth, refreshNow)
	old := month.AddDate(0, -(monthlyWindow + 2), 0) // глубже окна
	repo := &stubRepo{monthly: []averagesales.TurnoverRow{
		{ProductID: "p1", PeriodStart: old, Qty: 5},
	}}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}

	uc := newRefreshUC(repo, &stubSales{}, prods)

	got, err := uc.Averages(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}
	if _, ok := got["p1"]; ok {
		t.Errorf("Averages() вернул товар со строками вне окна: %v", got)
	}
}

// Батч-путь считает среднее тем же правилом окна, что и прежний расчёт по
// товару (splitWindow + windowAvg на тех же фикстурах, что в
// averagesales_test.go): 12 завершённых месяцев 1…12 (по убыванию периода) и
// текущий 3; самый дальний завершённый — 12, текущий его не перебивает →
// окно = 12 завершённых, среднее 78/12.
func TestAveragesBatchedMatchesWindowRule(t *testing.T) {
	month := currentPeriodStart(intervalMonth, refreshNow)
	rows := make([]averagesales.TurnoverRow, 0, monthlyWindow+1)
	rows = append(rows, averagesales.TurnoverRow{ProductID: "p1", PeriodStart: month, Qty: 3})
	for i := 1; i <= monthlyWindow; i++ {
		rows = append(rows, averagesales.TurnoverRow{
			ProductID: "p1", PeriodStart: month.AddDate(0, -i, 0), Qty: float64(i),
		})
	}

	repo := &stubRepo{monthly: rows}
	prods := &stubProducts{byID: map[string]averagesales.TurnoverProduct{
		"p1": {ID: "p1", UOM: "шт"},
	}}

	uc := newRefreshUC(repo, &stubSales{}, prods)

	got, err := uc.Averages(context.Background(), []string{"p1"})
	if err != nil {
		t.Fatalf("Averages() error: %v", err)
	}

	finished, current := splitWindow(rows, monthlyWindow, month)
	want, err := windowAvg(finished, current, monthlyWindow)
	if err != nil {
		t.Fatalf("windowAvg() error: %v", err)
	}
	if math.Abs(got["p1"]-*want) > 1e-9 {
		t.Errorf("Averages()[p1] = %v, want %v (прежнее правило окна)", got["p1"], *want)
	}
	if math.Abs(got["p1"]-78.0/12.0) > 1e-9 {
		t.Errorf("Averages()[p1] = %v, want %v", got["p1"], 78.0/12.0)
	}
}

func TestUniqueIDs(t *testing.T) {
	got := uniqueIDs([]string{"a", "", "a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("uniqueIDs() = %v, want [a b]", got)
	}
}

func TestSplitWindowLimitsFinished(t *testing.T) {
	start := currentPeriodStart(intervalWeek, refreshNow)
	rows := []averagesales.TurnoverRow{
		{ProductID: "p1", PeriodStart: start, Qty: 1},
		{ProductID: "p1", PeriodStart: start.AddDate(0, 0, -7), Qty: 2},
		{ProductID: "p1", PeriodStart: start.AddDate(0, 0, -14), Qty: 3},
		{ProductID: "p1", PeriodStart: start.AddDate(0, 0, 7), Qty: 4}, // будущее — пропуск
	}

	finished, current := splitWindow(rows, 1, start)
	if len(finished) != 1 || finished[0].Qty != 2 {
		t.Errorf("finished = %v, want ровно одну строку qty=2", finished)
	}
	if current == nil || current.Qty != 1 {
		t.Errorf("current = %v, want строку текущего периода qty=1", current)
	}
}

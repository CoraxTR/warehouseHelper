package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/msclient/client"
)

// Пакетные обновления оборотов для потребителей среднего (шов модуля скидок).
//
// Отличие от AverageSales: он работает с ОДНИМ товаром и перед расчётом
// перезапрашивает из МС каждый период окна (13 месяцев / 6 недель). Модулю
// скидок нужен оборот по СОТНЯМ товаров на каждом часовом тике, поэтому здесь
// запросы идут пачками: один запрос отчёта на период на группу товаров или
// пачку из noGroupBatch товаров — цена обновления падает в десятки раз.
//
// Полное окно перезапрашивается редко (09:00 и первый запуск дня: забирает
// возвраты задним числом), текущий незакрытый период — по запросу тика.
// Среднее всегда считается обычным правилом окна (windowAvg) по данным БД:
// завершённые периоды + текущий.

// Размеры окна средних: месячный — год, недельный — 5 недель (см. AGENTS.md
// модуля). Среднее считается по завершённым периодам окна и текущему.
const (
	monthlyWindow = 12
	weeklyWindow  = 5
)

// turnoverScope — какие периоды обновлять: всё окно (12 завершённых месяцев +
// текущий / 5 недель + текущий) или только текущий незакрытый. Тип вместо
// bool-параметра: контрол-флаг в параметрах запрещён (revive flag-parameter).
type turnoverScope int

const (
	scopeWindow      turnoverScope = iota // все периоды окна + текущий
	scopeCurrentOnly                      // только текущий незакрытый период
)

// RefreshWindow обновляет из МС ВСЕ периоды окна перечисленных товаров
// (12 завершённых месяцев + текущий / 5 недель + текущий) и возвращает
// действующий средний оборот за период (шт). Товары без продаж в карту не
// попадают (данных нет).
func (uc *UseCase) RefreshWindow(ctx context.Context, productIDs []string) (map[string]float64, error) {
	done := metrics.Track(trackPkg, "RefreshWindow")
	defer done()

	return uc.refreshTurnover(ctx, productIDs, scopeWindow)
}

// RefreshCurrent обновляет из МС только текущий незакрытый период перечисленных
// товаров и возвращает действующий средний оборот за период (шт): завершённые
// периоды берутся из БД, свежий — из отчёта. Рабочий вызов часового тика.
func (uc *UseCase) RefreshCurrent(ctx context.Context, productIDs []string) (map[string]float64, error) {
	done := metrics.Track(trackPkg, "RefreshCurrent")
	defer done()

	return uc.refreshTurnover(ctx, productIDs, scopeCurrentOnly)
}

// Averages возвращает действующий средний оборот за период (шт) по данным БД,
// НЕ обращаясь к МС: то, что известно про товары сейчас. Товары без продаж
// в карту не попадают.
//
// Окно читается батчем: по одному запросу на пачку товаров своего ряда
// (репозиторий бьёт список сам), то есть на 1000 товаров — единицы запросов
// вместо запроса на товар.
func (uc *UseCase) Averages(ctx context.Context, productIDs []string) (map[string]float64, error) {
	done := metrics.Track(trackPkg, "Averages")
	defer done()

	ids := uniqueIDs(productIDs)
	out := make(map[string]float64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	prods, err := uc.turnoverProducts(ctx, ids)
	if err != nil {
		return nil, err
	}

	// Два прохода — по ряду: недельные и месячные товары лежат в своих таблицах
	// и имеют свои размеры окна. Внутри прохода запрос один (пачки — дело
	// репозитория).
	for _, plan := range uc.windowPlans() {
		subset := plan.trackIDs(prods)
		if len(subset) == 0 {
			continue
		}

		rows, err := plan.window(ctx, subset, windowSince(plan.interval, plan.n, uc.now()))
		if err != nil {
			return nil, fmt.Errorf("обороты окна %d товаров: %w", len(subset), err)
		}

		byProduct := rowsByProduct(rows)
		for _, id := range subset {
			avg, err := rowsAverage(byProduct[id], plan, uc.now())
			if errors.Is(err, ErrNoData) {
				continue // продаж не было ни за один период — товара в карте нет
			}
			if err != nil {
				return nil, fmt.Errorf("среднее товара %s: %w", id, err)
			}
			out[id] = *avg
		}
	}

	return out, nil
}

// refreshTurnover — общий шаг обновления: товары → параметры окна → запросы
// отчёта пачками (по периодам) → апсёрт → средние из БД. scope выбирает набор
// периодов (тип вместо bool-флага).
func (uc *UseCase) refreshTurnover(ctx context.Context, productIDs []string, scope turnoverScope) (map[string]float64, error) {
	ids := uniqueIDs(productIDs)
	if len(ids) == 0 {
		return map[string]float64{}, nil
	}

	prods, err := uc.turnoverProducts(ctx, ids)
	if err != nil {
		return nil, err
	}

	active := make(map[string]averagesales.TurnoverProduct, len(prods))
	for _, p := range prods {
		active[p.ID] = p
	}

	for _, plan := range uc.windowPlans() {
		subset := plan.trackProducts(active)
		if len(subset) == 0 {
			continue
		}

		periods := []time.Time{currentPeriodStart(plan.interval, uc.now())}
		if scope == scopeWindow {
			periods = append(completedPeriodStarts(plan.interval, plan.n, uc.now()), periods...)
		}

		if err := uc.refreshPeriods(ctx, plan.interval, periods, subset); err != nil {
			return nil, err
		}
	}

	return uc.Averages(ctx, ids)
}

// turnoverProducts — срез каталога для перечисленных товаров (uom, вес,
// недельный учёт, группа); порядок — как отдал каталог.
func (uc *UseCase) turnoverProducts(ctx context.Context, ids []string) ([]averagesales.TurnoverProduct, error) {
	prods, err := uc.products.TurnoverProductsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("товары для оборотов: %w", err)
	}
	return prods, nil
}

// windowPlan — параметры окна интервала: размер окна, имя интервала отчёта и
// чтение окна СПИСКОМ товаров (батч, пачки внутри репозитория).
type windowPlan struct {
	n        int
	interval string
	window   func(ctx context.Context, productIDs []string, since time.Time) ([]averagesales.TurnoverRow, error)
}

// windowPlans — планы окон в порядке обхода: месячный ряд, затем недельный
// (прежние два прохода bool-цикла).
func (uc *UseCase) windowPlans() []windowPlan {
	return []windowPlan{uc.monthlyPlan(), uc.weeklyPlan()}
}

// monthlyPlan — план месячного окна: 12 завершённых месяцев + текущий.
func (uc *UseCase) monthlyPlan() windowPlan {
	return windowPlan{
		n:        monthlyWindow,
		interval: intervalMonth,
		window:   uc.repo.MonthlyTurnoverWindowByProducts,
	}
}

// weeklyPlan — план недельного окна: 5 завершённых недель + текущий.
func (uc *UseCase) weeklyPlan() windowPlan {
	return windowPlan{
		n:        weeklyWindow,
		interval: intervalWeek,
		window:   uc.repo.WeeklyTurnoverWindowByProducts,
	}
}

// weeklyTrack — план описывает недельный ряд (иначе — месячный): по этому
// признаку выбираются товары своего ряда, без bool-параметров в функциях.
func (p windowPlan) weeklyTrack() bool { return p.interval == intervalWeek }

// trackIDs — id товаров ряда плана (недельного или месячного) в порядке
// каталога: такие товары читаются одним батчем из своей таблицы.
func (p windowPlan) trackIDs(prods []averagesales.TurnoverProduct) []string {
	out := make([]string, 0, len(prods))
	for _, prod := range prods {
		if prod.TrackWeekly == p.weeklyTrack() {
			out = append(out, prod.ID)
		}
	}
	return out
}

// trackProducts — товары ряда плана (недельного или месячного) по id.
func (p windowPlan) trackProducts(active map[string]averagesales.TurnoverProduct) map[string]averagesales.TurnoverProduct {
	out := make(map[string]averagesales.TurnoverProduct, len(active))
	for id, prod := range active {
		if prod.TrackWeekly == p.weeklyTrack() {
			out[id] = prod
		}
	}
	return out
}

// refreshPeriods — запросы отчёта по каждому периоду: группы товаров одним
// запросом (ProductFolderID), безгрупповые — пачками по noGroupBatch.
func (uc *UseCase) refreshPeriods(ctx context.Context, interval string, periods []time.Time, active map[string]averagesales.TurnoverProduct) error {
	groups, singles := splitByFolder(active)

	for _, period := range periods {
		for folder, memberIDs := range groups {
			filter := client.ProfitFilter{ProductFolderID: folder}
			rows, err := uc.fetchGroupRows(ctx, period, interval, filter, toSet(memberIDs), active, map[string]int{})
			if err != nil {
				return fmt.Errorf("обновить обороты группы %s за %s: %w", folder, period.Format(time.DateOnly), err)
			}
			if err := uc.upsertByInterval(ctx, interval, rows); err != nil {
				return fmt.Errorf("записать обороты группы %s за %s: %w", folder, period.Format(time.DateOnly), err)
			}
		}

		for i := 0; i < len(singles); i += noGroupBatch {
			end := min(i+noGroupBatch, len(singles))
			chunk := singles[i:end]

			filter := client.ProfitFilter{ProductIDs: chunk}
			rows, err := uc.fetchGroupRows(ctx, period, interval, filter, toSet(chunk), active, map[string]int{})
			if err != nil {
				return fmt.Errorf("обновить обороты пачки за %s: %w", period.Format(time.DateOnly), err)
			}
			if err := uc.upsertByInterval(ctx, interval, rows); err != nil {
				return fmt.Errorf("записать обороты пачки за %s: %w", period.Format(time.DateOnly), err)
			}
		}
	}

	return nil
}

// rowsAverage — средний оборот товара по строкам его окна: разделение на
// завершённые периоды и текущий незакрытый (splitWindow) и обычное правило
// окна (windowAvg). ErrNoData — продаж не было ни за один период (товар в
// карту средних не попадает): sentinel вместо (nil, nil), иначе линт ловит
// nilnil.
func rowsAverage(rows []averagesales.TurnoverRow, plan windowPlan, now time.Time) (*float64, error) {
	finished, current := splitWindow(rows, plan.n, currentPeriodStart(plan.interval, now))

	avg, err := windowAvg(finished, current, plan.n)
	if errors.Is(err, ErrNoData) {
		return nil, ErrNoData
	}
	if err != nil {
		return nil, fmt.Errorf("окно %s: %w", plan.interval, err)
	}

	return avg, nil
}

// rowsByProduct — строки окна, разложенные по товарам: порядок строк внутри
// товара тот же, что отдал репозиторий (период по убыванию).
func rowsByProduct(rows []averagesales.TurnoverRow) map[string][]averagesales.TurnoverRow {
	out := make(map[string][]averagesales.TurnoverRow, len(rows))
	for _, r := range rows {
		out[r.ProductID] = append(out[r.ProductID], r)
	}
	return out
}

// splitWindow — разделение строк оборота (по убыванию периода) на завершённые
// периоды окна (не больше n) и текущий незакрытый период. Общее правило
// AverageSales и пакетных обновлений: строки за пределами окна пропускаются.
func splitWindow(rows []averagesales.TurnoverRow, n int, periodStart time.Time) (finished []averagesales.TurnoverRow, current *averagesales.TurnoverRow) {
	for _, r := range rows {
		switch {
		case r.PeriodStart.Before(periodStart):
			if len(finished) < n {
				finished = append(finished, r)
			}
		case r.PeriodStart.Equal(periodStart):
			c := r
			current = &c
		default:
			// Строки за пределами окна (будущие/дубли) — пропуск.
		}
	}
	return finished, current
}

// uniqueIDs — id без повторов и пустых значений, порядок первого появления.
func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

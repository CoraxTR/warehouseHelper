package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/returns"
	"warehouseHelper/internal/stock"
)

// Методы страницы «Возврат в продажу» (хаб Продукция): список активных
// событий, страница события с ожиданиями, приём возврата по сканам,
// ручное закрытие. Валидация сканов на сервере — авторитетная (страница
// дублирует её в JS для мгновенного отклика).

// ValidationError — сканы не сошлись с ожиданиями (HTTP 400).
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return e.Reason }

// PageState — состояние страницы события.
type PageState struct {
	Event    returns.ReturnEvent
	Expected []returns.Expected // nil, если событие обработано
	// Remaining — остаток живого заказа (read-only ориентир оператору):
	// только для kind=positions_removed.
	Remaining []returns.RemainingPosition
	Done      bool // возврат принят / закрыт вручную
}

// ListEvents — активные события (new/sent) для списка на хабе.
func (uc *UseCase) ListEvents(ctx context.Context) ([]returns.ReturnEvent, error) {
	return uc.repo.ListActive(ctx)
}

// EventPage — данные страницы «Возврат в продажу» для события: ожидания
// перечитываются из МС (в БД только id события). Обработанное событие —
// страница «уже обработано» без запросов к МС. Отменённый заказ, который к
// моменту открытия вернули в работу (статус больше не «Отменён»),
// расформировывать нельзя: событие авто-закрывается (как ручное, без действий
// в МС) и страница сразу показывает «закрыто».
func (uc *UseCase) EventPage(ctx context.Context, eventID string) (*PageState, error) {
	ev, err := uc.repo.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if ev.Status == returns.StatusDone {
		return &PageState{Event: *ev, Done: true}, nil
	}

	cancelled, err := uc.orderCancelledNow(ctx, ev)
	if err != nil {
		return nil, err
	}
	if !cancelled {
		// Авто-закрыто: closeEvent пометил ev (Status done, Manual true).
		return &PageState{Event: *ev, Done: true}, nil
	}

	state := &PageState{Event: *ev}
	state.Remaining = uc.remainingPositions(ctx, ev)

	expected, err := uc.buildExpected(ctx, ev)
	if err != nil {
		if errors.Is(err, returns.ErrNothingToReturn) {
			return state, nil // событие опустело — страница покажет «нечего возвращать»
		}
		return nil, err
	}
	state.Expected = expected
	return state, nil
}

// scannedUnit — разобранный скан куска: какую строку отчёта он закрыл и что из
// этикетки идёт в остатки.
type scannedUnit struct {
	row     returns.Expected // строка отчёта, которую закрыл скан
	weightG int64            // вес из этикетки, г (у штучного — вес-заглушка)
	expDate time.Time        // срок годности из этикетки
}

// AcceptReturn — приём возврата: валидация сканов против ожиданий (строгое
// равенство сумм, без допуска) и запись в остатки (AcceptStock). Для события
// отмены заказа перед записью остатков снимается резерв МС (reserve → 0, PUT)
// — только пока заказ всё ещё отменён (иначе событие авто-закрывается, см.
// orderCancelledNow). При успехе событие закрывается (done), сообщение в чате
// удаляется. Повторный вызов для обработанного события — ErrAlreadyDone
// (иначе задвоение остатков).
func (uc *UseCase) AcceptReturn(ctx context.Context, eventID string, scans []string) (int, error) {
	ev, err := uc.repo.GetEvent(ctx, eventID)
	if err != nil {
		return 0, err
	}
	if ev.Status == returns.StatusDone {
		return 0, returns.ErrAlreadyDone
	}

	cancelled, err := uc.orderCancelledNow(ctx, ev)
	if err != nil {
		return 0, err
	}
	if !cancelled {
		// Заказ вернули в работу после открытия страницы: событие уже
		// авто-закрыто — остатки не принимаем, резерв не трогаем.
		return 0, &ValidationError{Reason: "заказ больше не отменён — событие закрыто автоматически, расформирование отменено"}
	}

	expected, err := uc.buildExpected(ctx, ev)
	if err != nil {
		return 0, err
	}
	if len(expected) == 0 {
		return 0, &ValidationError{Reason: "нечего возвращать: отложенные позиции не найдены"}
	}

	units, err := matchScans(scans, expected)
	if err != nil {
		return 0, err
	}

	if ev.Kind == returns.KindCancelled {
		// МС резерв при отмене НЕ сбрасывает сам: снимаем PUT-ом до записи
		// остатков (ошибка МС не должна оставить рассинхрон «остатки приняты,
		// резерв висит»). Повтор после сбоя безопасен: reserve уже 0 — no-op.
		if err := uc.orders.ClearOrderReserves(ctx, ev.OrderID); err != nil {
			return 0, fmt.Errorf("clear order reserve %s: %w", ev.OrderID, err)
		}
	}

	if err := uc.stock.AcceptStock(ctx, aggregateLots(units)); err != nil {
		return 0, fmt.Errorf("stock accept return: %w", err)
	}

	if err := uc.closeEvent(ctx, ev, false); err != nil {
		return 0, err
	}

	return len(units), nil
}

// CloseManual — ручное закрытие возврата (куски не вернулись: потеряны,
// списаны, не нашлись; либо строку не гасит ни один скан — вес не совпал,
// позиция «слита» при подборе). В остатки не пишется, в МС ничего не меняется
// (резерв отменённого заказа остаётся как есть — закрытие вручную не снимает
// его: событие могло устареть, PUT вслепую перетёр бы уже изменённый заказ),
// сообщение удаляется, а в чат склада уходит список незакрытых позиций: склад
// пересчитывает по ним сроки построчно (решение владельца 10.09 — уведомление
// при КАЖДОМ ручном закрытии). scans — что оператор успел отсканировать
// (нужны только для состава списка; авторитетной сверки здесь нет).
func (uc *UseCase) CloseManual(ctx context.Context, eventID string, scans []string) error {
	ev, err := uc.repo.GetEvent(ctx, eventID)
	if err != nil {
		return err
	}
	if ev.Status == returns.StatusDone {
		return returns.ErrAlreadyDone
	}

	unclosed := uc.unclosedRows(ctx, ev, scans)

	if err := uc.closeEvent(ctx, ev, true); err != nil {
		return err
	}

	if len(unclosed) > 0 {
		if err := uc.notify.NotifyWarehouse(recountText(unclosed)); err != nil {
			slog.Error("returns recount notify failed", "event", ev.ID, "err", err)
		}
	}
	return nil
}

// unclosedRows — строки события, которые не закрыты присланными сканами (для
// уведомления о пересчёте): мягкий разбор без отказов — чужие/лишние сканы
// игнорируются, ошибка МС и пустые ожидания не блокируют закрытие (nil).
func (uc *UseCase) unclosedRows(ctx context.Context, ev *returns.ReturnEvent, scans []string) []returns.Expected {
	expected, err := uc.buildExpected(ctx, ev)
	if err != nil {
		if !errors.Is(err, returns.ErrNothingToReturn) {
			slog.Warn("returns recount: ожидания недоступны", "event", ev.ID, "err", err)
		}
		return nil
	}

	progress := make([]int64, len(expected))
	for _, raw := range scans {
		code, err := innercode.Parse(raw)
		if err != nil || code.Kind != innercode.KindItem {
			continue
		}
		idx := pickRow(expected, progress, code.InternalCode, int64(code.WeightG))
		if idx < 0 {
			continue
		}
		if expected[idx].Weighted {
			progress[idx] = expected[idx].ExpectedQty
			continue
		}
		progress[idx]++
	}

	out := make([]returns.Expected, 0, len(expected))
	for i := range expected {
		if progress[i] != expected[i].ExpectedQty {
			out = append(out, expected[i])
		}
	}
	return out
}

// recountText — текст уведомления складу о пересчёте сроков: позиции в порядке
// отчёта, дубли названий сводятся в «×N строк» (пересчитывать надо построчно).
func recountText(rows []returns.Expected) string {
	type group struct {
		name string
		code string
		n    int
	}
	groups := make([]group, 0, len(rows))
	byKey := make(map[string]int, len(rows))
	for _, r := range rows {
		key := r.Name + "\x00" + r.InternalCode
		if i, ok := byKey[key]; ok {
			groups[i].n++
			continue
		}
		byKey[key] = len(groups)
		groups = append(groups, group{name: r.Name, code: r.InternalCode, n: 1})
	}

	var sb strings.Builder
	sb.WriteString("Необходимо пересчитать сроки по позициям:")
	for _, g := range groups {
		sb.WriteString("\n— ")
		sb.WriteString(g.name)
		if g.code != "" {
			sb.WriteString(" (")
			sb.WriteString(g.code)
			sb.WriteByte(')')
		}
		if g.n > 1 {
			fmt.Fprintf(&sb, " ×%d строк", g.n)
		}
	}
	sb.WriteString("\nпострочно")
	return sb.String()
}

// remainingPositions — что осталось в живом заказе: read-only ориентир
// оператору, где физически искать товар. Только для события удаления позиций
// (у отмены состав тот же, что в ожиданиях); ошибка МС не роняет страницу —
// блок просто не показывается.
func (uc *UseCase) remainingPositions(ctx context.Context, ev *returns.ReturnEvent) []returns.RemainingPosition {
	if ev.Kind != returns.KindRemoved {
		return nil
	}

	positions, err := uc.audit.FetchOrderPositions(ctx, ev.OrderID)
	if err != nil {
		slog.Warn("returns: остаток заказа недоступен", "event", ev.ID, "order", ev.OrderID, "err", err)
		return nil
	}

	codes := make([]string, 0, len(positions))
	seen := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		if p.Assortment.Code == "" {
			continue
		}
		if _, ok := seen[p.Assortment.Code]; ok {
			continue
		}
		seen[p.Assortment.Code] = struct{}{}
		codes = append(codes, p.Assortment.Code)
	}

	products, err := uc.catalog.ProductsByInternalCodes(ctx, codes)
	if err != nil {
		slog.Warn("returns: каталог для остатка заказа недоступен", "event", ev.ID, "err", err)
		products = nil
	}

	out := make([]returns.RemainingPosition, 0, len(positions))
	for _, p := range positions {
		row := returns.RemainingPosition{
			InternalCode: p.Assortment.Code,
			Name:         p.Assortment.Name,
			Quantity:     p.Quantity,
		}
		if prod, ok := products[p.Assortment.Code]; ok {
			row.Weighted = prod.Weighted
		}
		out = append(out, row)
	}
	return out
}

// closeEvent — закрытие события: статус done с признаком manual и удаление
// сообщения из чата склада. Ошибка удаления сообщения не фатальна (лог; на
// повторном открытии страница покажет done). Мутирует ev под рендер ответа.
func (uc *UseCase) closeEvent(ctx context.Context, ev *returns.ReturnEvent, manual bool) error {
	if err := uc.repo.MarkDone(ctx, ev.ID, manual); err != nil {
		return fmt.Errorf("returns mark done %s: %w", ev.ID, err)
	}
	ev.Status = returns.StatusDone
	ev.Manual = manual

	if ev.ChatID != nil && ev.MessageID != nil {
		if err := uc.notify.DeleteMessage(ctx, *ev.ChatID, *ev.MessageID); err != nil {
			slog.Error("returns delete message failed", "event", ev.ID, "err", err)
		}
	}
	return nil
}

// orderCancelledNow — событие отмены заказа всё ещё актуально? Заказ должен
// находиться в статусе «Отменён»: если его вернули в работу (клиент передумал,
// склад не успел расформировать), возвращать товары в продажу нельзя — событие
// авто-закрывается как ручное (без действий в МС), расформирование
// блокируется. Проверка только для kind=order_cancelled (удаление позиций от
// статуса заказа не зависит); при ненастроенном детекте отмен (пустой
// CancelledStateID) событие считается актуальным без запроса к МС.
func (uc *UseCase) orderCancelledNow(ctx context.Context, ev *returns.ReturnEvent) (bool, error) {
	if ev.Kind != returns.KindCancelled || uc.cfg.CancelledStateID == "" {
		return true, nil
	}

	stateID, err := uc.orders.FetchOrderState(ctx, ev.OrderID)
	if err != nil {
		return false, err
	}
	if stateID == uc.cfg.CancelledStateID {
		return true, nil
	}

	if err := uc.closeEvent(ctx, ev, true); err != nil {
		return false, err
	}
	slog.Info("returns: заказ больше не отменён — событие закрыто автоматически",
		"event", ev.ID, "order", ev.OrderName, "state", stateID)
	return false, nil
}

// matchScans — сверка сканов с ожиданиями ПОСТРОЧНО (решение владельца 10.09):
// строку отчёта гасит свой скан — весовую закрывает ровно один скан с тем же
// весом (строго, без допуска: «бип и отказ»), штучную — ExpectedQty сканов.
// Каждый скан — кусок 29 (коробки 33 не участвуют). Любое несоответствие —
// ValidationError: событие остаётся открытым, выход — ручное закрытие.
func matchScans(scans []string, expected []returns.Expected) ([]scannedUnit, error) {
	// Прогресс по строкам отчёта (индекс = порядок строки/Idx).
	progress := make([]int64, len(expected))

	units := make([]scannedUnit, 0, len(scans))
	for _, raw := range scans {
		code, err := innercode.Parse(raw)
		if err != nil {
			return nil, &ValidationError{Reason: fmt.Sprintf("неверный штрих-код %q: %v", raw, err)}
		}
		if code.Kind != innercode.KindItem {
			return nil, &ValidationError{Reason: fmt.Sprintf("штрих-код %q — коробка (33): возвращаются только куски", raw)}
		}

		weightG := int64(code.WeightG)
		idx := pickRow(expected, progress, code.InternalCode, weightG)
		if idx < 0 {
			return nil, scanReject(expected, code.InternalCode, weightG)
		}

		units = append(units, scannedUnit{row: expected[idx], weightG: weightG, expDate: code.ExpDate})
		if expected[idx].Weighted {
			progress[idx] = expected[idx].ExpectedQty // весовой: строка гасится целиком
			continue
		}
		progress[idx]++ // штучный: этикетка = одна единица (вес-заглушка не участвует)
	}

	for i := range expected {
		if progress[i] != expected[i].ExpectedQty {
			return nil, &ValidationError{Reason: fmt.Sprintf(
				"вес не сходится: %s — отсканировано %s, ожидается %s",
				expected[i].Name, formatQtyAmt(&expected[i], progress[i]), formatQtyAmt(&expected[i], expected[i].ExpectedQty))}
		}
	}

	return units, nil
}

// pickRow — индекс свободной строки отчёта, которую гасит скан: тот же
// internal_code; весовой — строго тот же вес, и строка ещё не закрыта;
// штучный — первая незакрытая. -1 — подходящей строки нет.
func pickRow(expected []returns.Expected, progress []int64, code string, weightG int64) int {
	for i := range expected {
		if expected[i].InternalCode != code {
			continue
		}
		if expected[i].Weighted {
			if weightG > 0 && expected[i].ExpectedQty == weightG && progress[i] == 0 {
				return i
			}
			continue
		}
		if progress[i] < expected[i].ExpectedQty {
			return i
		}
	}
	return -1
}

// scanReject — расшифровка отказа скану, под который не нашлось строки: чужая
// позиция, перебор по штучной либо вес, которого нет ни в одной строке (в т.ч.
// «слитая» при подборе позиция). Выход один — ручное закрытие; склад получит
// уведомление о пересчёте сроков.
func scanReject(expected []returns.Expected, code string, weightG int64) *ValidationError {
	rows := make([]*returns.Expected, 0, len(expected))
	for i := range expected {
		if expected[i].InternalCode == code {
			rows = append(rows, &expected[i])
		}
	}
	if len(rows) == 0 {
		return &ValidationError{Reason: fmt.Sprintf("товар с кодом %s не в списке возврата", code)}
	}
	if !rows[0].Weighted {
		return &ValidationError{Reason: fmt.Sprintf("перебор: все строки %s уже закрыты", rows[0].Name)}
	}

	weights := make([]string, 0, len(rows))
	for _, r := range rows {
		weights = append(weights, formatQtyAmt(r, r.ExpectedQty))
	}
	return &ValidationError{Reason: fmt.Sprintf(
		"вес %.3f кг не подходит ни одной строке %s: ожидаются %s — закройте событие вручную, склад пересчитает сроки по этим позициям",
		float64(weightG)/1000, rows[0].Name, strings.Join(weights, " / "))}
}

// formatQtyAmt — количество для текста ошибки/ожидания в единицах строки.
func formatQtyAmt(e *returns.Expected, qty int64) string {
	if e.Weighted {
		return fmt.Sprintf("%.3f кг", float64(qty)/1000)
	}
	return fmt.Sprintf("%d шт", qty)
}

// aggregateLots — группировка принятых сканов по (товар, срок): каждый скан —
// одна единица остатка (product_stock keyed (product_id, best_before), qty —
// штуки; вес в остатках не хранится).
func aggregateLots(units []scannedUnit) []stock.LotIn {
	type key struct {
		productID  string
		bestBefore time.Time
	}
	counts := make(map[key]int64, len(units))
	for _, u := range units {
		k := key{productID: u.row.ProductID, bestBefore: u.expDate}
		counts[k]++
	}

	lots := make([]stock.LotIn, 0, len(counts))
	for k, qty := range counts {
		lots = append(lots, stock.LotIn{
			ProductID:  k.productID,
			BestBefore: k.bestBefore,
			Qty:        qty,
		})
	}
	return lots
}

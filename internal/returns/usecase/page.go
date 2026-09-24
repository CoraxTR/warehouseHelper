package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"warehouseHelper/internal/returns"
	"warehouseHelper/internal/scanmatch"
)

// Методы страницы «Возврат в продажу» (хаб Продукция): список активных
// событий, страница события с ожиданиями, приём возврата по сканам,
// ручное закрытие. Валидация сканов на сервере — авторитетная (страница
// дублирует её в JS для мгновенного отклика).

// ValidationError — сканы не сошлись с ожиданиями (HTTP 400). Тип живёт в
// общем ядре сверки сканов; здесь — алиас, чтобы API пакета не менялся.
type ValidationError = scanmatch.ValidationError

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
		// Ошибка помечается ErrReserveNotCleared: склад получает свой текст
		// (резерв снимается в МС вручную), а не общее «попробуйте позже».
		if err := uc.orders.ClearOrderReserves(ctx, ev.OrderID); err != nil {
			return 0, fmt.Errorf("%w (заказ %s): %w", returns.ErrReserveNotCleared, ev.OrderID, err)
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
		scan, err := scanmatch.ParseScan(raw)
		if err != nil {
			continue // чужой штрих-код/коробка — в мягком разборе игнор
		}
		idx := scanmatch.PickRow(expected, progress, scan.InternalCode, scan.WeightG)
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

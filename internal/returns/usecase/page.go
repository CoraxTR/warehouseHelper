package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Done     bool               // возврат принят / закрыт вручную
}

// ListEvents — активные события (new/sent) для списка на хабе.
func (uc *UseCase) ListEvents(ctx context.Context) ([]returns.ReturnEvent, error) {
	return uc.repo.ListActive(ctx)
}

// EventPage — данные страницы «Возврат в продажу» для события: ожидания
// перечитываются из МС (в БД только id события). Обработанное событие —
// страница «уже обработано» без запросов к МС.
func (uc *UseCase) EventPage(ctx context.Context, eventID string) (*PageState, error) {
	ev, err := uc.repo.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}

	state := &PageState{Event: *ev, Done: ev.Status == returns.StatusDone}
	if state.Done {
		return state, nil
	}

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

// scannedUnit — разобранный скан возвращаемого куска.
type scannedUnit struct {
	expected *returns.Expected
	weightG  int64     // весовой: вес из этикетки (г); штучный: 0 (счётчик единиц)
	expDate  time.Time // срок годности из этикетки
}

// AcceptReturn — приём возврата: валидация сканов против ожиданий (строгое
// равенство сумм, без допуска) и запись в остатки (AcceptStock). При успехе
// событие закрывается (done), сообщение в чате удаляется. Повторный вызов
// для обработанного события — ErrAlreadyDone (иначе задвоение остатков).
func (uc *UseCase) AcceptReturn(ctx context.Context, eventID string, scans []string) (int, error) {
	ev, err := uc.repo.GetEvent(ctx, eventID)
	if err != nil {
		return 0, err
	}
	if ev.Status == returns.StatusDone {
		return 0, returns.ErrAlreadyDone
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

	lots := aggregateLots(units)

	if err := uc.stock.AcceptStock(ctx, lots); err != nil {
		return 0, fmt.Errorf("stock accept return: %w", err)
	}

	if err := uc.repo.MarkDone(ctx, eventID, false); err != nil {
		return 0, fmt.Errorf("returns mark done %s: %w", eventID, err)
	}

	if ev.ChatID != nil && ev.MessageID != nil {
		if err := uc.notify.DeleteMessage(ctx, *ev.ChatID, *ev.MessageID); err != nil {
			slog.Error("returns delete message failed", "event", eventID, "err", err)
		}
	}

	return len(units), nil
}

// CloseManual — ручное закрытие возврата (куски не вернулись: потеряны,
// списаны и т.п.). В остатки ничего не пишется, сообщение удаляется.
func (uc *UseCase) CloseManual(ctx context.Context, eventID string) error {
	ev, err := uc.repo.GetEvent(ctx, eventID)
	if err != nil {
		return err
	}
	if ev.Status == returns.StatusDone {
		return returns.ErrAlreadyDone
	}

	if err := uc.repo.MarkDone(ctx, eventID, true); err != nil {
		return fmt.Errorf("returns manual close %s: %w", eventID, err)
	}

	if ev.ChatID != nil && ev.MessageID != nil {
		if err := uc.notify.DeleteMessage(ctx, *ev.ChatID, *ev.MessageID); err != nil {
			slog.Error("returns delete message failed", "event", eventID, "err", err)
		}
	}
	return nil
}

// matchScans — сверка сканов с ожиданиями: каждый скан — кусок 29 (коробки
// 33 не участвуют); товар по internal_code, накопление в пределах товара,
// строгое равенство сумм. Отклонения — ValidationError с расшифровкой.
func matchScans(scans []string, expected []returns.Expected) ([]scannedUnit, error) {
	byCode := make(map[string]*returns.Expected, len(expected))
	for i := range expected {
		byCode[expected[i].InternalCode] = &expected[i]
	}

	// Прогресс по строкам ожиданий (копии: мутируем аккумулятор, не домен).
	progress := make(map[string]int64, len(expected))

	units := make([]scannedUnit, 0, len(scans))
	for _, raw := range scans {
		code, err := innercode.Parse(raw)
		if err != nil {
			return nil, &ValidationError{Reason: fmt.Sprintf("неверный штрих-код %q: %v", raw, err)}
		}
		if code.Kind != innercode.KindItem {
			return nil, &ValidationError{Reason: fmt.Sprintf("штрих-код %q — коробка (33): возвращаются только куски", raw)}
		}

		exp, ok := byCode[code.InternalCode]
		if !ok {
			return nil, &ValidationError{Reason: fmt.Sprintf("товар с кодом %s не в списке возврата", code.InternalCode)}
		}

		var add int64
		if exp.Weighted {
			add = int64(code.WeightG) // вес куска в граммах — из этикетки
		} else {
			add = 1 // штучный: каждая этикетка = одна единица (вес-заглушка 00001 не участвует)
		}
		progress[exp.InternalCode] += add

		units = append(units, scannedUnit{expected: exp, weightG: int64(code.WeightG), expDate: code.ExpDate})
	}

	for i := range expected {
		e := &expected[i]
		if progress[e.InternalCode] != e.ExpectedQty {
			return nil, &ValidationError{Reason: fmt.Sprintf(
				"вес не сходится: %s — отсканировано %s, ожидается %s",
				e.Name, formatQtyAmt(e, progress[e.InternalCode]), formatQtyAmt(e, e.ExpectedQty))}
		}
	}

	return units, nil
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
		k := key{productID: u.expected.ProductID, bestBefore: u.expDate}
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

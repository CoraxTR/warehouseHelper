package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"warehouseHelper/internal/discounts"
)

// Расписание модуля: фоновый цикл, который раз в минуту проверяет, какой шаг
// пора сделать, и добирает пропущенное после сна/перезапуска. Маркеры дня —
// в БД (discount_day_flags), поэтому повторный запуск процесса не теряет
// пропущенные шаги; шаги идемпотентны и внутри себя проверяют свой маркер.

// Schedule — времена шагов (локальное время процесса, как APP_DAYSTATE_SNAPSHOT_TIME)
// и ёмкость ТГ-слота.
type Schedule struct {
	Morning     time.Duration // утренний шаг: окно оборотов, пересчёт по сроку, дайджест
	Plan        time.Duration // план ТГ-слота (14:00)
	Raise       time.Duration // подъём general до telegram (16:00)
	TelegramCap int           // ёмкость слота ТГ (APP_DISCOUNT_TELEGRAM_CAP)
}

// Run — цикл расписания: первый прогон сразу (догон после рестарта), далее раз
// в минуту. Завершается по отмене ctx; ошибки шагов логируются и не роняют цикл
// (следующий тик повторит — маркеры дня не дадут продублировать сделанное).
func (uc *UseCase) Run(ctx context.Context, s Schedule) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	uc.runSteps(ctx, s)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			uc.runSteps(ctx, s)
		}
	}
}

// runSteps — один проход расписания: утро (09:00), час избытка, ТГ-день
// (14:00 план, 16:00 подъём) в дни вт/чт.
func (uc *UseCase) runSteps(ctx context.Context, s Schedule) {
	now := uc.now()
	day := beginningOfDay(now)

	if err := uc.runMorning(ctx, day, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: утренний шаг: %v", err))
	}
	if err := uc.runSurplus(ctx, now); err != nil {
		slog.Info(fmt.Sprintf("discounts: пересчёт избытка: %v", err))
	}
	if !isTelegramDay(now) {
		return // слот ТГ и подъём — только вт/чт
	}
	if err := uc.runSlotPlan(ctx, day, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: план ТГ-слота: %v", err))
	}
	if err := uc.runRaise(ctx, day, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: подъём general: %v", err))
	}
}

// runMorning — утренний шаг после Morning: полное окно оборотов (возвраты
// задним числом — раз в день), пересчёт по сроку в КТ-дни, дайджест в общий чат.
// Шаги проверяют свои маркеры дня сами, порядок важен: дайджест идёт после
// пересчёта, иначе отчёт покажет вчерашние значения.
func (uc *UseCase) runMorning(ctx context.Context, day, now time.Time, s Schedule) error {
	if now.Sub(day) < s.Morning {
		return nil
	}

	if err := uc.refreshWindowOnce(ctx, day); err != nil {
		return fmt.Errorf("окно оборотов: %w", err)
	}
	if err := uc.RecalcExpiry(ctx, now); err != nil {
		return fmt.Errorf("пересчёт по сроку: %w", err)
	}
	if err := uc.SendDigest(ctx, now); err != nil {
		return fmt.Errorf("дайджест: %w", err)
	}
	return nil
}

// refreshWindowOnce — обновление всего окна оборотов товаров с лотами (один раз
// за день, маркер discounts.FlagTurnoverWindow): забирает возвраты задним числом по старым
// заказам. Товаров с лотами нет — шаг всё равно отмечается (иначе будем ходить
// в МС каждый тик).
func (uc *UseCase) refreshWindowOnce(ctx context.Context, day time.Time) error {
	done, err := uc.repo.DayFlagDone(ctx, day, discounts.FlagTurnoverWindow)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	ids, err := uc.productIDs(ctx, day)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := uc.turnover.RefreshWindow(ctx, ids); err != nil {
			return err
		}
	}
	if err := uc.repo.MarkDayFlag(ctx, day, discounts.FlagTurnoverWindow); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("discounts: окно оборотов обновлено (%d товаров)", len(ids)))
	return nil
}

// runSurplus — часовой пересчёт избытка: не чаще раза в час; после рестарта
// идёт сразу (маркер «за этот час не считали» теряется с памятью процесса, а
// лишний пересчёт безвреден: без изменений он ничего не пишет).
func (uc *UseCase) runSurplus(ctx context.Context, now time.Time) error {
	hour := now.Truncate(time.Hour)
	uc.mu.Lock()
	last := uc.lastSurplusHour
	uc.mu.Unlock()
	if hour.Equal(last) {
		return nil
	}

	if err := uc.RecalcSurplus(ctx, now); err != nil {
		return err
	}
	uc.mu.Lock()
	uc.lastSurplusHour = hour
	uc.mu.Unlock()
	return nil
}

// runSlotPlan — сборка плана ТГ-слота в Plan: раз в день (свой маркер внутри
// RunSlotPlan), в памяти держим день, чтобы не дёргать проверку каждую минуту.
func (uc *UseCase) runSlotPlan(ctx context.Context, day, now time.Time, s Schedule) error {
	if now.Sub(day) < s.Plan {
		return nil
	}
	uc.mu.Lock()
	last := uc.lastPlanDay
	uc.mu.Unlock()
	if last.Equal(day) {
		return nil
	}

	if err := uc.RunSlotPlan(ctx, now, s.TelegramCap); err != nil {
		return err
	}
	uc.mu.Lock()
	uc.lastPlanDay = day
	uc.mu.Unlock()
	return nil
}

// runRaise — подъём general до telegram в Raise (маркер внутри RunRaise).
func (uc *UseCase) runRaise(ctx context.Context, day, now time.Time, s Schedule) error {
	if now.Sub(day) < s.Raise {
		return nil
	}
	uc.mu.Lock()
	last := uc.lastRaiseDay
	uc.mu.Unlock()
	if last.Equal(day) {
		return nil
	}

	if err := uc.RunRaise(ctx, now); err != nil {
		return err
	}
	uc.mu.Lock()
	uc.lastRaiseDay = day
	uc.mu.Unlock()
	return nil
}

// isTelegramDay — день рассылки ТГ-слота: вт или чт (решение владельца 14.09).
func isTelegramDay(now time.Time) bool {
	switch now.Weekday() {
	case time.Tuesday, time.Thursday:
		return true
	default:
		return false
	}
}

// productIDs — id товаров с лотами во входе расчёта (без повторов, порядок
// первого появления): кого обновлять в окне оборотов.
func (uc *UseCase) productIDs(ctx context.Context, day time.Time) ([]string, error) {
	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(inputs))
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if _, ok := seen[in.ProductID]; ok {
			continue
		}
		seen[in.ProductID] = struct{}{}
		ids = append(ids, in.ProductID)
	}
	return ids, nil
}

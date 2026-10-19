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

	// Порядок важен: окно оборотов → пересчёт по событиям стока (затронутые
	// товары — в ближайшую минуту) → часовой избыток (он же наполняет реестр,
	// по которому строится отчёт) → лестница по сроку → дайджест 09:00. Отчёт
	// идёт ПОСЛЕ пересчётов: иначе он показывал бы вчерашние скидки, а на
	// свежем старте — пустой список.
	if err := uc.runWindow(ctx, day, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: окно оборотов: %v", err))
	}
	if err := uc.runAffected(ctx, now); err != nil {
		slog.Info(fmt.Sprintf("discounts: пересчёт по событиям стока: %v", err))
	}
	if err := uc.runSurplus(ctx, now); err != nil {
		slog.Info(fmt.Sprintf("discounts: пересчёт избытка: %v", err))
	}
	if err := uc.runExpiry(ctx, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: пересчёт по сроку: %v", err))
	}
	if err := uc.runDigest(ctx, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: дайджест: %v", err))
	}
	if !isTelegramDay(now) {
		return // слот ТГ и подъём — только вт/чт
	}
	if err := uc.runSlotPlan(ctx, day, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: план ТГ-слота: %v", err))
	}
	if err := uc.runMailing(ctx, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: живое письмо складу: %v", err))
	}
	if err := uc.runRaise(ctx, day, now, s); err != nil {
		slog.Info(fmt.Sprintf("discounts: подъём general: %v", err))
	}
}

// runMailing — живое письмо складу (решение владельца 18.10.2026): с 14:00 до
// 14:45 письмо пересобирается по жёсткой сетке 5 минут (по часам, а не от конца
// предыдущего пересчёта: не успели — такт пропущен, очередь не копится), в 14:45
// — финальная сборка снимка и запросы фото (с повторами до 14:50, если первый
// заход упал), с 14:45 до 15:00 — окно количеств каждую минуту.
func (uc *UseCase) runMailing(ctx context.Context, now time.Time, s Schedule) error {
	planAt := beginningOfDay(now).Add(s.Plan)

	switch {
	case now.Before(planAt.Add(mailingRefreshStep)):
		return nil // 14:00–14:04: письмо только что отправлено
	case now.Before(planAt.Add(mailingFreezeAfter)):
		slot := now.Truncate(mailingRefreshStep)
		if uc.mailPassDone(slot) {
			return nil
		}
		// Письма нет (пустой слот) — править нечего: «лишние» всё равно ждут
		// 14:45, разбирать их раньше нельзя.
		st, err := uc.repo.TodayDigest(ctx, beginningOfDay(now), discounts.ChatWarehouse)
		if err != nil {
			return err
		}
		if st == nil || st.SentAt == nil {
			return nil
		}
		if err := uc.refreshMailing(ctx, now, s.TelegramCap, mailingRebuild); err != nil {
			return err
		}
		uc.markMailPass(slot)

		return nil
	case now.Before(planAt.Add(mailingPublishHour)):
		freeze := planAt.Add(mailingFreezeAfter)
		if !uc.mailPassDone(freeze) {
			st, err := uc.repo.TodayDigest(ctx, beginningOfDay(now), discounts.ChatWarehouse)
			if err != nil {
				return err
			}
			if st != nil && st.SentAt != nil {
				err = uc.refreshMailing(ctx, now, s.TelegramCap, mailingFreeze)
			} else {
				// Рассылки нет вовсе — 14:45 всё равно наступает: «лишние»
				// получают скидку сайта, письма и фото не будет.
				err = uc.applyExtras(ctx, now, s.TelegramCap)
			}
			if err != nil {
				return err
			}
			uc.markMailPass(freeze)
		}

		return uc.syncMailingQty(ctx, now, planAt)
	default:
		return nil // 15:00 — рассылка публикуется, письмо больше не правим
	}
}

// runWindow — полное окно оборотов товаров с лотами после Morning (один раз за
// день, свой маркер): забирает возвраты задним числом по старым заказам.
func (uc *UseCase) runWindow(ctx context.Context, day, now time.Time, s Schedule) error {
	if now.Sub(day) < s.Morning {
		return nil
	}

	return uc.refreshWindowOnce(ctx, day)
}

// runExpiry — пересмотр лестницы по сроку после Morning (КТ-дни и маркер дня
// проверяет сам RecalcExpiry).
func (uc *UseCase) runExpiry(ctx context.Context, now time.Time, s Schedule) error {
	if now.Sub(beginningOfDay(now)) < s.Morning {
		return nil
	}

	return uc.RecalcExpiry(ctx, now)
}

// runDigest — дайджест в общий чат после Morning (маркер дня — внутри SendDigest).
// Идёт после пересчётов: реестр к этому моменту уже наполнен ими.
func (uc *UseCase) runDigest(ctx context.Context, now time.Time, s Schedule) error {
	if now.Sub(beginningOfDay(now)) < s.Morning {
		return nil
	}

	return uc.SendDigest(ctx, now)
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

	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return err
	}
	ids := inputProductIDs(inputs)
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

// runAffected — пересчёт по событиям стока: товары, помеченные MarkDirty
// (приёмка, расформирование заказа, ручная правка), считаются в ближайшую
// минуту, не дожидаясь смены часа и не глядя на КТ-день: минутный цикл Run
// сам работает дебунсом, а событие — это уже готовое «решение устарело».
// Решение по метке — полное (лестница по сроку + избыток), см. RecalcAffected.
//
// Час после такого пересчёта отмечается пройденным: полный часовой пересчёт в
// этом же проходе считал бы те же пары второй раз (избыток RecalcAffected
// пишет по всем парам, не только по затронутым), а сохранённый оборот у него
// тот же самый.
//
// Ошибка шага возвращает товары в метки: событие не должно потеряться из-за
// разового сбоя БД или МС — ближайшая минута повторит.
func (uc *UseCase) runAffected(ctx context.Context, now time.Time) error {
	dirty := uc.takeDirty()
	if len(dirty) == 0 {
		return nil
	}

	if err := uc.RecalcAffected(ctx, now, dirty); err != nil {
		uc.markAll(dirty)
		return err
	}

	uc.mu.Lock()
	uc.lastSurplusHour = now.Truncate(time.Hour)
	uc.mu.Unlock()
	return nil
}

// runSurplus — часовой пересчёт избытка: не чаще раза в час; после рестарта
// идёт сразу (маркер «за этот час не считали» теряется с памятью процесса, а
// лишний пересчёт безвреден: без изменений он ничего не пишет). Час события
// стока (runAffected) уже прошёл — повторно те же пары не считаем.
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
	wd := now.Weekday()
	return wd == time.Tuesday || wd == time.Thursday
}

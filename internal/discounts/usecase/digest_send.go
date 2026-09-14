// Отправка дайджеста наружу: утренняя рассылка в общий канал (09:00) и ответ
// бота на команду /скидки в чат отправителя.
//
// Строитель отчёта один — Registry.Digest (реестр активных пар); различаются
// только выводы: рассылка дня (маркер дня discounts.FlagDigestSent) и ответ на
// команду (без маркера — команда может приходить сколько угодно раз).
//
// Своих часов методы не заводят: время приходит параметром (SendDigest) или
// берётся из uc.now (реестр), время суток при этом не важно.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"warehouseHelper/internal/discounts"
)

// noDataText — ответ на /скидки, когда реестр пуст (расчёта ещё не было).
// Своего расчёта у метода нет — ни часов, ни выборки в БД, — поэтому вместо
// отчёта уходит короткий текст.
const noDataText = "Данных о скидках пока нет"

// SendDigest — дайджест 09:00 в общий канал: текст реестра, первый раз за день.
//
// Маркер дня уже стоит — отправки нет (второй тик дня и запуск после рестарта
// рассылку не повторяют). Ошибка чтения маркера, отправки или отметки
// возвращается наружу; маркер ставится ПОСЛЕ удачной отправки, поэтому
// неудачную рассылку догоняет следующий тик.
func (uc *UseCase) SendDigest(ctx context.Context, now time.Time) error {
	today := beginningOfDay(now)

	done, err := uc.repo.DayFlagDone(ctx, today, discounts.FlagDigestSent)
	if err != nil {
		return fmt.Errorf("маркер дня дайджеста: %w", err)
	}
	if done {
		return nil
	}

	text := uc.Digest().Text()
	if uc.common == nil {
		// Канал не подключён (нет токена/чата) — как в notifyChanges: текст
		// только в лог, маркер дня не ставим (канал могут подключить позже).
		slog.Info(fmt.Sprintf("discounts: дайджест (канал не подключён): %s", text))
		return nil
	}
	if err := uc.common.NotifyCommon(ctx, text); err != nil {
		return fmt.Errorf("рассылка дайджеста в общий канал: %w", err)
	}

	if err := uc.repo.MarkDayFlag(ctx, today, discounts.FlagDigestSent); err != nil {
		return fmt.Errorf("маркер дня дайджеста: %w", err)
	}
	return nil
}

// ReplyDigest — тот же отчёт по команде /скидки в чат отправителя.
//
// Маркер дня не трогается: команда не зависит от рассылки 09:00. Прав не
// проверяем — команду принимает бот, любой участник вправе увидеть отчёт.
// Реестр пуст — короткий ответ noDataText (расчёт не дублируем).
func (uc *UseCase) ReplyDigest(ctx context.Context, chatID int64) error {
	text := uc.Digest().Text()
	if uc.emptyRegistry() {
		text = noDataText
	}

	if uc.common == nil {
		slog.Info(fmt.Sprintf("discounts: ответ на /скидки в чат %d (канал не подключён): %s", chatID, text))
		return nil
	}
	if err := uc.common.SendDetails(ctx, chatID, text); err != nil {
		return fmt.Errorf("ответ на /скидки в чат %d: %w", chatID, err)
	}
	return nil
}

// emptyRegistry — расчёта ещё не было: реестр не дал ни одной активной строки.
// Смотрим окно (его верх — первая активная строка) и очередь избытка: ёмкость
// окна методу неизвестна, поэтому очередь берётся без окна (windowSize <= 0 —
// окно вмещает всё, значит избыточные строки были бы видны в окне).
func (uc *UseCase) emptyRegistry() bool {
	return len(uc.Window(1)) == 0 && len(uc.Queue(0)) == 0
}

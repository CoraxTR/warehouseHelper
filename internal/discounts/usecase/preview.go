// Предпросмотр плана дня: страница «Скидки» показывает, что уйдёт складу в
// рассылке 14:00. Расчёт тот же, что у RunSlotPlan, но без записи, истории и
// маркеров дня — предпросмотр ничего не меняет.
package usecase

import (
	"context"
	"fmt"

	"warehouseHelper/internal/discounts"
)

// DayPlanPreview — предпросмотр плана дня для страницы «Скидки»: текст, который
// получит чат склада, и счётчики по раскладке плана.
type DayPlanPreview struct {
	// Text — тот же отчёт, что уйдёт в чат склада (позиции слота помечены (ТГ),
	// лишние повышения идут своим источником со скидкой сайта сразу).
	Text string
	// SlotCount — позиций в рассылке (ТГ-колонка, сайт поднимется в 16:00).
	SlotCount int
	// ExtraCount — позиций, которым скидка сайта встанет сразу (в рассылку они
	// не идут).
	ExtraCount int
}

// PlanPreview — предпросмотр плана ТГ-слота (14:00) на текущий момент.
//
// capacity — ёмкость слота ТГ (APP_DISCOUNT_TELEGRAM_CAP). Расчёт читает свежий
// вход из БД и оборот (как сам план), но НИЧЕГО не пишет: ни скидок, ни истории
// рассылок, ни маркеров дня. Состав считается на момент вызова: до 14:00 он
// может измениться (приёмка, подбор, ручная правка, продажа пары) — поэтому это
// предпросмотр, а не обещание.
//
// Смысл блока: отчёт (дайджест 09:00, `/discounts`, страница) собирается по
// факту — стоящие скидки, — поэтому скидку дня до 14:00 видно только здесь.
func (uc *UseCase) PlanPreview(ctx context.Context, capacity int) (DayPlanPreview, error) {
	day := beginningOfDay(uc.now())

	inputs, err := uc.repo.LoadDiscountInput(ctx, day)
	if err != nil {
		return DayPlanPreview{}, fmt.Errorf("предпросмотр плана дня: вход: %w", err)
	}
	rates, err := uc.turnover.Averages(ctx, inputProductIDs(inputs))
	if err != nil {
		return DayPlanPreview{}, fmt.Errorf("предпросмотр плана дня: оборот: %w", err)
	}
	prev, err := uc.repo.LastDigestPairs(ctx)
	if err != nil {
		return DayPlanPreview{}, fmt.Errorf("предпросмотр плана дня: прошлая рассылка: %w", err)
	}

	pairs := Evaluate(inputs, rates, day)
	plan := buildDayPlan(pairs, prev, capacity)

	// Дата в шапке — как у сообщения складу (writeSlot): без неё отчёт печатает
	// нулевой год.
	digest := discounts.BuildDigest(uc.windowRows(pairs, plan.slot), uc.WindowCap())
	digest.Date = uc.now()

	return DayPlanPreview{
		Text:       digest.Text(),
		SlotCount:  len(plan.slot),
		ExtraCount: len(plan.extra),
	}, nil
}

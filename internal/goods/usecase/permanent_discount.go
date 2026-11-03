package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

// permDiscountLoc — таймзона склада: «сегодня» и дата окончания постоянной
// скидки считаются по московскому дню, а не по UTC сервера (03.11.2026).
var permDiscountLoc = time.FixedZone("MSK", 3*60*60)

// Границы постоянной скидки поставщика: 1..90 %. Верх — 90, а не 100: скидка
// 100 % отдавала бы товар бесплатно, а это уже подарок/списание, а не цена.
const (
	permanentDiscountMin int16 = 1
	permanentDiscountMax int16 = 90
)

// SetPermanentDiscount — постоянная скидка поставщика (products.permanent_discount,
// products.permanent_discount_until). Решение владельца 03.11.2026: такая скидка —
// пол канала сайта ВНЕ слотов, движок скидок читает эти колонки при пересчёте.
//
// Ввод ручной, поэтому проверки здесь: без даты окончания скидка была бы
// бессрочной и тихо держала бы пол канала; дата в прошлом уже ничего не значит.
// percent == nil && until == nil — снять скидку (пишем NULL/NULL).
func (uc *GoodsUseCase) SetPermanentDiscount(ctx context.Context, productID string, percent *int16, until *time.Time) error {
	done := metrics.Track(trackPkg, "SetPermanentDiscount")
	defer done()

	productID = strings.TrimSpace(productID)
	if productID == "" {
		return fmt.Errorf("не выбран товар: %w", domain.ErrProductNotFound)
	}

	switch {
	case percent == nil && until == nil:
		// снятие — пишем NULL/NULL, проверять нечего
	case percent == nil:
		return errors.New("нужен процент скидки")
	case until == nil:
		return errors.New("нужна дата окончания")
	case *percent < permanentDiscountMin || *percent > permanentDiscountMax:
		return fmt.Errorf("скидка должна быть от %d до %d %%", permanentDiscountMin, permanentDiscountMax)
	}

	var untilDate *time.Time
	if until != nil {
		d := dateOnlyMSK(*until)
		if d.Before(dateOnlyMSK(time.Now())) {
			return errors.New("дата уже прошла")
		}
		untilDate = &d
	}

	if err := uc.repo.SetProductPermanentDiscount(ctx, productID, percent, untilDate); err != nil {
		return fmt.Errorf("сохранить постоянную скидку: %w", err)
	}

	uc.notifyCatalogChanged(ctx)

	return nil
}

// PermanentDiscount — сохранённая постоянная скидка товара для мини-окна
// карточки позиции: процент и дата окончания (nil — не задана).
func (uc *GoodsUseCase) PermanentDiscount(ctx context.Context, productID string) (*int16, *time.Time, error) {
	done := metrics.Track(trackPkg, "PermanentDiscount")
	defer done()

	productID = strings.TrimSpace(productID)
	if productID == "" {
		return nil, nil, fmt.Errorf("не выбран товар: %w", domain.ErrProductNotFound)
	}

	percent, until, err := uc.repo.ProductPermanentDiscount(ctx, productID)
	if err != nil {
		return nil, nil, fmt.Errorf("прочитать постоянную скидку: %w", err)
	}

	return percent, until, nil
}

// dateOnlyMSK — календарный день МСК как UTC-полночь: колонка DATE, поэтому
// время суток и часовой пояс входа не должны сдвигать дату при записи в базу.
func dateOnlyMSK(t time.Time) time.Time {
	y, m, d := t.In(permDiscountLoc).Date()

	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

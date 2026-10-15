// Письмо складу (второе сообщение 14:00): позиции ТГ-рассылки готовым текстом
// для подписчиков. Сборка письма — из плана дня: одна позиция плана даёт один
// блок, поэтому разные сроки одного товара печатаются разными блоками (раскладку
// по срокам уже сделал план — добор из избытка идёт FIFO, см. SurplusSalePlan).
//
// Первое сообщение (окно скидок с метками каналов) собирает writeSlot, оно же
// отправляет и это: оба уходят одним шагом дня и отмечаются одним маркером.
package usecase

import (
	"fmt"
	"log/slog"

	"warehouseHelper/internal/discounts"
)

// sendMailing — второе сообщение складу за шаг 14:00. Пустой план — молчание:
// сообщений о пустом слоте шаг не шлёт вовсе (см. writeSlot).
//
// Ошибка отправки возвращается наружу: маркер дня не ставится, и шаг повторится
// целиком — отдельного признака отправки у письма нет (первое сообщение уйдёт
// второй раз, как и в случае сбоя отметки; рассылка от этого не теряется).
func (uc *UseCase) sendMailing(slot []slotPosition) error {
	if len(slot) == 0 {
		return nil
	}

	text := discounts.BuildMailing(mailingItems(slot))
	if uc.warehouse == nil {
		slog.Info(fmt.Sprintf("discounts: письмо складу (канал не подключён): %s", text))
		return nil
	}
	if err := uc.warehouse.NotifyWarehouseHTML(text); err != nil {
		return fmt.Errorf("письмо складу: %w", err)
	}

	return nil
}

// mailingItems — позиции письма из плана дня: количество — то, что ушло в ТГ по
// этой паре (план добора из избытка, у сроковых и ручных — остаток пары), скидка
// — скидка дня плана, срок — срок пары, цена и адрес карточки — из товара.
func mailingItems(slot []slotPosition) []discounts.MailingItem {
	items := make([]discounts.MailingItem, 0, len(slot))
	for _, s := range slot {
		items = append(items, discounts.MailingItem{
			Name:       s.pair.Name,
			SiteURL:    s.pair.SiteURL,
			UOM:        s.pair.UOM,
			SalePrice:  s.pair.SalePrice,
			Percent:    s.percent,
			Qty:        mailingQty(s),
			BestBefore: s.pair.BestBefore,
		})
	}

	return items
}

// mailingQty — сколько идёт в ТГ по позиции плана: у добора из избытка — план
// продаж пары, у остальных — остаток пары (то же число, что попадает в строку
// отчёта по этой паре).
func mailingQty(s slotPosition) int64 {
	if s.planQty != nil {
		return *s.planQty
	}

	return s.pair.Qty
}

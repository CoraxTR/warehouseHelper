// Пакет usecase — сценарии домена «Скидки»: расчёт значений пары (товар, срок),
// запись их через шов стока и уведомления о том, что человеку надо сделать со
// скидкой на сайте. Пакет — чистая логика: БД, телеграм и часы приходят швами
// (входы-параметры, интерфейсы), своих часов и соединений пакет не заводит.
package usecase

import (
	"fmt"
	"time"
)

// notifyLayout — формат даты в уведомлении: день и месяц, как в дайджесте.
const notifyLayout = "02.01"

// discountPercent — эффективное значение пары для уведомления: NULL и 0
// равнозначны «скидки нет», отрицательное значение (мусор из БД) читается так же.
// Ровно та же трактовка, что в discounts.Resolve, где скидкой считается > 0.
func discountPercent(p *int16) int16 {
	if p == nil || *p <= 0 {
		return 0
	}
	return *p
}

// NotifyText — текст уведомления о том, что человеку надо сделать со скидкой
// на сайте (канал general), по разнице эффективного значения пары (товар, срок)
// до записи и после. ok=false — уведомлять нечего.
//
// Четыре типа события (черновик §15 / дизайн модуля, 14.09.2026):
//
//	0/NULL → >0       — «поставить X%»
//	>0     → больше   — «поднять до X%»
//	>0     → меньше>0 — «понизить до X%»
//	>0     → 0/NULL   — «убрать скидку»
//
// Уведомляем не о входах/выходах в избыток, а только об изменении значения,
// которое человек переносит на сайт. Значение не изменилось (в том числе
// «нет → нет») → ok=false.
func NotifyText(name string, bestBefore time.Time, prev, next *int16) (text string, ok bool) {
	was, now := discountPercent(prev), discountPercent(next)
	date := bestBefore.Format(notifyLayout)
	switch {
	case was == now:
		return "", false
	case was == 0:
		return fmt.Sprintf("%s (до %s): Необходимо поставить скидку %d%%", name, date, now), true
	case now == 0:
		return fmt.Sprintf("%s (до %s): Необходимо убрать скидку", name, date), true
	case now > was:
		return fmt.Sprintf("%s (до %s): Необходимо поднять скидку до %d%%", name, date, now), true
	default:
		return fmt.Sprintf("%s (до %s): Необходимо понизить скидку до %d%%", name, date, now), true
	}
}

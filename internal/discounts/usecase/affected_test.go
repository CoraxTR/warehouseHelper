package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// affectedSchedule — расписание тестов событий стока: утро 09:00, план 14:00,
// подъём 16:00, ёмкость слота 10 (как в бою). Проходы тестов идут утром до
// 09:00, поэтому шаги утра (окно оборотов, лестница по сроку, дайджест) в них
// не участвуют — видно только пересчёт по событиям.
func affectedSchedule() Schedule {
	return Schedule{
		Morning:     9 * time.Hour,
		Plan:        14 * time.Hour,
		Raise:       16 * time.Hour,
		TelegramCap: 10,
	}
}

// Событие стока (расформирование заказа вернуло лот в остатки) в НЕ-КТ день:
// ближайший минутный проход расписания обязан вернуть товару скидку по сроку —
// записать ступень и уведомить людей. Без события то же окно автоматика в
// понедельник не пересматривает.
func TestRecalcAffectedStockEventReturnsExpiryOutOfExpiryDay(t *testing.T) {
	now := day(0).Add(8 * time.Hour) // понедельник, 08:00 — вне КТ-дней
	if expiryDay(now) {
		t.Fatalf("тест требует не-КТ день, а %s — КТ-день", now.Weekday())
	}
	h := newRecalcHarness(now,
		lotInput("p-affected", "Творог", day(5), 20, shelfLifeInput(30)), // D = 5 → ступень 40
		lotInput("p-quiet", "Молоко", day(5), 20, shelfLifeInput(30)),
	)
	ctx := context.Background()

	// Наполняем реестр (первый снапшот процесса уведомлений не даёт): до события
	// скидок нет ни у одного товара.
	if err := h.uc.RecalcSurplus(ctx, now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.common.texts, h.common.tries = nil, nil

	// Событие стока: расформированный лот вернулся в остатки.
	h.uc.MarkDirty("p-affected")
	h.uc.runSteps(ctx, affectedSchedule())

	if got := len(h.batches()); got != 1 {
		t.Fatalf("батчей записи: %d, ожидался один: %v", got, h.batches())
	}
	writes := h.batches()[0]
	if len(writes) != 1 {
		t.Fatalf("правок в батче: %d, ожидалась одна: %+v", len(writes), writes)
	}
	if w := writes[0]; w.ProductID != "p-affected" || w.General == nil || *w.General != 40 ||
		w.Source != discounts.SourceExpiry.String() {
		t.Errorf("правка события: %+v, ожидалась ступень по сроку 40 у p-affected", w)
	}

	want := []string{"Творог (до " + day(5).Format(notifyLayout) + "): Необходимо поставить скидку 40%"}
	if !reflect.DeepEqual(h.common.texts, want) {
		t.Errorf("уведомления %q, want %q", h.common.texts, want)
	}

	// Свежий оборот спрашивают ровно по товару события.
	if !reflect.DeepEqual(h.turn.asked, []string{"p-affected"}) {
		t.Errorf("свежий оборот спрашивали по %q, want [p-affected]", h.turn.asked)
	}
	// Маркеры дня событие не трогает: они про расписание, не про события.
	if h.flag(day(0), discounts.FlagExpiry) {
		t.Error("событие стока отметило маркер дня пересмотра лестницы")
	}
}

// Подбор заказа в ТГ-день (вт/чт): списание остатка — тоже событие стока, но
// поднимать скидку сайта раньше рассылки оно не имеет права. Ступень по сроку
// в ТГ-дни двигают план дня (14:00) и подъём (16:00), до них действуют старые
// значения (правило владельца 24.09.2026): иначе подписчик увидел бы в рассылке
// то, что сайт уже отдал покупателю.
func TestRecalcAffectedTelegramDayKeepsExpiryStill(t *testing.T) {
	now := day(1).Add(8 * time.Hour) // вторник, 08:00 — ТГ-день
	if !isTelegramDay(now) {
		t.Fatalf("тест требует ТГ-день, а %s — не он", now.Weekday())
	}
	h := newRecalcHarness(now,
		lotInput("p-affected", "Творог", day(5), 20, shelfLifeInput(30)), // D = 5 → ступень 40
	)
	ctx := context.Background()

	// Наполняем реестр (первый снапшот процесса уведомлений не даёт): до события
	// скидок у пары нет.
	if err := h.uc.RecalcSurplus(ctx, now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.common.texts, h.common.tries = nil, nil

	// Событие стока: подбор заказа списал часть остатка пары.
	h.uc.MarkDirty("p-affected")
	h.uc.runSteps(ctx, affectedSchedule())

	if got := h.batches(); len(got) != 0 {
		t.Errorf("батчи записи: %+v, want ни одного: в ТГ-день ступень по сроку двигает план дня, а не событие стока", got)
	}
	if len(h.common.texts) != 0 {
		t.Errorf("уведомления %q, want тишину: до подъёма скидка сайта не меняется", h.common.texts)
	}
}

// Тот же проход, но лот товара без событий: его ступень НЕ пишется — правило
// «вне КТ-дней лестница не пересматривается» осталось для остального окна,
// исключение — только помеченные товары.
func TestRecalcAffectedLeavesQuietProductsAlone(t *testing.T) {
	now := day(0).Add(8 * time.Hour) // понедельник, 08:00 — вне КТ-дней
	h := newRecalcHarness(now,
		lotInput("p-event", "Творог", day(5), 20, shelfLifeInput(30)),
		lotInput("p-quiet", "Молоко", day(5), 20, shelfLifeInput(30)),
	)
	ctx := context.Background()

	h.uc.MarkDirty("p-event")
	h.uc.runSteps(ctx, affectedSchedule())

	if got := len(h.batches()); got != 1 {
		t.Fatalf("батчей записи: %d, ожидался один: %v", got, h.batches())
	}
	for _, w := range h.batches()[0] {
		if w.ProductID == "p-quiet" {
			t.Errorf("ступень тихого товара записана вне КТ-дня: %+v", w)
		}
	}

	// В «БД» изменился только затронутый товар: у тихого колонка движка пуста.
	// (Окно реестра показывает РЕШЕНИЕ по всем парам — ступень видна в нём и вне
	// КТ-дней, поэтому проверяем именно записанное значение.)
	for _, in := range h.repo.inputs {
		if in.ProductID == "p-quiet" && in.GeneralPlain != nil {
			t.Errorf("general тихого товара в БД: %d, want пусто (вне КТ-дня ступень не пишется)", *in.GeneralPlain)
		}
		if in.ProductID == "p-event" && (in.GeneralPlain == nil || *in.GeneralPlain != 40) {
			t.Errorf("general затронутого товара в БД: %v, want 40", in.GeneralPlain)
		}
	}
	if h.flag(day(0), discounts.FlagExpiry) {
		t.Error("плановый пересмотр лестницы вне КТ-дня отметился маркером дня")
	}

	// Следующий часовой пересчёт того же дня ступень тихого товара тоже не
	// пишет: для остального окна правило КТ-дней не изменилось.
	if err := h.uc.RecalcSurplus(ctx, now.Add(3*time.Hour)); err != nil {
		t.Fatalf("RecalcSurplus (следующий час): %v", err)
	}
	if got := len(h.batches()); got != 1 {
		t.Errorf("часовой пересчёт вне КТ-дня записал %d батчей, ожидался 1: %v", got, h.batches()[1:])
	}
}

// Пустой dirty и тот же час: проход расписания не пересчитывает ничего — ни
// записи, ни уведомлений, ни обращений к снапшоту входа и обороту. Механику
// видно на следующем проходе с событием: он считает сразу, а час остаётся
// отмеченным (полного часового пересчёта тем же проходом нет).
func TestRunStepsNoRecalcWithoutEvents(t *testing.T) {
	now := day(0).Add(8 * time.Hour)
	h := newRecalcHarness(now, lotInput("p-one", "Творог", day(5), 20, shelfLifeInput(30)))
	ctx := context.Background()
	s := affectedSchedule()

	// Первый проход: событий нет, час избытка закрывается, реестр наполняется
	// (записывать нечего: вне КТ-дня ступень не пишет никто).
	h.uc.runSteps(ctx, s)
	if got := len(h.batches()); got != 0 {
		t.Fatalf("первый проход без событий записал %d батчей: %v", got, h.batches())
	}
	loads, avgCalls := h.repo.loads, h.turn.avgCalls
	h.common.texts, h.common.tries = nil, nil

	// Тот же час, событий нет: снапшот входа и оборот не переспрашивают.
	h.uc.runSteps(ctx, s)
	if h.repo.loads != loads || h.turn.avgCalls != avgCalls {
		t.Errorf("проход без событий и смены часа пересчитывал: снапшот %d → %d, оборот %d → %d",
			loads, h.repo.loads, avgCalls, h.turn.avgCalls)
	}
	if len(h.batches()) != 0 || len(h.common.texts) != 0 {
		t.Errorf("проход без событий: записи %v, уведомления %q", h.batches(), h.common.texts)
	}

	// Событие стока в тот же час: пересчёт идёт сразу (ступень + уведомление),
	// и час остаётся отмеченным — второго, полного пересчёта в проходе нет.
	h.uc.MarkDirty("p-one")
	h.uc.runSteps(ctx, s)
	if got := len(h.batches()); got != 1 {
		t.Fatalf("проход с событием записал %d батчей, ожидался 1: %v", got, h.batches())
	}
	want := []string{"Творог (до " + day(5).Format(notifyLayout) + "): Необходимо поставить скидку 40%"}
	if !reflect.DeepEqual(h.common.texts, want) {
		t.Errorf("уведомления %q, want %q", h.common.texts, want)
	}
	if h.turn.avgCalls != avgCalls+1 {
		t.Errorf("оборот спрашивали %d раз, ожидался %d: час события не отмечен",
			h.turn.avgCalls, avgCalls+1)
	}
}

// Сбой пересчёта не теряет событие: метки остаются за товарами, и ближайший
// проход (уже без сбоя) считает их так, будто событие пришло только что.
func TestRunAffectedKeepsEventOnError(t *testing.T) {
	now := day(0).Add(8 * time.Hour)
	h := newRecalcHarness(now, lotInput("p-one", "Творог", day(5), 20, shelfLifeInput(30)))
	ctx := context.Background()
	s := affectedSchedule()

	// Событие пришло, а снапшот входа недоступен: шаг падает, записи нет.
	h.uc.MarkDirty("p-one")
	h.turn.avgErr = errors.New("БД недоступна")
	h.uc.runSteps(ctx, s)
	if got := len(h.batches()); got != 0 {
		t.Fatalf("проход со сбоем записал %d батчей: %v", got, h.batches())
	}

	// Сбой прошёл: тот же товар пересчитан без нового события стока.
	h.turn.avgErr = nil
	h.uc.runSteps(ctx, s)
	if got := len(h.batches()); got != 1 {
		t.Fatalf("событие потерялось после сбоя: батчей %d, ожидался 1: %v", got, h.batches())
	}
	if w := h.batches()[0][0]; w.ProductID != "p-one" || w.General == nil || *w.General != 40 {
		t.Errorf("правка после сбоя: %+v, ожидалась ступень по сроку 40 у p-one", w)
	}
}

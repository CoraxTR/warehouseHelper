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
	h.tasks.texts, h.tasks.tries = nil, nil

	// Событие стока: расформированный лот вернулся в остатки — остаток вырос.
	h.uc.MarkGrown("p-affected")
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

	want := []string{"Поставить скидку 40% на Творог сроки до: " + day(5).Format(notifyLayout)}
	if !reflect.DeepEqual(h.tasks.texts, want) {
		t.Errorf("уведомления %q, want %q", h.tasks.texts, want)
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

// Рост остатка в ТГ-день (вт/чт): приёмка вернула лот в остатки, ступень по
// сроку на пустое место есть — но поднимать скидку сайта раньше рассылки
// событие не имеет права. В ТГ-дни ступень двигают план дня (14:00) и подъём
// (16:00), до них действуют старые значения (правило владельца 24.09.2026):
// иначе подписчик увидел бы в рассылке то, что сайт уже отдал покупателю.
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
	h.tasks.texts, h.tasks.tries = nil, nil

	// Событие стока: приёмка вернула остаток пары (рост).
	h.uc.MarkGrown("p-affected")
	h.uc.runSteps(ctx, affectedSchedule())

	if got := h.batches(); len(got) != 0 {
		t.Errorf("батчи записи: %+v, want ни одного: в ТГ-день ступень по сроку двигает план дня, а не событие стока", got)
	}
	if len(h.tasks.texts) != 0 {
		t.Errorf("уведомления %q, want тишину: до подъёма скидка сайта не меняется", h.tasks.texts)
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

	h.uc.MarkGrown("p-event")
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
	h.tasks.texts, h.tasks.tries = nil, nil

	// Тот же час, событий нет: снапшот входа и оборот не переспрашивают.
	h.uc.runSteps(ctx, s)
	if h.repo.loads != loads || h.turn.avgCalls != avgCalls {
		t.Errorf("проход без событий и смены часа пересчитывал: снапшот %d → %d, оборот %d → %d",
			loads, h.repo.loads, avgCalls, h.turn.avgCalls)
	}
	if len(h.batches()) != 0 || len(h.tasks.texts) != 0 {
		t.Errorf("проход без событий: записи %v, уведомления %q", h.batches(), h.tasks.texts)
	}

	// Событие стока в тот же час: пересчёт идёт сразу (ступень + уведомление),
	// и час остаётся отмеченным — второго, полного пересчёта в проходе нет.
	h.uc.MarkGrown("p-one")
	h.uc.runSteps(ctx, s)
	if got := len(h.batches()); got != 1 {
		t.Fatalf("проход с событием записал %d батчей, ожидался 1: %v", got, h.batches())
	}
	want := []string{"Поставить скидку 40% на Творог сроки до: " + day(5).Format(notifyLayout)}
	if !reflect.DeepEqual(h.tasks.texts, want) {
		t.Errorf("уведомления %q, want %q", h.tasks.texts, want)
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

	// Событие пришло (приёмка прибавила остаток), а снапшот входа недоступен:
	// шаг падает, записи нет.
	h.uc.MarkGrown("p-one")
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

// Пара уже со скидкой: событие стока вне КТ-дня стоящее значение не поднимает —
// ступень пишется только на пустое место (решение владельца 28.09.2026). Кейс с
// сайта 28.09.2026: в субботу плановый пересмотр поставил лоту 30 %, в понедельник
// подбор заказа отметил товар событием — и «ступень на сегодня» (40 %) ушла бы на
// сайт на день раньше плана (вторник: план дня 14:00 и подъём 16:00).
func TestRecalcAffectedKeepsAppliedExpiryStill(t *testing.T) {
	now := day(0).Add(8 * time.Hour) // понедельник, 08:00 — вне КТ-дней
	h := newRecalcHarness(now,
		// D = 5 → ступень 40; у первой пары в канале сайта стоит 10 %.
		lotInput("p-applied", "Творог", day(5), 20, shelfLifeInput(30), plainInput(10)),
		lotInput("p-empty", "Молоко", day(5), 20, shelfLifeInput(30)),
	)
	ctx := context.Background()

	// Наполняем реестр (первый снапшот процесса уведомлений не даёт).
	if err := h.uc.RecalcSurplus(ctx, now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.tasks.texts, h.tasks.tries = nil, nil

	// Событие стока по обоим товарам: приёмка прибавила остаток.
	h.uc.MarkGrown("p-applied", "p-empty")
	h.uc.runSteps(ctx, affectedSchedule())

	if got := len(h.batches()); got != 1 {
		t.Fatalf("батчей записи: %d, ожидался один: %v", got, h.batches())
	}
	writes := h.batches()[0]
	if len(writes) != 1 {
		t.Fatalf("правок в батче: %d, ожидалась одна: %+v", len(writes), writes)
	}
	if w := writes[0]; w.ProductID != "p-empty" || w.General == nil || *w.General != 40 ||
		w.Source != discounts.SourceExpiry.String() {
		t.Errorf("правка события: %+v, ожидалась ступень 40 у пары без скидки (p-empty)", w)
	}

	// Значение пары со скидкой не тронуто: ступень на пустое место пишем, стоящее
	// не поднимаем.
	for _, in := range h.repo.inputs {
		if in.ProductID == "p-applied" && (in.GeneralPlain == nil || *in.GeneralPlain != 10) {
			t.Errorf("general пары со скидкой стал %v, want 10: ступень вне КТ-дня стоящее значение не поднимает", in.GeneralPlain)
		}
	}

	want := []string{"Поставить скидку 40% на Молоко сроки до: " + day(5).Format(notifyLayout)}
	if !reflect.DeepEqual(h.tasks.texts, want) {
		t.Errorf("уведомления %q, want %q", h.tasks.texts, want)
	}
}

// Подбор заказа вне КТ-дня: остаток пары списан, и ступень по сроку расчёт НЕ
// ставит — роста не было (решение владельца 30.09.2026). Кейс с сайта 30.09.2026
// (среда): кладовщик подобрал соус в заказ, а в чат ушло «Поставить скидку 40%».
// Контроль в том же тесте: под приёмкой (рост) та же пара ступень получает —
// значит «ничего не записали» ниже означает запрет подбора, а не пустую фикстуру.
func TestRecalcAffectedPickDoesNotFillEmptyPlace(t *testing.T) {
	ctx := context.Background()
	now := day(0).Add(8 * time.Hour) // понедельник, 08:00 — вне КТ-дней
	if expiryDay(now) {
		t.Fatalf("тест требует не-КТ день, а %s — КТ-день", now.Weekday())
	}
	fixture := func() *recalcHarness {
		return newRecalcHarness(now,
			lotInput("p-picked", "Соус", day(5), 20, shelfLifeInput(30)), // D = 5 → ступень 40
		)
	}

	// Контроль: приёмка прибавила остаток — ступень 40 уходит на сайт.
	hGrown := fixture()
	hGrown.uc.MarkGrown("p-picked")
	hGrown.uc.runSteps(ctx, affectedSchedule())
	if got := len(hGrown.batches()); got != 1 {
		t.Fatalf("под приёмкой батчей %d, ожидался 1: %v", got, hGrown.batches())
	}
	if w := hGrown.batches()[0][0]; w.General == nil || *w.General != 40 {
		t.Errorf("под приёмкой правка: %+v, ожидалась ступень 40", w)
	}

	// Подбор: остаток списан, роста нет — ни записи, ни уведомления.
	h := fixture()
	if err := h.uc.RecalcSurplus(ctx, now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.tasks.texts, h.tasks.tries = nil, nil
	avgCalls := h.turn.avgCalls

	h.uc.MarkDirty("p-picked")
	h.uc.runSteps(ctx, affectedSchedule())

	if got := h.batches(); len(got) != 0 {
		t.Errorf("батчи записи: %+v, want ни одного: подбор ступень по сроку не ставит", got)
	}
	if len(h.tasks.texts) != 0 {
		t.Errorf("уведомления %q, want тишину: подбор не даёт сайту новую скидку", h.tasks.texts)
	}
	// Событие не потерялось: оборот по товару спросили, значит пересчёт был.
	if h.turn.avgCalls != avgCalls+1 {
		t.Errorf("оборот спрашивали %d раз, ожидался %d: событие подбора должно пересчитываться",
			h.turn.avgCalls, avgCalls+1)
	}
}

// Подбор заказа, у которого ушёл избыток: СТОЯЩЕЕ значение пары остаётся на
// месте, хотя «положенное сегодня» (ступень по сроку) уже выше него — выход по
// владельцу значения НЕ поднимает (решение владельца 05.10.2026). Кейс с сайта
// 05.10.2026 (понедельник): подбор мяса убрал избыток группы, значение пары
// держал ТГ-день (20 %), а выход по владельцу записал на сайт ступень на
// сегодня (30 %) — в чат ушло «Поднять скидку до 30 %», и сайт получил ступень
// вторника в понедельник. Подъём — работа планового пересмотра (план 14:00 и
// подъём 16:00 КТ-дня, утро субботы), поэтому в тесте проверяем и то, что
// часовой пересчёт избытка значение тоже не поднимает.
func TestRecalcAffectedPickDoesNotRaiseOwnedValue(t *testing.T) {
	ctx := context.Background()
	now := day(0).Add(12 * time.Hour) // понедельник, 12:00 — вне КТ-дней, день начат
	if expiryDay(now) {
		t.Fatalf("тест требует не-КТ день, а %s — КТ-день", now.Weekday())
	}
	fixture := func() *recalcHarness {
		return newRecalcHarness(now,
			// D = 7 → ступень на сегодня 30, D = 8 → тоже 30: обе пары ждут
			// подъёма вторника. Стоит у них 20 % владельца «эскалация» —
			// значение поставлено прошлым ТГ-днём.
			lotInput("p-meat", "Мясник Праймбиф. Охл.", day(7), 4, shelfLifeInput(30),
				plainInput(20), ownerInput(discounts.OwnerEscalation.String()),
				telegramInput(20), telegramOwnerInput(discounts.OwnerEscalation.String())),
			lotInput("p-meat", "Мясник Праймбиф. Охл.", day(8), 5, shelfLifeInput(30),
				plainInput(20), ownerInput(discounts.OwnerEscalation.String()),
				telegramInput(20), telegramOwnerInput(discounts.OwnerEscalation.String())),
		)
	}

	// Контроль: до подбора избыток группы есть — основания для выхода нет, и
	// значения остаются на месте.
	hQuiet := fixture()
	hQuiet.turnover("p-meat", 30) // 1 шт/день: накопленного остатка больше скорости
	if err := hQuiet.uc.RecalcSurplus(ctx, now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	if got := hQuiet.batches(); len(got) != 0 {
		t.Fatalf("до подбора правки: %+v, want ни одной: избыток на месте", got)
	}

	// Подбор: из дальнего лота списали 4 шт — избытка группы не стало у обеих пар.
	h := fixture()
	h.turnover("p-meat", 30)
	if err := h.uc.RecalcSurplus(ctx, now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	h.tasks.texts, h.tasks.tries = nil, nil
	h.repo.inputs[1].Qty = 1

	h.uc.MarkDirty("p-meat")
	h.uc.runSteps(ctx, affectedSchedule())

	// Главное: подбора не хватило, чтобы поднять сайту скидку до ступени.
	for _, b := range h.batches() {
		for _, w := range b {
			if w.General != nil && *w.General > 20 {
				t.Errorf("подбор поднял значение пары: %+v, want не выше стоящих 20 %%", w)
			}
		}
	}
	for _, in := range h.repo.inputs {
		if in.GeneralPlain == nil || *in.GeneralPlain != 20 {
			t.Errorf("general пары в БД стал %v, want 20: выход по владельцу не поднимает", in.GeneralPlain)
		}
	}
	if len(h.tasks.texts) != 0 {
		t.Errorf("уведомления %q, want тишину: сайту скидку не поднимали", h.tasks.texts)
	}
	// Работа по паре у выхода всё же была: ТГ-колонку он чистит по-своему
	// основанию (план ТГ-дня отработан, избытка нет) — иначе зависшая 20 %
	// всплывёт меткой в отчёте при пустой колонке сайта (дефект 02.10.2026).
	for _, in := range h.repo.inputs {
		if in.TelegramPlain != nil {
			t.Errorf("ТГ-колонка пары в БД %d, want пусто (основание ушло)", *in.TelegramPlain)
		}
	}

	// Час спустя: полный пересчёт избытка правило не обходит.
	before := len(h.batches())
	if err := h.uc.RecalcSurplus(ctx, now.Add(3*time.Hour)); err != nil {
		t.Fatalf("RecalcSurplus (следующий час): %v", err)
	}
	for _, b := range h.batches()[before:] {
		for _, w := range b {
			if w.General != nil && *w.General > 20 {
				t.Errorf("часовой пересчёт поднял значение пары: %+v, want не выше 20 %%", w)
			}
		}
	}
}

// Событие стока правит только затронутые товары: у чужой пары основание тоже
// ушло (план добора выполнен), но её снятие — работа часового пересчёта избытка,
// а не подбора соседа (решение владельца 05.10.2026). Кейс с сайта 05.10.2026:
// точечный пересчёт обходил все пары сразу, и одно событие подбора могло снять
// скидку и уведомить людей по товару, которого подбор не касался.
func TestRecalcAffectedDoesNotTouchOtherProducts(t *testing.T) {
	ctx := context.Background()
	now := day(0).Add(12 * time.Hour) // понедельник, 12:00
	h := newRecalcHarness(now,
		// Товар события: подбор списал остаток (роста не было) — своих правок
		// у пары нет ни до, ни после.
		lotInput("p-picked", "Соус", day(7), 1, shelfLifeInput(30)),
		// Чужой товар: значение 20 % держал ТГ-день, избыток ещё есть — пару
		// отпустит выполненный план добора (план сохранил ТГ-день, события
		// стока по этому товару не было).
		lotInput("p-other", "Сыр", day(10), 100,
			plainInput(20), ownerInput(discounts.OwnerEscalation.String())),
	)
	h.turnover("p-other", 30) // избыток есть: пару отпускает именно план

	// Наполняем реестр и закрываем час: дальше проходы расписания делают только
	// пересчёт по событиям.
	h.uc.runSteps(ctx, affectedSchedule())
	if got := h.batches(); len(got) != 0 {
		t.Fatalf("проход наполнения записал: %+v, want ни одной правки", got)
	}
	h.tasks.texts, h.tasks.tries = nil, nil

	// План добора выполнен — у чужой пары появилось что снимать.
	h.repo.plans = map[discounts.LotKey]discounts.LotPlan{
		{ProductID: "p-other", BestBefore: day(10)}: {Initial: 120, Plan: 10},
	}

	// Событие стока по другому товару.
	h.uc.MarkDirty("p-picked")
	h.uc.runSteps(ctx, affectedSchedule())

	if got := h.batches(); len(got) != 0 {
		t.Errorf("пересчёт события правил чужие товары: %+v, want ни одной правки", got)
	}
	if len(h.tasks.texts) != 0 {
		t.Errorf("уведомления %q, want тишину: чужого товара событие не касается", h.tasks.texts)
	}
	if got := h.repo.inputs[1].GeneralPlain; got == nil || *got != 20 {
		t.Errorf("значение чужой пары в БД %v, want 20 (его снимает часовой пересчёт)", got)
	}

	// Часовой пересчёт избытка ту же правку делает по своему расписанию.
	if err := h.uc.RecalcSurplus(ctx, now.Add(time.Hour)); err != nil {
		t.Fatalf("RecalcSurplus (следующий час): %v", err)
	}
	batches := h.batches()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("часовой пересчёт: батчи %+v, ожидалась одна правка по p-other", batches)
	}
	if w := batches[0][0]; w.ProductID != "p-other" || w.General == nil ||
		*w.General != discounts.SurplusPercent() {
		t.Errorf("правка часового пересчёта: %+v, ожидалось расчётное значение p-other", w)
	}
}

// Ручная правка скидки на странице «Сроки» — событие БЕЗ роста: пустое место
// соседней пары она ступенью не заполняет (вне КТ-дней ступень двигает план
// дня, а не правка менеджера). Контроль: то же событие с ростом остатка ступень
// на дальнюю пару пишет.
func TestRecalcAffectedManualEditDoesNotFillEmptyPlace(t *testing.T) {
	ctx := context.Background()
	now := day(0).Add(8 * time.Hour)
	fixture := func() *recalcHarness {
		return newRecalcHarness(now,
			// Ближняя пара: ручная скидка менеджера 15 — место занято.
			lotInput("p-manual", "Творог", day(3), 10, shelfLifeInput(30), manualInput(15)),
			// Дальняя пара того же товара: в канале сайта пусто.
			lotInput("p-manual", "Творог", day(5), 10, shelfLifeInput(30)),
		)
	}

	// Контроль: рост остатка — дальняя пара получает свою ступень 40.
	hGrown := fixture()
	hGrown.uc.MarkGrown("p-manual")
	hGrown.uc.runSteps(ctx, affectedSchedule())
	gotExpiry := 0
	for _, b := range hGrown.batches() {
		for _, w := range b {
			if w.Source == discounts.SourceExpiry.String() {
				gotExpiry++
			}
		}
	}
	if gotExpiry != 1 {
		t.Fatalf("под приёмкой ступеней по сроку %d, ожидалась 1: %v", gotExpiry, hGrown.batches())
	}

	// Ручная правка: ступень на пустое место не пишется.
	h := fixture()
	if err := h.uc.RecalcSurplus(ctx, now); err != nil {
		t.Fatalf("RecalcSurplus (наполнение): %v", err)
	}
	before := len(h.batches())

	if err := h.uc.OnManualDiscountChanged(ctx, "p-manual"); err != nil {
		t.Fatalf("OnManualDiscountChanged: %v", err)
	}
	for _, b := range h.batches()[before:] {
		for _, w := range b {
			if w.Source == discounts.SourceExpiry.String() {
				t.Errorf("ручная правка поставила ступень по сроку: %+v", w)
			}
		}
	}
}

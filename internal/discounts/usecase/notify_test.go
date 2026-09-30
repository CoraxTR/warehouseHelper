package usecase

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
)

// fakeTaskOpener — шов модуля «Внутренние задачи» в тестах: texts — открытые
// задачи (тексты по порядку), kinds — их виды, tries — все попытки открытия,
// err — ошибка шва.
type fakeTaskOpener struct {
	texts []string
	kinds []domain.TaskKind
	tries []string
	err   error
}

func (o *fakeTaskOpener) Open(_ context.Context, kind domain.TaskKind, text string) error {
	o.tries = append(o.tries, text)
	if o.err != nil {
		return o.err
	}
	o.texts = append(o.texts, text)
	o.kinds = append(o.kinds, kind)
	return nil
}

// notifyDate — срок из golden-строк (22.06).
func notifyDate() time.Time {
	return time.Date(2026, time.June, 22, 0, 0, 0, 0, time.UTC)
}

// notifyDate2 — второй срок позиции из golden-строк (05.07): тексты с 29.09.2026
// перечисляют сроки, к которым относится действие.
func notifyDate2() time.Time {
	return time.Date(2026, time.July, 5, 0, 0, 0, 0, time.UTC)
}

// Golden: четыре точных текста задач (канал general, формат 02.01, сроки через
// запятую) и их виды: действие в начале строки, имя товара и сроки — после
// (решения 25.09.2026 и 29.09.2026).
func TestNotifyTextGolden(t *testing.T) {
	const name = "Творог"
	tests := []struct {
		title    string
		dates    []time.Time
		prev     *int16
		next     *int16
		want     string
		wantKind domain.TaskKind
	}{
		{"поставить один срок", []time.Time{notifyDate()}, nil, new(int16(20)),
			"Поставить скидку 20% на Творог сроки до: 22.06", domain.TaskKindDiscountPut},
		{"поставить два срока", []time.Time{notifyDate(), notifyDate2()}, nil, new(int16(20)),
			"Поставить скидку 20% на Творог сроки до: 22.06, 05.07", domain.TaskKindDiscountPut},
		{"поднять", []time.Time{notifyDate()}, new(int16(20)), new(int16(30)),
			"Поднять скидку до 30% на Творог сроки до: 22.06", domain.TaskKindDiscountRaise},
		{"понизить", []time.Time{notifyDate()}, new(int16(20)), new(int16(10)),
			"Понизить скидку до 10% на Творог сроки до: 22.06", domain.TaskKindDiscountLower},
		{"убрать", []time.Time{notifyDate()}, new(int16(20)), nil,
			"Убрать скидку с: Творог сроки до: 22.06", domain.TaskKindDiscountRemove},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got, kind, ok := NotifyText(name, tt.dates, tt.prev, tt.next)
			if !ok {
				t.Fatal("ok=false, want true")
			}
			if got != tt.want {
				t.Errorf("текст = %q, want %q", got, tt.want)
			}
			if kind != tt.wantKind {
				t.Errorf("вид = %q, want %q", kind, tt.wantKind)
			}
		})
	}
}

// Четыре перехода и «значение не изменилось» — в том числе 0 и NULL как
// равнозначное «скидки нет».
func TestNotifyTextTransitions(t *testing.T) {
	dates := []time.Time{notifyDate()}
	tests := []struct {
		title    string
		prev     *int16
		next     *int16
		want     string
		wantKind domain.TaskKind
		ok       bool
	}{
		// было 0/NULL → стало >0: «поставить»
		{"нет → есть", nil, new(int16(20)), "Поставить скидку 20% на Плов сроки до: 22.06", domain.TaskKindDiscountPut, true},
		{"ноль → есть", new(int16(0)), new(int16(20)), "Поставить скидку 20% на Плов сроки до: 22.06", domain.TaskKindDiscountPut, true},

		// было >0 → стало больше: «поднять»
		{"10 → 20", new(int16(10)), new(int16(20)), "Поднять скидку до 20% на Плов сроки до: 22.06", domain.TaskKindDiscountRaise, true},
		{"10 → 50", new(int16(10)), new(int16(50)), "Поднять скидку до 50% на Плов сроки до: 22.06", domain.TaskKindDiscountRaise, true},

		// было >0 → стало меньше, но >0: «понизить»
		{"50 → 20", new(int16(50)), new(int16(20)), "Понизить скидку до 20% на Плов сроки до: 22.06", domain.TaskKindDiscountLower, true},
		{"30 → 10", new(int16(30)), new(int16(10)), "Понизить скидку до 10% на Плов сроки до: 22.06", domain.TaskKindDiscountLower, true},

		// было >0 → стало 0/NULL: «убрать»
		{"есть → нет", new(int16(20)), nil, "Убрать скидку с: Плов сроки до: 22.06", domain.TaskKindDiscountRemove, true},
		{"есть → ноль", new(int16(20)), new(int16(0)), "Убрать скидку с: Плов сроки до: 22.06", domain.TaskKindDiscountRemove, true},

		// значение не изменилось — задачи нет
		{"нет → нет", nil, nil, "", "", false},
		{"нет → нет (ноль)", nil, new(int16(0)), "", "", false},
		{"ноль → ноль", new(int16(0)), new(int16(0)), "", "", false},
		{"20 → 20", new(int16(20)), new(int16(20)), "", "", false},
		{"50 → 50", new(int16(50)), new(int16(50)), "", "", false},

		// краевые: отрицательное значение из БД — тоже «нет»
		{"минус → нет", new(int16(-5)), new(int16(0)), "", "", false},
		{"нет → минус", nil, new(int16(-5)), "", "", false},
		{"минус → есть", new(int16(-5)), new(int16(20)), "Поставить скидку 20% на Плов сроки до: 22.06", domain.TaskKindDiscountPut, true},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got, kind, ok := NotifyText("Плов", dates, tt.prev, tt.next)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("текст = %q, want %q", got, tt.want)
			}
			if kind != tt.wantKind {
				t.Errorf("вид = %q, want %q", kind, tt.wantKind)
			}
		})
	}
}

// Сроков может не быть вовсе (реестр не нашёл подходящих лотов): шаблон не
// ломается — подсказка без дат лучше, чем пропущенное действие.
func TestNotifyTextWithoutDates(t *testing.T) {
	got, kind, ok := NotifyText("Творог", nil, nil, new(int16(20)))
	if !ok {
		t.Fatal("ok=false, want true")
	}
	const want = "Поставить скидку 20% на Творог сроки до: "
	if got != want {
		t.Errorf("текст = %q, want %q", got, want)
	}
	if kind != domain.TaskKindDiscountPut {
		t.Errorf("вид = %q, want %q", kind, domain.TaskKindDiscountPut)
	}
}

// Пустое имя товара не паникует и не портит шаблон (имя приходит из МС).
func TestNotifyTextEmptyName(t *testing.T) {
	got, kind, ok := NotifyText("", []time.Time{notifyDate()}, nil, new(int16(20)))
	if !ok {
		t.Fatal("ok=false, want true")
	}
	const want = "Поставить скидку 20% на  сроки до: 22.06"
	if got != want {
		t.Errorf("текст = %q, want %q", got, want)
	}
	if kind != domain.TaskKindDiscountPut {
		t.Errorf("вид = %q, want %q", kind, domain.TaskKindDiscountPut)
	}
	if !strings.Contains(got, "сроки до: 22.06") {
		t.Errorf("шаблон поехал: %q", got)
	}
}

// Изменения скидки позиции открываются задачами текстами правила NotifyText:
// четыре перехода со своими видами, а неизменившееся значение молчит. Одно
// изменение = одна задача, даже если в тексте перечислено два срока.
func TestNotifyChangesOpensTasks(t *testing.T) {
	tasks := &fakeTaskOpener{}
	uc := NewUseCase(nil, nil, nil, nil, tasks, nil, nil)

	uc.notifyChanges(context.Background(), []Change{
		{Name: "Творог", Dates: []time.Time{notifyDate()}, Prev: nil, Next: new(int16(20))},
		{Name: "Плов", Dates: []time.Time{notifyDate(), notifyDate2()}, Prev: new(int16(20)), Next: new(int16(30))},
		{Name: "Сыр", Dates: []time.Time{notifyDate()}, Prev: new(int16(20)), Next: new(int16(10))},
		{Name: "Хлеб", Dates: []time.Time{notifyDate()}, Prev: new(int16(20)), Next: nil},
		{Name: "Кефир", Dates: []time.Time{notifyDate()}, Prev: new(int16(20)), Next: new(int16(20))},
	})

	want := []string{
		"Поставить скидку 20% на Творог сроки до: 22.06",
		"Поднять скидку до 30% на Плов сроки до: 22.06, 05.07",
		"Понизить скидку до 10% на Сыр сроки до: 22.06",
		"Убрать скидку с: Хлеб сроки до: 22.06",
	}
	wantKinds := []domain.TaskKind{
		domain.TaskKindDiscountPut,
		domain.TaskKindDiscountRaise,
		domain.TaskKindDiscountLower,
		domain.TaskKindDiscountRemove,
	}
	if !reflect.DeepEqual(tasks.texts, want) {
		t.Errorf("задачи %q, want %q", tasks.texts, want)
	}
	if !reflect.DeepEqual(tasks.kinds, wantKinds) {
		t.Errorf("виды %q, want %q", tasks.kinds, wantKinds)
	}
	if len(tasks.tries) != 4 {
		t.Errorf("попыток открытия %d, want 4 (неизменившееся значение молчит)", len(tasks.tries))
	}
}

// Ошибка открытия задачи пересчёт не роняет: остальные изменения всё равно
// уходят (тексты только в логе).
func TestNotifyChangesOpenerError(t *testing.T) {
	tasks := &fakeTaskOpener{err: errors.New("телеграм недоступен")}
	uc := NewUseCase(nil, nil, nil, nil, tasks, nil, nil)

	uc.notifyChanges(context.Background(), []Change{
		{Name: "Творог", Dates: []time.Time{notifyDate()}, Prev: nil, Next: new(int16(20))},
		{Name: "Плов", Dates: []time.Time{notifyDate()}, Prev: nil, Next: new(int16(30))},
	})

	if len(tasks.texts) != 0 {
		t.Errorf("открыто %q при ошибке шва", tasks.texts)
	}
	if len(tasks.tries) != 2 {
		t.Errorf("попыток открытия %d, want 2", len(tasks.tries))
	}
}

// Шов задач не подключён (канал не сконфигурирован): изменение не роняет
// расчёт — текст уходит только в лог.
func TestNotifyChangesNilOpener(_ *testing.T) {
	uc := NewUseCase(nil, nil, nil, nil, nil, nil, nil)

	uc.notifyChanges(context.Background(), []Change{
		{Name: "Творог", Dates: []time.Time{notifyDate()}, Prev: nil, Next: new(int16(20))},
	})
}

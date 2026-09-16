package usecase

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// notifyDate — срок из golden-строк (22.06).
func notifyDate() time.Time {
	return time.Date(2026, time.June, 22, 0, 0, 0, 0, time.UTC)
}

// Golden: четыре точных текста уведомлений (канал general, формат 02.01).
func TestNotifyTextGolden(t *testing.T) {
	const name = "Творог"
	tests := []struct {
		title      string
		prev, next *int16
		want       string
	}{
		{"поставить", nil, new(int16(20)), "Творог (до 22.06): Необходимо поставить скидку 20%"},
		{"поднять", new(int16(20)), new(int16(30)), "Творог (до 22.06): Необходимо поднять скидку до 30%"},
		{"понизить", new(int16(20)), new(int16(10)), "Творог (до 22.06): Необходимо понизить скидку до 10%"},
		{"убрать", new(int16(20)), nil, "Творог (до 22.06): Необходимо убрать скидку"},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got, ok := NotifyText(name, notifyDate(), tt.prev, tt.next)
			if !ok {
				t.Fatal("ok=false, want true")
			}
			if got != tt.want {
				t.Errorf("текст = %q, want %q", got, tt.want)
			}
		})
	}
}

// Четыре перехода и «значение не изменилось» — в том числе 0 и NULL как
// равнозначное «скидки нет».
func TestNotifyTextTransitions(t *testing.T) {
	tests := []struct {
		title      string
		prev, next *int16
		want       string
		ok         bool
	}{
		// было 0/NULL → стало >0: «поставить»
		{"нет → есть", nil, new(int16(20)), "Плов (до 22.06): Необходимо поставить скидку 20%", true},
		{"ноль → есть", new(int16(0)), new(int16(20)), "Плов (до 22.06): Необходимо поставить скидку 20%", true},

		// было >0 → стало больше: «поднять»
		{"10 → 20", new(int16(10)), new(int16(20)), "Плов (до 22.06): Необходимо поднять скидку до 20%", true},
		{"10 → 50", new(int16(10)), new(int16(50)), "Плов (до 22.06): Необходимо поднять скидку до 50%", true},

		// было >0 → стало меньше, но >0: «понизить»
		{"50 → 20", new(int16(50)), new(int16(20)), "Плов (до 22.06): Необходимо понизить скидку до 20%", true},
		{"30 → 10", new(int16(30)), new(int16(10)), "Плов (до 22.06): Необходимо понизить скидку до 10%", true},

		// было >0 → стало 0/NULL: «убрать»
		{"есть → нет", new(int16(20)), nil, "Плов (до 22.06): Необходимо убрать скидку", true},
		{"есть → ноль", new(int16(20)), new(int16(0)), "Плов (до 22.06): Необходимо убрать скидку", true},

		// значение не изменилось — уведомлять нечего
		{"нет → нет", nil, nil, "", false},
		{"нет → нет (ноль)", nil, new(int16(0)), "", false},
		{"ноль → ноль", new(int16(0)), new(int16(0)), "", false},
		{"20 → 20", new(int16(20)), new(int16(20)), "", false},
		{"50 → 50", new(int16(50)), new(int16(50)), "", false},

		// краевые: отрицательное значение из БД — тоже «нет»
		{"минус → нет", new(int16(-5)), new(int16(0)), "", false},
		{"нет → минус", nil, new(int16(-5)), "", false},
		{"минус → есть", new(int16(-5)), new(int16(20)), "Плов (до 22.06): Необходимо поставить скидку 20%", true},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got, ok := NotifyText("Плов", notifyDate(), tt.prev, tt.next)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("текст = %q, want %q", got, tt.want)
			}
		})
	}
}

// Пустое имя товара не паникует и не портит шаблон.
func TestNotifyTextEmptyName(t *testing.T) {
	got, ok := NotifyText("", notifyDate(), nil, new(int16(20)))
	if !ok {
		t.Fatal("ok=false, want true")
	}
	const want = " (до 22.06): Необходимо поставить скидку 20%"
	if got != want {
		t.Errorf("текст = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, " (до 22.06):") {
		t.Errorf("шаблон поехал: %q", got)
	}
}

// Изменения эффективной скидки уходят в общий канал текстами правила NotifyText:
// четыре перехода, а неизменившееся значение молчит.
func TestNotifyChangesSendsTransitions(t *testing.T) {
	common := &fakeCommonNotifier{}
	uc := NewUseCase(nil, nil, nil, common, nil, nil)

	uc.notifyChanges(context.Background(), []Change{
		{Name: "Творог", BestBefore: notifyDate(), Prev: nil, Next: new(int16(20))},
		{Name: "Плов", BestBefore: notifyDate(), Prev: new(int16(20)), Next: new(int16(30))},
		{Name: "Сыр", BestBefore: notifyDate(), Prev: new(int16(20)), Next: new(int16(10))},
		{Name: "Хлеб", BestBefore: notifyDate(), Prev: new(int16(20)), Next: nil},
		{Name: "Кефир", BestBefore: notifyDate(), Prev: new(int16(20)), Next: new(int16(20))},
	})

	want := []string{
		"Творог (до 22.06): Необходимо поставить скидку 20%",
		"Плов (до 22.06): Необходимо поднять скидку до 30%",
		"Сыр (до 22.06): Необходимо понизить скидку до 10%",
		"Хлеб (до 22.06): Необходимо убрать скидку",
	}
	if !reflect.DeepEqual(common.texts, want) {
		t.Errorf("уведомления %q, want %q", common.texts, want)
	}
	if len(common.tries) != 4 {
		t.Errorf("попыток отправки %d, want 4 (неизменившееся значение молчит)", len(common.tries))
	}
}

// Ошибка отправки пересчёт не роняет: остальные изменения всё равно уходят в
// канал (тексты только в логе).
func TestNotifyChangesNotifierError(t *testing.T) {
	common := &fakeCommonNotifier{err: errors.New("телеграм недоступен")}
	uc := NewUseCase(nil, nil, nil, common, nil, nil)

	uc.notifyChanges(context.Background(), []Change{
		{Name: "Творог", BestBefore: notifyDate(), Prev: nil, Next: new(int16(20))},
		{Name: "Плов", BestBefore: notifyDate(), Prev: nil, Next: new(int16(30))},
	})

	if len(common.texts) != 0 {
		t.Errorf("отправлено %q при ошибке канала", common.texts)
	}
	if len(common.tries) != 2 {
		t.Errorf("попыток отправки %d, want 2", len(common.tries))
	}
}

// Уведомитель не подключён (канал не сконфигурирован): изменение не роняет
// расчёт — текст уходит только в лог.
func TestNotifyChangesNilNotifier(_ *testing.T) {
	uc := NewUseCase(nil, nil, nil, nil, nil, nil)

	uc.notifyChanges(context.Background(), []Change{
		{Name: "Творог", BestBefore: notifyDate(), Prev: nil, Next: new(int16(20))},
	})
}

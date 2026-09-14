package usecase

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// pp — процент как указатель: удобно писать пары (было, стало) в таблицах.
func pp(v int16) *int16 { return &v }

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
		{"поставить", nil, pp(20), "Творог (до 22.06): Необходимо поставить скидку 20%"},
		{"поднять", pp(20), pp(30), "Творог (до 22.06): Необходимо поднять скидку до 30%"},
		{"понизить", pp(20), pp(10), "Творог (до 22.06): Необходимо понизить скидку до 10%"},
		{"убрать", pp(20), nil, "Творог (до 22.06): Необходимо убрать скидку"},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got, ok := NotifyText(name, notifyDate(), tt.prev, tt.next)
			if !ok {
				t.Fatalf("ok=false, want true")
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
		{"нет → есть", nil, pp(20), "Плов (до 22.06): Необходимо поставить скидку 20%", true},
		{"ноль → есть", pp(0), pp(20), "Плов (до 22.06): Необходимо поставить скидку 20%", true},

		// было >0 → стало больше: «поднять»
		{"10 → 20", pp(10), pp(20), "Плов (до 22.06): Необходимо поднять скидку до 20%", true},
		{"10 → 50", pp(10), pp(50), "Плов (до 22.06): Необходимо поднять скидку до 50%", true},

		// было >0 → стало меньше, но >0: «понизить»
		{"50 → 20", pp(50), pp(20), "Плов (до 22.06): Необходимо понизить скидку до 20%", true},
		{"30 → 10", pp(30), pp(10), "Плов (до 22.06): Необходимо понизить скидку до 10%", true},

		// было >0 → стало 0/NULL: «убрать»
		{"есть → нет", pp(20), nil, "Плов (до 22.06): Необходимо убрать скидку", true},
		{"есть → ноль", pp(20), pp(0), "Плов (до 22.06): Необходимо убрать скидку", true},

		// значение не изменилось — уведомлять нечего
		{"нет → нет", nil, nil, "", false},
		{"нет → нет (ноль)", nil, pp(0), "", false},
		{"ноль → ноль", pp(0), pp(0), "", false},
		{"20 → 20", pp(20), pp(20), "", false},
		{"50 → 50", pp(50), pp(50), "", false},

		// краевые: отрицательное значение из БД — тоже «нет»
		{"минус → нет", pp(-5), pp(0), "", false},
		{"нет → минус", nil, pp(-5), "", false},
		{"минус → есть", pp(-5), pp(20), "Плов (до 22.06): Необходимо поставить скидку 20%", true},
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
	got, ok := NotifyText("", notifyDate(), nil, pp(20))
	if !ok {
		t.Fatalf("ok=false, want true")
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
		{Name: "Творог", BestBefore: notifyDate(), Prev: nil, Next: pp(20)},
		{Name: "Плов", BestBefore: notifyDate(), Prev: pp(20), Next: pp(30)},
		{Name: "Сыр", BestBefore: notifyDate(), Prev: pp(20), Next: pp(10)},
		{Name: "Хлеб", BestBefore: notifyDate(), Prev: pp(20), Next: nil},
		{Name: "Кефир", BestBefore: notifyDate(), Prev: pp(20), Next: pp(20)},
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
		{Name: "Творог", BestBefore: notifyDate(), Prev: nil, Next: pp(20)},
		{Name: "Плов", BestBefore: notifyDate(), Prev: nil, Next: pp(30)},
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
func TestNotifyChangesNilNotifier(t *testing.T) {
	uc := NewUseCase(nil, nil, nil, nil, nil, nil)

	uc.notifyChanges(context.Background(), []Change{
		{Name: "Творог", BestBefore: notifyDate(), Prev: nil, Next: pp(20)},
	})
}

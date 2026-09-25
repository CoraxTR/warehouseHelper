package usecase

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"warehouseHelper/internal/domain"
)

// База сотрудников: пустые поля и простыня — ошибки ввода, лишние пробелы
// снимаются, дубли (решение владельца 25.09.2026) не отсекаются.
func TestAddEmployeeValidation(t *testing.T) {
	long := strings.Repeat("я", maxFieldLen+1)
	tests := []struct {
		title    string
		name     string
		position string
		wantErr  error
		wantPair *domain.Employee // nil — до репозитория не дошло
	}{
		{"пустое ФИО", "   ", "Кладовщик", ErrEmployeeNoName, nil},
		{"пустая должность", "Иванов Иван", "  ", ErrEmployeeNoPosition, nil},
		{"длинное ФИО", long, "Кладовщик", ErrEmployeeTooLong, nil},
		{"длинная должность", "Иванов Иван", long, ErrEmployeeTooLong, nil},
		{"пробелы сняты", "  Иванов Иван  ", " Кладовщик ", nil,
			&domain.Employee{FullName: "Иванов Иван", Position: "Кладовщик"}},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			uc, repo, _ := newUC()

			err := uc.AddEmployee(context.Background(), tt.name, tt.position)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ошибка %v, want %v", err, tt.wantErr)
			}
			if tt.wantPair == nil {
				if len(repo.addedEmp) != 0 {
					t.Fatalf("сотрудник записан при ошибке ввода: %+v", repo.addedEmp)
				}

				return
			}
			if len(repo.addedEmp) != 1 {
				t.Fatalf("записанных сотрудников %d, want 1", len(repo.addedEmp))
			}
			got := repo.addedEmp[0]
			if got.FullName != tt.wantPair.FullName || got.Position != tt.wantPair.Position {
				t.Errorf("пара %q / %q, want %q / %q", got.FullName, got.Position, tt.wantPair.FullName, tt.wantPair.Position)
			}
		})
	}
}

// Ошибка репозитория отдаётся наружу: страница покажет «попробуйте ещё раз».
func TestAddEmployeeRepoError(t *testing.T) {
	uc, repo, _ := newUC()
	repo.addEmpErr = errors.New("БД недоступна")

	if err := uc.AddEmployee(context.Background(), "Иванов Иван", "Кладовщик"); !errors.Is(err, repo.addEmpErr) {
		t.Errorf("ошибка %v, want %v", err, repo.addEmpErr)
	}
}

// Удаление: без id — ошибка ввода, с id — уходит в репозиторий.
func TestDeleteEmployee(t *testing.T) {
	uc, repo, _ := newUC()

	if err := uc.DeleteEmployee(context.Background(), 0); !errors.Is(err, ErrEmployeeBadID) {
		t.Errorf("нулевой id: ошибка %v, want %v", err, ErrEmployeeBadID)
	}
	if len(repo.deletedEmp) != 0 {
		t.Errorf("удаление без id дошло до репозитория: %v", repo.deletedEmp)
	}

	if err := uc.DeleteEmployee(context.Background(), 7); err != nil {
		t.Fatalf("удаление сотрудника: %v", err)
	}
	if want := []int64{7}; !reflect.DeepEqual(repo.deletedEmp, want) {
		t.Errorf("удаления %v, want %v", repo.deletedEmp, want)
	}
}

// Лента отдаётся странице как есть, с пределом модуля (свежие сверху — это
// сортировка репозитория).
func TestFeedUsesModuleLimit(t *testing.T) {
	uc, repo, _ := newUC()
	repo.feed = []domain.Task{{ID: 2, Text: "Вернуть на сайт: Творог"}, {ID: 1, Text: "Убрать с сайта: Творог"}}

	got, err := uc.Feed(context.Background())
	if err != nil {
		t.Fatalf("лента: %v", err)
	}
	if len(got) != 2 || got[0].ID != 2 {
		t.Errorf("лента %+v, want две задачи свежими сверху", got)
	}
	if repo.limit != feedLimit {
		t.Errorf("предел ленты %d, want %d", repo.limit, feedLimit)
	}
}

// Данные кнопок задачи: свой формат разбираем, чужой (жалобы) — нет, мусор не
// превращается в «задачу 0».
func TestParseCallbackData(t *testing.T) {
	tests := []struct {
		title string
		data  string
		want  Callback
		ok    bool
	}{
		{"отметка", "task_done:7", Callback{TaskID: 7}, true},
		{"сотрудник", "task_who:7:3", Callback{TaskID: 7, EmployeeID: 3}, true},
		{"чужая кнопка", "complaint_details:12", Callback{}, false},
		{"без id", "task_done:", Callback{}, false},
		{"нулевая задача", "task_done:0", Callback{}, false},
		{"не число", "task_done:семь", Callback{}, false},
		{"сотрудник без id", "task_who:7", Callback{}, false},
		{"нулевой сотрудник", "task_who:7:0", Callback{}, false},
		{"мусор", "", Callback{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got, ok := ParseCallbackData(tt.data)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("разбор %q = %+v, want %+v", tt.data, got, tt.want)
			}
		})
	}
}

// Данные кнопок собираются теми же правилами, что разбираются (шов не
// разъедется с диспетчером).
func TestCallbackDataRoundTrip(t *testing.T) {
	got, ok := ParseCallbackData(DoneCallbackData(15))
	if !ok || got.TaskID != 15 || got.EmployeeID != 0 {
		t.Errorf("отметка: %+v ok=%v, want задачу 15 без сотрудника", got, ok)
	}
	got, ok = ParseCallbackData(whoCallbackData(15, 4))
	if !ok || got.TaskID != 15 || got.EmployeeID != 4 {
		t.Errorf("сотрудник: %+v ok=%v, want задачу 15 и сотрудника 4", got, ok)
	}
}

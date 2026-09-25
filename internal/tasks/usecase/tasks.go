// Package usecase — сценарии модуля «Внутренние задачи»: уведомления общего
// канала превращаются в задачи с одноразовой кнопкой «✅ Отметить», отметка
// (кто выполнил) фиксируется в БД, лента задач и база сотрудников — для
// страницы модуля. Пакет — чистая логика: БД, телеграм и время приходят швами
// (время создания и отметки ставит БД — своих часов у модуля нет).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

const trackPkg = "tasks"

const (
	// doneButton — текст кнопки задачи в общем канале. Кнопка одноразовая:
	// после отметки она гаснет (сообщение остаётся историей).
	doneButton = "✅ Отметить"
	// feedLimit — сколько задач отдаёт лента страницы (свежие сверху).
	feedLimit = 200
	// employeesPerRow — кнопок сотрудников в строке клавиатуры «кто отметил».
	employeesPerRow = 2
	// maxFieldLen — предел длины ФИО/должности (защита от простыни в кнопке:
	// текст кнопки читает человек в телеграме).
	maxFieldLen = 100
	// momentLayout — формат момента отметки в сообщении и ленте.
	momentLayout = "02.01 15:04"
)

// Ошибки ввода базы сотрудников (слой HTTP переводит их в сообщения формы).
var (
	ErrEmployeeNoName     = errors.New("не указано ФИО")
	ErrEmployeeNoPosition = errors.New("не указана должность")
	ErrEmployeeTooLong    = errors.New("слишком длинное значение")
	ErrEmployeeBadID      = errors.New("сотрудник не выбран")
)

// UseCase — сценарии модуля «Внутренние задачи».
type UseCase struct {
	repo     Repository
	notifier Notifier
}

// NewUseCase собирает юзкейс: repo — данные модуля (postgres), notifier —
// телеграм-шов задач.
func NewUseCase(repo Repository, notifier Notifier) *UseCase {
	return &UseCase{repo: repo, notifier: notifier}
}

// Open — открыть задачу по уведомлению общего канала: строка в ленте и
// сообщение с одноразовой кнопкой «✅ Отметить».
//
// Строка создаётся ПЕРЕД отправкой (id нужен в callback_data кнопки), но
// невидимых задач в ленте не остаётся: отправка не удалась — строку
// откатываем и отдаём ошибку вызывающему (производитель уведомления логирует
// её, как раньше ошибку отправки); канал не подключён — строку тоже убираем,
// в ленте не должно быть задач, которых никто не видел в чате.
func (uc *UseCase) Open(ctx context.Context, kind domain.TaskKind, text string) error {
	done := metrics.Track(trackPkg, "Open")
	defer done()

	if uc.notifier == nil {
		slog.Info(fmt.Sprintf("tasks: задача (канал не подключён): %s", text))
		return nil
	}

	id, err := uc.repo.CreateTask(ctx, kind, text)
	if err != nil {
		return err
	}

	messageID, err := uc.notifier.SendTask(ctx, text, doneButton, DoneCallbackData(id))
	if err != nil {
		uc.rollback(ctx, id)
		return fmt.Errorf("сообщение-задача: %w", err)
	}
	if messageID == 0 {
		uc.rollback(ctx, id)
		slog.Info(fmt.Sprintf("tasks: задача не отправлена (канал не подключён): %s", text))
	}
	return nil
}

// rollback убирает строку задачи, сообщение по которой не ушло. Ошибка отката
// логируется: наружу отдаём исходную причину.
func (uc *UseCase) rollback(ctx context.Context, id int64) {
	if err := uc.repo.DeleteTask(ctx, id); err != nil {
		slog.Info(fmt.Sprintf("tasks: откат задачи %d: %v", id, err))
	}
}

// HandleDone — нажатие «✅ Отметить»: задача уже закрыта или её нет — ответ
// всплывающим окном; иначе в том же сообщении кнопка меняется на список
// сотрудников («кто отметил»). Отдельного сообщения-вопроса нет — вопрос
// виден там же, где сама задача, и чат не засоряется.
func (uc *UseCase) HandleDone(ctx context.Context, callbackQueryID string, chatID, messageID, taskID int64) error {
	done := metrics.Track(trackPkg, "HandleDone")
	defer done()

	task, err := uc.repo.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return uc.notifier.AnswerCallbackAlert(ctx, callbackQueryID, "Задача не найдена")
	}
	if task.DoneAt != nil {
		return uc.notifier.AnswerCallbackAlert(ctx, callbackQueryID,
			fmt.Sprintf("Уже отмечено: %s, %s", task.DoneBy, formatMoment(*task.DoneAt)))
	}

	employees, err := uc.repo.ListEmployees(ctx)
	if err != nil {
		return err
	}
	if len(employees) == 0 {
		return uc.notifier.AnswerCallbackAlert(ctx, callbackQueryID,
			"База сотрудников пуста: заполните её на странице «Внутренние задачи» → «Сотрудники»")
	}

	if err := uc.notifier.AnswerCallback(ctx, callbackQueryID); err != nil {
		slog.Info(fmt.Sprintf("tasks: ответ на нажатие задачи %d: %v", taskID, err))
	}
	if err := uc.notifier.EditKeyboard(ctx, chatID, messageID, employeeButtons(taskID, employees)); err != nil {
		return fmt.Errorf("кнопки сотрудников задачи %d: %w", taskID, err)
	}
	return nil
}

// HandleWho — выбран сотрудник: отметка записывается (однократно), кнопки
// гаснут, в сообщение дописывается «✅ Выполнено: ФИО, время».
func (uc *UseCase) HandleWho(ctx context.Context, callbackQueryID string, chatID, messageID, taskID, employeeID int64) error {
	done := metrics.Track(trackPkg, "HandleWho")
	defer done()

	task, err := uc.repo.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return uc.notifier.AnswerCallbackAlert(ctx, callbackQueryID, "Задача не найдена")
	}

	employee, err := uc.repo.GetEmployee(ctx, employeeID)
	if err != nil {
		return err
	}
	if employee == nil {
		return uc.notifier.AnswerCallbackAlert(ctx, callbackQueryID,
			"Сотрудник не найден в базе — обновите список и нажмите «✅» заново")
	}

	at, ok, err := uc.repo.MarkTaskDone(ctx, taskID, employee.ID, employee.FullName)
	if err != nil {
		return err
	}
	if !ok {
		return uc.notifier.AnswerCallbackAlert(ctx, callbackQueryID, "Задачу уже отметили")
	}
	slog.Info(fmt.Sprintf("tasks: задача %d отмечена: %s", taskID, employee.FullName))

	if err := uc.notifier.AnswerCallback(ctx, callbackQueryID); err != nil {
		slog.Info(fmt.Sprintf("tasks: ответ на нажатие задачи %d: %v", taskID, err))
	}
	return uc.notifier.EditText(ctx, chatID, messageID, markedText(task.Text, employee.FullName, at))
}

// Feed — лента задач для страницы «Внутренние задачи»: свежие сверху, с
// отметками «кто выполнил».
func (uc *UseCase) Feed(ctx context.Context) ([]domain.Task, error) {
	done := metrics.Track(trackPkg, "Feed")
	defer done()

	return uc.repo.ListTasks(ctx, feedLimit)
}

// Employees — база сотрудников (страница «Сотрудники»).
func (uc *UseCase) Employees(ctx context.Context) ([]domain.Employee, error) {
	done := metrics.Track(trackPkg, "Employees")
	defer done()

	return uc.repo.ListEmployees(ctx)
}

// AddEmployee добавляет пару ФИО - должность: пустое поле и простыня в кнопке —
// ошибки ввода. Дубли не ловим — однофамильцев и повторы базы владелец
// разбирает глазами (решение 25.09.2026).
func (uc *UseCase) AddEmployee(ctx context.Context, fullName, position string) error {
	done := metrics.Track(trackPkg, "AddEmployee")
	defer done()

	name := strings.TrimSpace(fullName)
	pos := strings.TrimSpace(position)
	switch {
	case name == "":
		return ErrEmployeeNoName
	case pos == "":
		return ErrEmployeeNoPosition
	case len([]rune(name)) > maxFieldLen || len([]rune(pos)) > maxFieldLen:
		return ErrEmployeeTooLong
	}

	if _, err := uc.repo.AddEmployee(ctx, name, pos); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("tasks: сотрудник добавлен: %s (%s)", name, pos))
	return nil
}

// DeleteEmployee убирает сотрудника из базы: в кнопках отметки он больше не
// появится, история уже закрытых задач сохраняется (ФИО лежит снимком).
func (uc *UseCase) DeleteEmployee(ctx context.Context, id int64) error {
	done := metrics.Track(trackPkg, "DeleteEmployee")
	defer done()

	if id <= 0 {
		return ErrEmployeeBadID
	}
	return uc.repo.DeleteEmployee(ctx, id)
}

// employeeButtons — клавиатура «кто отметил»: по кнопке на сотрудника
// (employeesPerRow в строке); данные кнопки — задача и сотрудник.
func employeeButtons(taskID int64, employees []domain.Employee) [][]domain.TGButton {
	rows := make([][]domain.TGButton, 0, (len(employees)+employeesPerRow-1)/employeesPerRow)
	for i := 0; i < len(employees); i += employeesPerRow {
		row := make([]domain.TGButton, 0, employeesPerRow)
		for _, e := range employees[i:min(i+employeesPerRow, len(employees))] {
			row = append(row, domain.TGButton{Text: e.FullName, CallbackData: whoCallbackData(taskID, e.ID)})
		}
		rows = append(rows, row)
	}
	return rows
}

// markedText — текст задачи с отметкой выполнения. Сообщение в чате остаётся
// историей: видно и что надо было сделать, и кто это закрыл.
func markedText(taskText, employeeName string, at time.Time) string {
	return fmt.Sprintf("%s\n✅ Выполнено: %s, %s", taskText, employeeName, formatMoment(at))
}

// loc — часовой пояс машины (склад, МСК): время отметки в сообщении
// показываем по местным часам, как страницы (procLoc), а не в UTC базы.
// Часов у модуля нет: время приходит из БД, здесь только его показ.
var loc = time.Now().Location()

// formatMoment — местное время отметки (часы склада).
func formatMoment(t time.Time) string {
	return t.In(loc).Format(momentLayout)
}

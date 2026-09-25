// Швы юзкейса модуля «Внутренние задачи»: что сценариям нужно от внешнего
// мира. Реализации — repository/postgres (данные модуля) и telegram.Notifier
// (сообщение-задача с кнопкой), связка — di.go. Своих часов модуль не заводит:
// время создания и отметки ставит БД.
package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/domain"
)

// Repository — данные модуля: задачи и база сотрудников (владелец записи —
// этот же модуль, чужие таблицы не читает).
type Repository interface {
	// CreateTask создаёт задачу и возвращает её id (уходит в callback_data
	// кнопки «✅ Отметить»); текст — как он ушёл в чат.
	CreateTask(ctx context.Context, kind domain.TaskKind, text string) (int64, error)
	// DeleteTask удаляет задачу — откат неудачной отправки сообщения.
	DeleteTask(ctx context.Context, id int64) error
	// GetTask возвращает задачу; задачи нет — (nil, nil).
	GetTask(ctx context.Context, id int64) (*domain.Task, error)
	// ListTasks — лента задач, свежие сверху.
	ListTasks(ctx context.Context, limit int) ([]domain.Task, error)
	// MarkTaskDone отмечает задачу выполненной (время ставит БД) и
	// возвращает это время; ok=false — задача уже отмечена (однократность
	// держит условие done_at IS NULL в SQL).
	MarkTaskDone(ctx context.Context, id, employeeID int64, employeeName string) (time.Time, bool, error)

	// ListEmployees — база сотрудников для кнопок отметки.
	ListEmployees(ctx context.Context) ([]domain.Employee, error)
	// GetEmployee — сотрудник по id; сотрудника нет — (nil, nil).
	GetEmployee(ctx context.Context, id int64) (*domain.Employee, error)
	// AddEmployee — добавить пару ФИО - должность.
	AddEmployee(ctx context.Context, fullName, position string) (int64, error)
	// DeleteEmployee — убрать сотрудника; отметки задач остаются (ФИО в
	// задачах лежит снимком).
	DeleteEmployee(ctx context.Context, id int64) error
}

// Notifier — телеграм-шов задач (реализация: telegram.Notifier): отправка
// сообщения-задачи в общий канал, правка кнопок и текста отправленного
// сообщения (шаг «кто отметил») и ответ на нажатие.
type Notifier interface {
	// SendTask шлёт в общий канал текст с одной inline-кнопкой и возвращает
	// message_id отправленного сообщения; 0 — канал не подключён (пустой
	// токен или chat_id) — задача тогда не создаётся.
	SendTask(ctx context.Context, text, buttonText, callbackData string) (messageID int64, err error)
	// EditKeyboard заменяет inline-кнопки сообщения (пустой rows — снять).
	EditKeyboard(ctx context.Context, chatID, messageID int64, rows [][]domain.TGButton) error
	// EditText заменяет текст сообщения и снимает inline-кнопки.
	EditText(ctx context.Context, chatID, messageID int64, text string) error
	// AnswerCallback закрывает «часики» на кнопке.
	AnswerCallback(ctx context.Context, callbackQueryID string) error
	// AnswerCallbackAlert закрывает нажатие всплывающим окном с текстом
	// (повторная отметка, пустая база сотрудников).
	AnswerCallbackAlert(ctx context.Context, callbackQueryID, alert string) error
}

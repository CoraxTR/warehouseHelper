package http

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/tasks/usecase"
)

// Модуль «Внутренние задачи»: страница ленты задач (кто выполнил) и страница
// базы сотрудников. Шаблоны парсятся один раз при старте (правило проекта:
// не парсить шаблон на каждый запрос).

var (
	tasksFeedTmpl      = template.Must(template.ParseFiles("../internal/delivery/web/templates/tasks.html", "../internal/delivery/web/templates/_nav.html"))
	tasksEmployeesTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/tasks_employees.html", "../internal/delivery/web/templates/_nav.html"))
)

// ---- Данные страниц ----

// taskRow — строка ленты задач: текст уведомления и отметка «кто выполнил».
type taskRow struct {
	ID        int64
	Text      string
	CreatedAt string
	Done      bool
	DoneBy    string
	DoneAt    string
}

// tasksFeedData — страница «Внутренние задачи».
type tasksFeedData struct {
	Title string
	Msg   string
	Rows  []taskRow
}

// employeeRow — строка базы сотрудников.
type employeeRow struct {
	ID       int64
	FullName string
	Position string
}

// tasksEmployeesData — страница «Сотрудники»: список пар ФИО - должность и
// форма добавления (при ошибке форма показывает введённое).
type tasksEmployeesData struct {
	Rows     []employeeRow
	Msg      string
	Error    string
	FullName string
	Position string
}

// ---- Преобразования ----

// taskRowFromTask собирает строку ленты: время уведомления, текст задачи и
// отметку выполнения (пусто — задача ещё не закрыта).
func taskRowFromTask(t domain.Task) taskRow {
	row := taskRow{
		ID:        t.ID,
		Text:      t.Text,
		CreatedAt: t.CreatedAt.In(procLoc).Format("02.01 15:04"),
	}
	if t.DoneAt != nil {
		row.Done = true
		row.DoneBy = t.DoneBy
		row.DoneAt = t.DoneAt.In(procLoc).Format("02.01 15:04")
	}
	return row
}

// ---- Страницы ----

// TasksPage — «Внутренние задачи» (GET): лента задач с отметками.
func (h *Handler) TasksPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	list, err := h.tasksUC.Feed(r.Context())
	if err != nil {
		slog.Info(fmt.Sprintf("внутренние задачи: лента: %v", err))
		http.Error(w, "Не удалось загрузить задачи.", http.StatusInternalServerError)
		return
	}

	data := tasksFeedData{Title: "Внутренние задачи", Msg: r.URL.Query().Get("msg")}
	for _, t := range list {
		data.Rows = append(data.Rows, taskRowFromTask(t))
	}

	if err := tasksFeedTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("внутренние задачи: рендер ленты: %v", err))
	}
}

// TasksEmployees — «Сотрудники»: GET — список и форма добавления, POST —
// добавление или удаление пары ФИО - должность. После успешного POST —
// редирект на GET с сообщением (PRG: F5 не повторяет POST).
func (h *Handler) TasksEmployees(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.renderTasksEmployees(w, r, tasksEmployeesData{Msg: r.URL.Query().Get("msg")})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Не удалось прочитать форму.", http.StatusBadRequest)
			return
		}
		h.tasksEmployeesPost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// tasksEmployeesPost разбирает действие формы: добавить сотрудника или убрать
// его из базы.
func (h *Handler) tasksEmployeesPost(w http.ResponseWriter, r *http.Request) {
	switch r.FormValue("action") {
	case "add":
		name := r.FormValue("full_name")
		position := r.FormValue("position")
		if err := h.tasksUC.AddEmployee(r.Context(), name, position); err != nil {
			h.renderTasksEmployees(w, r, tasksEmployeesData{
				Error:    employeeFormError(err),
				FullName: strings.TrimSpace(name),
				Position: strings.TrimSpace(position),
			})
			return
		}
		h.redirectTasksEmployees(w, r, "Сотрудник добавлен")
	case "delete":
		id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if err != nil || id <= 0 {
			h.renderTasksEmployees(w, r, tasksEmployeesData{Error: "Не удалось определить сотрудника."})
			return
		}
		if err := h.tasksUC.DeleteEmployee(r.Context(), id); err != nil {
			slog.Info(fmt.Sprintf("внутренние задачи: удаление сотрудника %d: %v", id, err))
			h.renderTasksEmployees(w, r, tasksEmployeesData{Error: "Не удалось удалить сотрудника. Попробуйте ещё раз."})
			return
		}
		h.redirectTasksEmployees(w, r, "Сотрудник удалён")
	default:
		h.renderTasksEmployees(w, r, tasksEmployeesData{Error: "Неизвестное действие."})
	}
}

// redirectTasksEmployees возвращает страницу базы сотрудников с сообщением
// (PRG: GET-хендлер читает msg из query).
func (h *Handler) redirectTasksEmployees(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/tasks/employees?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// renderTasksEmployees отдаёт страницу базы сотрудников: список плюс переданное
// сообщение или ошибку формы.
func (h *Handler) renderTasksEmployees(w http.ResponseWriter, r *http.Request, data tasksEmployeesData) {
	list, err := h.tasksUC.Employees(r.Context())
	if err != nil {
		slog.Info(fmt.Sprintf("внутренние задачи: база сотрудников: %v", err))
		http.Error(w, "Не удалось загрузить сотрудников.", http.StatusInternalServerError)
		return
	}
	for _, e := range list {
		data.Rows = append(data.Rows, employeeRow{ID: e.ID, FullName: e.FullName, Position: e.Position})
	}

	if err := tasksEmployeesTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("внутренние задачи: рендер сотрудников: %v", err))
	}
}

// employeeFormError — сообщение формы по ошибке валидации юзкейса. Прочие
// ошибки (БД) — общее сообщение, детали только в логе.
func employeeFormError(err error) string {
	switch {
	case errors.Is(err, usecase.ErrEmployeeNoName):
		return "Укажите ФИО."
	case errors.Is(err, usecase.ErrEmployeeNoPosition):
		return "Укажите должность."
	case errors.Is(err, usecase.ErrEmployeeTooLong):
		return "ФИО и должность — не длиннее 100 символов."
	default:
		return "Не удалось добавить сотрудника. Попробуйте ещё раз."
	}
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"warehouseHelper/internal/domain"
)

// Модуль «Внутренние задачи»: методы на общем PGClient с префиксом Task
// (сотрудники — Employee). Своих часов модуль не заводит: created_at/done_at
// ставит БД (now()), поэтому ни CreateTask, ни MarkTaskDone времени не
// принимают.

// taskColumns — колонки tasks в порядке SELECT/Scan.
const taskColumns = `id, kind, text, created_at, done_at, done_by`

// scanTaskRow сканирует строку tasks в domain.Task. done_at/done_by —
// NULL-колонки («не отмечено»): pgx не кладёт NULL в string, поэтому
// через *string + textValue (как в остальных репозиториях слоя).
func scanTaskRow(row pgx.Row) (*domain.Task, error) {
	var (
		t      domain.Task
		doneBy *string
	)
	if err := row.Scan(&t.ID, (*string)(&t.Kind), &t.Text, &t.CreatedAt, &t.DoneAt, &doneBy); err != nil {
		return nil, err
	}
	t.DoneBy = textValue(doneBy)
	return &t, nil
}

// CreateTask создаёт задачу по уведомлению общего канала и возвращает её id —
// он уходит в callback_data кнопки «✅ Отметить». Строка создаётся ДО отправки
// сообщения: id нужен кнопке; отправка не удалась — вызывающий (usecase)
// удаляет задачу, чтобы в ленте не осталось невидимой строки.
func (pg *PGClient) CreateTask(ctx context.Context, kind domain.TaskKind, text string) (int64, error) {
	var id int64
	if err := pg.Pool.QueryRow(ctx,
		`INSERT INTO tasks (kind, text) VALUES ($1, $2) RETURNING id`,
		string(kind), text,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert task: %w", err)
	}
	return id, nil
}

// DeleteTask удаляет задачу (откат неудачной отправки и чистка вручную).
// Отсутствие задачи не ошибка.
func (pg *PGClient) DeleteTask(ctx context.Context, id int64) error {
	if _, err := pg.Pool.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	return nil
}

// GetTask возвращает задачу по id; задачи нет — (nil, nil), как у остальных
// «спросить одну строку» в слое.
func (pg *PGClient) GetTask(ctx context.Context, id int64) (*domain.Task, error) {
	t, err := scanTaskRow(pg.Pool.QueryRow(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		//nolint:nilnil // контракт порта модуля: нет строки — (nil, nil), как у GetSupplierBarcode
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query task %d: %w", id, err)
	}
	return t, nil
}

// ListTasks — лента задач, свежие сверху (limit — размер ленты).
func (pg *PGClient) ListTasks(ctx context.Context, limit int) ([]domain.Task, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT `+taskColumns+` FROM tasks ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()

	list := make([]domain.Task, 0)
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		list = append(list, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return list, nil
}

// MarkTaskDone отмечает задачу выполненной и возвращает время отметки (его
// ставит БД). ok=false — задача уже отмечена (или её нет): условие done_at IS
// NULL делает отметку однократной даже при двух одновременных нажатиях.
// employeeName — снимок ФИО: сотрудника могут удалить, история остаётся.
func (pg *PGClient) MarkTaskDone(ctx context.Context, id, employeeID int64, employeeName string) (time.Time, bool, error) {
	var at time.Time
	err := pg.Pool.QueryRow(ctx,
		`UPDATE tasks SET done_at = now(), employee_id = $2, done_by = $3
		 WHERE id = $1 AND done_at IS NULL
		 RETURNING done_at`,
		id, employeeID, employeeName,
	).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mark task %d done: %w", id, err)
	}
	return at, true, nil
}

// ListEmployees возвращает базу сотрудников (порядок — по ФИО).
func (pg *PGClient) ListEmployees(ctx context.Context) ([]domain.Employee, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT id, full_name, position FROM employees ORDER BY full_name, id`)
	if err != nil {
		return nil, fmt.Errorf("query employees: %w", err)
	}
	defer rows.Close()

	list := make([]domain.Employee, 0)
	for rows.Next() {
		var e domain.Employee
		if err := rows.Scan(&e.ID, &e.FullName, &e.Position); err != nil {
			return nil, fmt.Errorf("scan employee: %w", err)
		}
		list = append(list, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate employees: %w", err)
	}
	return list, nil
}

// GetEmployee возвращает сотрудника по id; сотрудника нет — (nil, nil).
func (pg *PGClient) GetEmployee(ctx context.Context, id int64) (*domain.Employee, error) {
	var e domain.Employee
	err := pg.Pool.QueryRow(ctx,
		`SELECT id, full_name, position FROM employees WHERE id = $1`, id).
		Scan(&e.ID, &e.FullName, &e.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		//nolint:nilnil // контракт порта модуля: нет строки — (nil, nil), как у GetSupplierBarcode
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query employee %d: %w", id, err)
	}
	return &e, nil
}

// AddEmployee добавляет сотрудника в базу и возвращает его id.
func (pg *PGClient) AddEmployee(ctx context.Context, fullName, position string) (int64, error) {
	var id int64
	if err := pg.Pool.QueryRow(ctx,
		`INSERT INTO employees (full_name, position) VALUES ($1, $2) RETURNING id`,
		fullName, position,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert employee: %w", err)
	}
	return id, nil
}

// DeleteEmployee удаляет сотрудника из базы; отметки задач остаются (ссылка
// tasks.employee_id обнуляется, ФИО лежит снимком в done_by).
func (pg *PGClient) DeleteEmployee(ctx context.Context, id int64) error {
	if _, err := pg.Pool.Exec(ctx, `DELETE FROM employees WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete employee %d: %w", id, err)
	}
	return nil
}

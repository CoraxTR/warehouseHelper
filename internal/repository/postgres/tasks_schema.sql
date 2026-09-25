-- Модуль «Внутренние задачи»: уведомления общего канала, которые сотрудник
-- закрывает кнопкой «✅» в телеграме, и база сотрудников для вопроса
-- «кто отметил». Владелец записи обоих таблиц — internal/tasks.

DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS employees;

-- Сотрудники: пара ФИО - должность, заполняет владелец через страницу
-- «Внутренние задачи» → «Сотрудники». ФИО идёт текстом кнопки отметки.
CREATE TABLE employees (
    id        BIGSERIAL PRIMARY KEY,
    full_name TEXT NOT NULL,
    position  TEXT NOT NULL
);

-- Задачи: по строке на уведомление (текст как ушёл в чат). created_at/done_at
-- ставит БД (now()), своих часов у модуля нет. done_by — снимок ФИО на момент
-- отметки: сотрудника из базы могут удалить, история отметок должна остаться.
-- Ссылка на сотрудника логическая (ON DELETE SET NULL), поэтому удаление
-- сотрудника не ломает ни одной отметки.
CREATE TABLE tasks (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN (
                    'stock_out', 'stock_in',
                    'discount_put', 'discount_raise', 'discount_lower', 'discount_remove')),
    text        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at     TIMESTAMPTZ,
    employee_id BIGINT REFERENCES employees(id) ON DELETE SET NULL,
    done_by     TEXT
);

-- Лента читается «свежие сверху».
CREATE INDEX tasks_created_at_idx ON tasks (created_at DESC);

-- Модуль «Внутренние задачи»: живая база. Файл tasks_schema.sql — DROP+CREATE
-- (для чистой базы), здесь — миграция существующей: CREATE IF NOT EXISTS,
-- данные не трогаются. Применяет владелец вместе с выкатом фичи.

CREATE TABLE IF NOT EXISTS employees (
    id        BIGSERIAL PRIMARY KEY,
    full_name TEXT NOT NULL,
    position  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
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

CREATE INDEX IF NOT EXISTS tasks_created_at_idx ON tasks (created_at DESC);

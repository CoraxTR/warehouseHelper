-- Миграция таблицы order_picking (журнал подбора заказов) для ЖИВОЙ БД.
-- Если таблицы на проде нет — применять order_picking_schema.sql; файл DROP+CREATE
-- на живую таблицу не мигрирует (снёс бы журнал). Применяет ВЛАДЕЛЕЦ:
--   psql -f order_picking_journal_migration.sql
-- Идемпотентна: ADD COLUMN / CREATE INDEX с IF NOT EXISTS — повторный запуск ничего
-- не меняет.
--
-- Что делает: старую таблицу (id, order_id, product_id, product_name, weight,
-- produced_on, best_before) приводит к составу order_picking_schema.sql — добавляет
-- position_id, internal_code, weighted, touched_at, новые индексы и пересоздаёт
-- представление агрегации под новый состав колонок.

-- Новые колонки. Добавляем с DEFAULT и NOT NULL — так проходит на таблице с любым
-- числом строк (ADD COLUMN ... NOT NULL без DEFAULT на непустой таблице падает).
ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS position_id   TEXT    NOT NULL DEFAULT '';
ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS internal_code TEXT    NOT NULL DEFAULT '';
ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS weighted      BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE order_picking ADD COLUMN IF NOT EXISTS touched_at    DATE    NOT NULL DEFAULT CURRENT_DATE;

-- DEFAULT у position_id/internal_code/weighted нужен был только для самих ADD COLUMN:
-- значения этих колонок обязан задавать каждый INSERT. Оставленный DEFAULT '' молча
-- записал бы единицу без позиции, а DEFAULT false — весовой кусок штучным. touched_at
-- DEFAULT сохраняет: он есть и в схеме (дата последней записи, по ней ретеншен).
ALTER TABLE order_picking ALTER COLUMN position_id   DROP DEFAULT;
ALTER TABLE order_picking ALTER COLUMN internal_code DROP DEFAULT;
ALTER TABLE order_picking ALTER COLUMN weighted      DROP DEFAULT;

-- Индексы нового состава. order_picking_order_idx и order_picking_product_idx уже
-- есть со старой таблицей — IF NOT EXISTS их не трогает.
CREATE INDEX IF NOT EXISTS order_picking_order_idx          ON order_picking (order_id);
CREATE INDEX IF NOT EXISTS order_picking_order_position_idx ON order_picking (order_id, position_id);
CREATE INDEX IF NOT EXISTS order_picking_touched_idx        ON order_picking (touched_at);
CREATE INDEX IF NOT EXISTS order_picking_product_idx        ON order_picking (product_id, best_before);

-- Старое представление агрегации снимаем и создаём заново: CREATE OR REPLACE VIEW
-- не может ни добавить колонку в середину, ни переименовать существующую. Новый
-- состав группирует по internal_code (код склада уникален и NOT NULL): по product_id
-- группировать нельзя — у товара, удалённого из каталога, product_id = NULL.
DROP VIEW IF EXISTS order_picking_aggregated;

CREATE VIEW order_picking_aggregated AS
SELECT order_id,
       internal_code,
       best_before,
       MAX(product_id)   AS product_id,
       MAX(product_name) AS product_name,
       COUNT(*)          AS units,
       SUM(weight)       AS total_weight
FROM order_picking
GROUP BY order_id, internal_code, best_before;

-- Контроль после применения (строк старого формата остаться не должно — журнал
-- писался до появления кода, обычно таблица пуста):
-- SELECT count(*) FROM order_picking WHERE position_id = '' OR internal_code = '';

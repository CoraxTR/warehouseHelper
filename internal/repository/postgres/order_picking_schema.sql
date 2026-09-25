-- Журнал подбора товаров в заказы (владелец таблицы — модуль подбора заказов).
-- Строка = одна подобранная единица товара: кусок для весового товара, штука для штучного.
-- Заказы здесь не хранятся — МС является ERP и источником заказов; в журнале только
-- order_id (UUID МС), из которого при необходимости строится ссылка на заказ.
-- Журнал хранит ФИНАЛЬНУЮ версию подбора: переподбор заменяет строки позиции,
-- добор дозаписывает, возврат в «Сроки» убирает строки, расформирование чистит заказ.
-- Агрегация по паре (товар, срок годности) — представление в конце файла: это
-- производное от журнала, дублировать отдельной таблицей не нужно.
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f order_picking_schema.sql (или DataGrip).
-- Живая БД: DROP+CREATE НЕ мигрирует (журнал сносится) — для существующей таблицы
-- применять order_picking_journal_migration.sql.

-- Представление зависит от таблицы: снимаем его первым, иначе DROP TABLE упрётся
-- в зависимость (при повторном применении файла таблица уже была бы «нужна» view).
DROP VIEW IF EXISTS order_picking_aggregated;
DROP TABLE IF EXISTS order_picking;

CREATE TABLE order_picking (
    id            BIGSERIAL PRIMARY KEY,             -- порядок записи
    order_id      TEXT NOT NULL,                     -- uuid заказа МС; не FK (заказы живут в МС)
    position_id   TEXT NOT NULL,                     -- id позиции МС: замену журнала делаем по позиции, а не по товару
    internal_code TEXT NOT NULL,                     -- код склада (8 цифр) — сверка возвратов в «Сроки»
    product_id    TEXT REFERENCES products(id) ON DELETE SET NULL,  -- SET NULL: журнал переживает удаление товара из каталога
    product_name  TEXT NOT NULL,                     -- снимок наименования на момент подбора
    weighted      BOOLEAN NOT NULL,                  -- весовой (weight — кг куска) или штучный (weight = 1)
    weight        NUMERIC(12, 4) NOT NULL,           -- вес единицы: весовые — кг, штучные — 1
    produced_on   DATE,                              -- дата выработки; NULL — не известна
    best_before   DATE NOT NULL,                     -- срок годности (годен до)
    touched_at    DATE NOT NULL DEFAULT CURRENT_DATE -- дата последней записи: по ней ретеншен (номер заказа повторяется каждый год)
);

CREATE INDEX order_picking_order_idx          ON order_picking (order_id);
CREATE INDEX order_picking_order_position_idx ON order_picking (order_id, position_id);
CREATE INDEX order_picking_touched_idx        ON order_picking (touched_at);
CREATE INDEX order_picking_product_idx        ON order_picking (product_id, best_before);

-- Агрегация по (заказ, товар, срок годности): сколько единиц и суммарный вес
-- подобранных единиц с одинаковым сроком в рамках заказа. Товар задаёт internal_code
-- (код склада уникален и NOT NULL): по product_id группировать нельзя — у товара,
-- удалённого из каталога, product_id обнуляется (SET NULL) и разные товары слились бы
-- в одну строку. product_id и product_name — MAX, чтобы строка несла и uuid, и название.
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

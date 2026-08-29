-- Журнал подбора товаров в заказы (владелец — модуль подбора заказов).
-- Одна строка = одна подобранная единица товара: кусок для весовых, штука для штучных.
-- Заказы здесь не хранятся — МС является ERP и источником заказов; в журнале только
-- order_id (UUID МС), из которого при необходимости строится ссылка на заказ.
-- Агрегация по паре (товар, срок годности) — представление в конце файла: это
-- производное от журнала, дублировать отдельной таблицей не нужно.
-- Зависит от products_schema.sql (FK products.id).

DROP TABLE IF EXISTS order_picking;

CREATE TABLE order_picking (
    id           BIGSERIAL PRIMARY KEY,             -- порядок записи
    order_id     TEXT NOT NULL,                     -- UUID заказа в МС; не FK (заказы живут в МС)
    product_id   TEXT REFERENCES products(id) ON DELETE SET NULL,  -- SET NULL: журнал переживает удаление товара из каталога
    product_name TEXT NOT NULL,                     -- наименование на момент подбора (снимок: каталог может переименовать/удалить)
    weight       NUMERIC(12, 4) NOT NULL,           -- вес подобранной единицы: весовые — кг, штучные — 1 (единица)
    produced_on  DATE,                              -- дата выработки; NULL — не известна
    best_before  DATE NOT NULL                      -- срок годности (годен до)
);

CREATE INDEX order_picking_order_idx ON order_picking (order_id);
CREATE INDEX order_picking_product_idx ON order_picking (product_id, best_before);

-- Агрегация по (заказ, товар, срок годности): сколько единиц и суммарный вес
-- подобранных единиц с одинаковым сроком в рамках заказа.
CREATE VIEW order_picking_aggregated AS
SELECT order_id,
       product_id,
       MAX(product_name) AS product_name,
       best_before,
       COUNT(*)          AS units,
       SUM(weight)       AS total_weight
FROM order_picking
GROUP BY order_id, product_id, best_before;

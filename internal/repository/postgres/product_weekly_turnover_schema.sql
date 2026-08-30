-- Оборот по неделям: продано за неделю в ШТУКАХ (весовые: (кг продаж − кг возвратов) /
-- средний вес штуки из products.average_weight; штучные — количество как есть).
-- Учитываются только товары с track_weekly = true.
-- Неделя считается с понедельника. Возврат «задним числом» даёт отрицательный
-- оборот периода — qty хранится честно, без CHECK (qty >= 0).
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_weekly_turnover_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_weekly_turnover;

CREATE TABLE product_weekly_turnover (
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    week_start DATE NOT NULL,                          -- понедельник недели
    qty        NUMERIC(12, 3) NOT NULL,                -- продажи за неделю, штуки
    PRIMARY KEY (product_id, week_start)
);

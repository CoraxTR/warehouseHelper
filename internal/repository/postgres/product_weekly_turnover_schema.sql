-- Оборот по неделям: продано за неделю в единицах измерения товара (uom):
-- штучный товар — в штуках, весовой — в кг; учитываются только товары с track_weekly = true.
-- Неделя считается с понедельника.
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_weekly_turnover_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_weekly_turnover;

CREATE TABLE product_weekly_turnover (
    product_id TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    week_start DATE NOT NULL,                          -- понедельник недели
    qty        NUMERIC(12, 3) NOT NULL CHECK (qty >= 0),  -- оборот в единицах товара (uom)
    PRIMARY KEY (product_id, week_start)
);

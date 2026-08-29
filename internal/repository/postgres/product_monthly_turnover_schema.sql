-- Оборот по месяцам: продано за месяц в единицах измерения товара (uom); для всех товаров.
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_monthly_turnover_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_monthly_turnover;

CREATE TABLE product_monthly_turnover (
    product_id  TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    month_start DATE NOT NULL,                         -- первое число месяца
    qty         NUMERIC(12, 3) NOT NULL CHECK (qty >= 0),  -- оборот в единицах товара (uom)
    PRIMARY KEY (product_id, month_start)
);

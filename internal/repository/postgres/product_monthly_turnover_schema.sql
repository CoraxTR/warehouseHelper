-- Оборот по месяцам: продано за месяц в ШТУКАХ (весовые: (кг продаж − кг возвратов) /
-- средний вес штуки из products.average_weight; штучные — количество как есть).
-- Для всех товаров. Возврат «задним числом» даёт отрицательный оборот периода —
-- qty хранится честно, без CHECK (qty >= 0).
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_monthly_turnover_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_monthly_turnover;

CREATE TABLE product_monthly_turnover (
    product_id  TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    month_start DATE NOT NULL,                         -- первое число месяца
    qty         NUMERIC(12, 3) NOT NULL,               -- продажи за месяц, штуки
    PRIMARY KEY (product_id, month_start)
);

-- Актуальные цены товаров у поставщиков: одна строка = пара (товар, поставщик).
-- Строка существует, только когда цена известна (NULL-строки не создаём).
-- Цена — в копейках (конвенция проекта, как в РефГо).
-- special_schedule = true — товар у этого поставщика заказывается по спец. расписанию
-- поставщика (special_* колонки в suppliers), false — по общему.
-- Зависит от products_schema.sql и suppliers_schema.sql.
-- Применить до первого запуска: psql -f product_supplier_prices_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_supplier_prices;

CREATE TABLE product_supplier_prices (
    supplier_id      TEXT NOT NULL REFERENCES suppliers(id) ON DELETE CASCADE,
    product_id       TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    current_price    BIGINT NOT NULL CHECK (current_price >= 0),  -- актуальная цена, копейки
    special_schedule BOOLEAN NOT NULL DEFAULT false,  -- заказ по спец. расписанию поставщика
    PRIMARY KEY (supplier_id, product_id)
);

CREATE INDEX product_supplier_prices_product_idx ON product_supplier_prices (product_id);

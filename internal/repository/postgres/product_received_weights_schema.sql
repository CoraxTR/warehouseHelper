-- Веса принятых единиц товара: сырые значения, которые пишет модуль приёмки.
-- Порядок FIFO — по возрастанию id; хранятся последние 100 значений на товар.
-- После каждой приёмки одной транзакцией: INSERT веса → DELETE строк за пределами
-- последних 100 (ORDER BY id DESC OFFSET 100) → SELECT AVG(weight) → UPDATE
-- products.average_weight. Лимит 100 — настройка приложения, не зашита в схему.
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_received_weights_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_received_weights;

CREATE TABLE product_received_weights (
    id          BIGSERIAL PRIMARY KEY,            -- порядок FIFO (растёт с каждой записью)
    product_id  TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    weight      NUMERIC(12, 4) NOT NULL CHECK (weight > 0)  -- вес принятой единицы, кг
);

CREATE INDEX product_received_weights_product_idx ON product_received_weights (product_id, id);

-- Веса принятых единиц товара: сырые значения, которые пишет модуль приёмки.
-- Вес хранится в целых граммах (из штрих-кодов вес приходит в граммах, конверсия
-- во float — только в модуле среднего веса, когда он появится).
-- Порядок FIFO — по возрастанию id; хранятся последние 100 значений на товар.
-- После каждой приёмки одной транзакцией: INSERT веса → DELETE строк за пределами
-- последних 100 (ORDER BY id DESC OFFSET 100). Лимит 100 — настройка приложения,
-- не зашита в схему. Средний вес считает будущий модуль среднего веса (читает
-- эти строки, пишет products.average_weight через каталог).
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_received_weights_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_received_weights;

CREATE TABLE product_received_weights (
    id          BIGSERIAL PRIMARY KEY,            -- порядок FIFO (растёт с каждой записью)
    product_id  TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    weight      INTEGER NOT NULL CHECK (weight > 0)  -- вес принятой единицы, граммы
);

CREATE INDEX product_received_weights_product_idx ON product_received_weights (product_id, id);

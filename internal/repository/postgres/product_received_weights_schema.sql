-- Веса принятых единиц товара: сырые значения, которые пишет модуль среднего
-- веса (internal/avgweight). Приёмка передаёт единичные веса кусков по позициям,
-- модуль записывает их поштучно и обрезает историю до лимита на товар.
-- Вес хранится в целых граммах (из штрих-кодов вес приходит в граммах; конверсия
-- во float — в модуле среднего веса).
-- Порядок FIFO — по возрастанию id; хранятся последние N значений на товар.
-- Поток: INSERT веса → DELETE строк за пределами последних N (ORDER BY id DESC
-- OFFSET N) → AVG по оставшимся → products.average_weight через каталог и страница
-- вики. Лимит N — настройка приложения PRODUCT_WEIGHTS_HISTORY, не зашита в схему.
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_received_weights_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_received_weights;

CREATE TABLE product_received_weights (
    id          BIGSERIAL PRIMARY KEY,            -- порядок FIFO (растёт с каждой записью)
    product_id  TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    weight      INTEGER NOT NULL CHECK (weight > 0)  -- вес принятой единицы, граммы
);

CREATE INDEX product_received_weights_product_idx ON product_received_weights (product_id, id);

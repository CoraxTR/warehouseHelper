-- Состояние товара по дням: одна строка = (товар, день).
-- Строка генерируется в начале дня из product_stock (остатки + скидки):
-- in_stock = есть ли хоть один лот с qty > 0, discount_start = максимальная скидка
-- среди лотов, discount = то же значение на старте.
-- В течение дня discount обновляется при каждом событии (бронь/продажа/возврат)
-- пересчётом из лотов; понижения не логируются, поэтому текущее значение —
-- только колонка, из лога оно не восстанавливается.
-- discount_increases — только повышения скидки (значения, без времени).
-- sold_out_today — маркер «позиция закончилась в течение дня» (даже если остаток вернулся).
-- Зависит от products_schema.sql и product_stock_schema.sql.
-- Применить до первого запуска: psql -f product_day_state_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_day_state;

CREATE TABLE product_day_state (
    product_id         TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    date               DATE NOT NULL,
    in_stock           BOOLEAN NOT NULL,                 -- в наличии / не в наличии
    discount_start     SMALLINT NOT NULL DEFAULT 0 CHECK (discount_start BETWEEN 0 AND 100),  -- скидка на начало дня, %
    discount           SMALLINT NOT NULL DEFAULT 0 CHECK (discount BETWEEN 0 AND 100),        -- актуальная скидка; на конец дня = финальная, %
    discount_increases SMALLINT[] NOT NULL DEFAULT '{}',  -- повышения скидки за день (значения)
    orderable          BOOLEAN NOT NULL DEFAULT true,   -- доступна для заказа
    sold_out_today     BOOLEAN NOT NULL DEFAULT false,  -- позиция закончилась в течение дня
    PRIMARY KEY (product_id, date)
);

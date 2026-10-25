-- Варианты заказа товара у поставщика: одна строка = один режим заказа.
-- Расписание живёт на паре (товар, поставщик), а не на поставщике: по
-- поставщику не понять, какую конкретно продукцию можно заказать сегодня,
-- а у одного товара режимов может быть несколько одновременно — «общий»
-- (со склада поставщика, на следующий день) и «специальный» (под заказ на
-- производство, через неделю).
-- Владелец записи — каталог (карточка позиции /goods/order-schedules);
-- автозаказ читает расписания через usecase каталога.
-- Зависит от products_schema.sql и suppliers_schema.sql.
-- Применить до первого запуска: psql -f product_order_schedules_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_order_schedules;

CREATE TABLE product_order_schedules (
    id                BIGSERIAL PRIMARY KEY,
    product_id        TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,     -- товар каталога
    supplier_id       TEXT NOT NULL REFERENCES suppliers(id) ON DELETE CASCADE,    -- поставщик справочника
    name              TEXT NOT NULL,                            -- название варианта: «Общий», «Под заказ с производства»
    order_days        SMALLINT[] NOT NULL DEFAULT '{}',         -- дни заказа, 1..7 (1=Пн ... 7=Вс)
    delivery_days     SMALLINT[] NOT NULL DEFAULT '{}',         -- дни доставки, 1..7
    delay_days        SMALLINT CHECK (delay_days >= 0),         -- макс. дней между заказом и доставкой; NULL — не задано
    order_cutoff_time TIME,                                     -- время, до которого можно сделать заказ в дни заказа варианта; NULL — не задано
    UNIQUE (product_id, supplier_id, name)
);

CREATE INDEX product_order_schedules_supplier_idx ON product_order_schedules (supplier_id);

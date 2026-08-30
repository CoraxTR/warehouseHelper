-- Поставщики: справочник, по нему группируются товары в заказы;
-- у каждого поставщика свой набор правил вычитки штрихкодов.
-- Расписания: общее (для всех товаров) и спец. (для товаров с special_schedule = true в
-- product_supplier_prices). У поставщика может быть только одно спец. расписание.
-- Применить до первого запуска: psql -f suppliers_schema.sql (или DataGrip).

DROP TABLE IF EXISTS suppliers;

CREATE TABLE suppliers (
    id                    TEXT PRIMARY KEY,      -- UUID контрагента МС (counterparty.id)
    name                  TEXT NOT NULL,         -- название
    decode_rules          TEXT[] NOT NULL DEFAULT '{}',  -- правила вычитки штрихкодов единичных товаров, формат "28-1-6-7-6-13-8-21-8"
    box_decode_rules      TEXT[] NOT NULL DEFAULT '{}',  -- правила вычитки штрихкодов коробок, применяются к коду коробки при приёмке
    -- общее расписание:
    order_days            SMALLINT[] NOT NULL DEFAULT '{}',  -- дни заказа, 1..7 (1=Пн ... 7=Вс)
    delivery_days         SMALLINT[] NOT NULL DEFAULT '{}',  -- дни доставки, 1..7
    delay_days            SMALLINT,                          -- макс. дней между заказом и доставкой; NULL — не задано
    min_order_amount      BIGINT CHECK (min_order_amount >= 0),  -- минимальная сумма заказа, копейки; NULL — не задана
    order_cutoff_time     TIME,                              -- время, до которого можно сделать заказ в дни заказа (общее — и для обычного, и для спец. расписания); NULL — не задано
    -- спец. расписание (для ограниченного круга товаров):
    special_order_days    SMALLINT[] NOT NULL DEFAULT '{}',  -- дни заказа по спец. расписанию, 1..7
    special_delivery_days SMALLINT[] NOT NULL DEFAULT '{}',  -- дни доставки по спец. расписанию, 1..7
    special_delay_days    SMALLINT                           -- задержка по спец. расписанию; NULL — не задано
);

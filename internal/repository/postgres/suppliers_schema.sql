-- Поставщики: справочник, по нему группируются товары в заказы;
-- у каждого поставщика свой набор правил вычитки штрихкодов.
-- Расписания заказа/доставки живут НЕ здесь, а на паре (товар, поставщик) —
-- таблица product_order_schedules: у одного товара может быть несколько
-- режимов заказа одновременно, а по поставщику не понять, что именно можно
-- заказать сегодня.
-- Применить до первого запуска: psql -f suppliers_schema.sql (или DataGrip).

DROP TABLE IF EXISTS suppliers;

CREATE TABLE suppliers (
    id                    TEXT PRIMARY KEY,      -- UUID контрагента МС (counterparty.id)
    name                  TEXT NOT NULL,         -- название
    decode_rules          TEXT[] NOT NULL DEFAULT '{}',  -- правила вычитки штрихкодов единичных товаров, формат "28-1-6-7-6-13-8-21-8"
    box_decode_rules      TEXT[] NOT NULL DEFAULT '{}',  -- правила вычитки штрихкодов коробок, применяются к коду коробки при приёмке
    min_order_amount      BIGINT CHECK (min_order_amount >= 0)  -- минимальная сумма заказа, копейки; NULL — не задана
);

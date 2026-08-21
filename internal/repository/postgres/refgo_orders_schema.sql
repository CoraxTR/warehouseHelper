-- Актуальная схема: первичный ключ — id (UUID МойСклад).
-- Для баз, созданных до перехода с href: сначала применить
-- refgo_orders_migration_to_id.sql (backfill id из href), затем эту схему.

DROP TABLE IF EXISTS refgoOrders;

CREATE TABLE refgoOrders (
    id TEXT PRIMARY KEY,
    name TEXT,
    receiver_name TEXT,
    receiver_phone_number BIGINT,
    description TEXT,
    delivery_planned_date TEXT,
    shipment_address TEXT,
    delivery_interval_from TEXT,
    delivery_interval_until TEXT,
    delivery_region TEXT,
    payment_method TEXT,
    refgo_number TEXT,
    refgo_zone TEXT,
    sum NUMERIC(12, 2),
    chilled_weight NUMERIC(12, 3),
    frozen_weight NUMERIC(12, 3),
    frozen_boxes BIGINT,
    chilled_boxes BIGINT,
    errors JSONB
);

-- Для баз, созданных до добавления колонки refgo_zone
ALTER TABLE refgoOrders ADD COLUMN IF NOT EXISTS refgo_zone TEXT;

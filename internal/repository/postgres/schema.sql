-- Active: 1770100121828@@localhost@5432@postgres

DROP TABLE IF EXISTS refgoOrders;

CREATE TABLE refgoOrders (
    href TEXT PRIMARY KEY,
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
    sum NUMERIC(12, 2),
    chilled_weight NUMERIC(12, 3),
    frozen_weight NUMERIC(12, 3),
    frozen_boxes BIGINT,
    chilled_boxes BIGINT,
    errors JSONB
);
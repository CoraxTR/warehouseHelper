-- Миграция живой базы задач: виды задач модуля «Проверка сайта»
-- (site_discount, site_offer, site_return, site_remove). tasks_schema.sql —
-- DROP+CREATE (для чистой базы), здесь — правка существующей: CHECK на kind
-- пересоздаётся, данные не трогаются. Применяет владелец вместе с выкатом:
--   psql -f tasks_sitecheck_kinds_migration.sql

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_kind_check;

ALTER TABLE tasks ADD CONSTRAINT tasks_kind_check CHECK (kind IN (
    'stock_out', 'stock_in',
    'discount_put', 'discount_raise', 'discount_lower', 'discount_remove',
    'site_discount', 'site_offer', 'site_return', 'site_remove'));

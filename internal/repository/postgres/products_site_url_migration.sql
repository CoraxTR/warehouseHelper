-- Миграция живой базы каталога: столбец products.site_url (адрес позиции на сайте).
-- products_schema.sql — DROP+CREATE (для чистой базы), здесь — правка существующей:
-- данные не трогаются. Применяет владелец на проде вместе с выкатом модуля
-- «Проверка сайта». Идемпотентно: повторный запуск безвреден.

ALTER TABLE products ADD COLUMN IF NOT EXISTS site_url TEXT;

COMMENT ON COLUMN products.site_url IS
    'адрес позиции на сайте (steakhome.ru); NULL — url не задан; пишет только карточка позиции';

-- Миграция «0 = NULL» для скидок product_stock (задача 11).
-- Решение владельца 14.09.2026: 0 в колонке скидки значит «скидки нет», НЕ «запрет скидки».
-- Поэтому заданный ноль приводим к NULL: «0 = NULL» — единственное каноническое
-- представление «скидки нет» (движок расчёта тоже пишет NULL вместо 0), а ноль,
-- оставшийся в базе от старого кода, расходится с этим правилом у читателей
-- (кэш стока, страница «Сроки», effective-скидка дня).
-- Применяет ВЛАДЕЛЕЦ на живой БД (Postgres на VM разработки нет):
--   psql -f product_stock_zero_discount_migration.sql
-- идемпотентна: повторный запуск ничего не меняет (строк с 0 уже нет).
-- Значения 1..100 не трогаются; NULL-ы не трогаются.

UPDATE product_stock SET discount_general = NULL         WHERE discount_general = 0;
UPDATE product_stock SET discount_telegram = NULL        WHERE discount_telegram = 0;
UPDATE product_stock SET discount_general_manual = NULL  WHERE discount_general_manual = 0;
UPDATE product_stock SET discount_telegram_manual = NULL WHERE discount_telegram_manual = 0;

-- Контроль: нулей в колонках скидок быть не должно.
-- SELECT count(*) FROM product_stock
--  WHERE 0 IN (discount_general, discount_telegram, discount_general_manual, discount_telegram_manual);

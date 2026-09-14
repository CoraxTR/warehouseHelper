-- Миграция product_stock для модуля скидок (задача 11) — две правки, обе
-- идемпотентны, применяются одним запуском на живой БД:
--   1) «0 = NULL» в колонках скидок;
--   2) новая колонка discount_source (метка источника «простой» скидки сайта).
-- Решение владельца 14.09.2026: 0 в колонке скидки значит «скидки нет», НЕ «запрет скидки».
-- Поэтому заданный ноль приводим к NULL: «0 = NULL» — единственное каноническое
-- представление «скидки нет» (движок расчёта тоже пишет NULL вместо 0), а ноль,
-- оставшийся в базе от старого кода, расходится с этим правилом у читателей
-- (кэш стока, страница «Сроки», effective-скидка дня).
-- Применяет ВЛАДЕЛЕЦ на живой БД (Postgres на VM разработки нет):
--   psql -f product_stock_zero_discount_migration.sql
-- идемпотентна: повторный запуск ничего не меняет (строк с 0 уже нет, колонка
-- уже добавлена). Значения 1..100 не трогаются; NULL-ы не трогаются.

-- Колонка discount_source: метка источника действующей «простой» скидки сайта
-- ('expiry' — лестница по сроку, 'surplus' — избыток остатка). Нужна расчёту
-- («что снимать при уходе избытка»: движок пишет скидку только в пары со своей
-- меткой) и странице «Сроки» (подсветка ячеек с активной скидкой). CHECK — как
-- в product_stock_schema.sql; у новой таблицы колонка уже есть, тогда ADD
-- COLUMN IF NOT EXISTS не делает ничего.
ALTER TABLE product_stock
    ADD COLUMN IF NOT EXISTS discount_source TEXT
    CHECK (discount_source IN ('manual', 'expiry', 'surplus'));

UPDATE product_stock SET discount_general = NULL         WHERE discount_general = 0;
UPDATE product_stock SET discount_telegram = NULL        WHERE discount_telegram = 0;
UPDATE product_stock SET discount_general_manual = NULL  WHERE discount_general_manual = 0;
UPDATE product_stock SET discount_telegram_manual = NULL WHERE discount_telegram_manual = 0;

-- Контроль: нулей в колонках скидок быть не должно.
-- SELECT count(*) FROM product_stock
--  WHERE 0 IN (discount_general, discount_telegram, discount_general_manual, discount_telegram_manual);

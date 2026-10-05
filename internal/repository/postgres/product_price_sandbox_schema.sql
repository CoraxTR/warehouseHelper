-- Песочница страницы «Проверка цен»: одна строка = последние «фантазийные»
-- значения полей расчёта наценки по товару. Отдельная таблица: страница
-- позволяет менять цену продажи/закупочную/оба НДС и посмотреть, что выйдет,
-- но в products НИЧЕГО не пишет. Снапшот нужен, чтобы, вернувшись к странице
-- (или уйдя с неё), человек увидел свои прошлые значения, а не дефолт из базы.
-- NULL в любой колонке = поле не заполнено (наценка не считается, пока его нет).
-- Зависит от products_schema.sql (FK products.id) — применять после неё.
-- Применить до первого запуска: psql -f product_price_sandbox_schema.sql.

DROP TABLE IF EXISTS product_price_sandbox;

CREATE TABLE product_price_sandbox (
    product_id    TEXT PRIMARY KEY REFERENCES products(id) ON DELETE CASCADE,  -- товар каталога (граница базы)
    sale_price    BIGINT   CHECK (sale_price >= 0),      -- цена продажи, КОПЕЙКИ
    buy_price     BIGINT   CHECK (buy_price >= 0),       -- закупочная цена, КОПЕЙКИ
    effective_vat SMALLINT CHECK (effective_vat >= 0 AND effective_vat <= 100),  -- НДС исходящий (наш), %
    vat_incoming  SMALLINT CHECK (vat_incoming >= 0 AND vat_incoming <= 100)      -- НДС входящий, %
);

-- Выборка страницы идёт по всем строкам сразу (ключ — product_id), отдельного
-- индекса не нужно: PK его уже даёт.

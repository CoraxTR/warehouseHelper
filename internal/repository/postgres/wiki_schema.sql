-- База знаний: страницы поставщиков и продуктов, теги, связи.
-- Применить до первого запуска: psql -f wiki_schema.sql (или DataGrip), аналогично refgo_orders_schema.sql.

DROP TABLE IF EXISTS wiki_page_tags;
DROP TABLE IF EXISTS wiki_tags;
DROP TABLE IF EXISTS wiki_pages;

CREATE TABLE wiki_pages (
    id             BIGSERIAL PRIMARY KEY,
    page_type      TEXT NOT NULL CHECK (page_type IN ('supplier', 'product')),
    title          TEXT NOT NULL,
    content        TEXT NOT NULL DEFAULT '',          -- «Дополнительная информация» (markdown)
    -- поля поставщика:
    contacts       JSONB NOT NULL DEFAULT '[]',       -- [{"name","phone","email","site"}]
    order_days     SMALLINT[] NOT NULL DEFAULT '{}',  -- 1..7, ISO: 1=Пн ... 7=Вс
    delivery_days  SMALLINT[] NOT NULL DEFAULT '{}',
    products       TEXT[] NOT NULL DEFAULT '{}',      -- названия страниц продуктов
    -- поля продукта:
    average_weight TEXT NOT NULL DEFAULT '',
    suppliers      TEXT[] NOT NULL DEFAULT '{}',      -- названия страниц поставщиков
    photo          BYTEA,
    photo_type     TEXT NOT NULL DEFAULT '',
    photo_name     TEXT NOT NULL DEFAULT ''
);

-- Уникальность заголовка без учёта регистра: [[склад]] и [[Склад]] — одна страница.
CREATE UNIQUE INDEX wiki_pages_title_uniq ON wiki_pages (lower(title));

CREATE TABLE wiki_tags (
    id   BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE UNIQUE INDEX wiki_tags_name_uniq ON wiki_tags (lower(name));

CREATE TABLE wiki_page_tags (
    page_id BIGINT NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    tag_id  BIGINT NOT NULL REFERENCES wiki_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (page_id, tag_id)
);

CREATE INDEX wiki_page_tags_tag_idx ON wiki_page_tags (tag_id);

-- Товары из МойСклад: справочник, синхронизируется из API МойСклад.
-- Применить до первого запуска: psql -f products_schema.sql (или DataGrip),
-- аналогично refgo_orders_schema.sql, wiki_schema.sql, qrcodes_schema.sql.

DROP TABLE IF EXISTS products;

CREATE TABLE products (
    id             TEXT PRIMARY KEY,    -- UUID МойСклад
    internal_code  TEXT UNIQUE,         -- код МС (поле code, задаётся вручную, уникален)
    name           TEXT NOT NULL,       -- название из МС
    uom            TEXT NOT NULL,       -- единица измерения из МС (uom.name): "шт", "кг", ...
    group_name     TEXT,                -- имя группы товаров МС (productFolder.name); NULL — товар без группы; по нему товары разделяются в отображении сроков
    folder_id      TEXT,                -- id папки МС (productfolder); NULL — товар без группы; заполняет каталог из дерева папок (FetchProductFolders)
    average_weight NUMERIC(12, 4),      -- средний вес штуки, кг; считает модуль приёмки, передаёт каталогу, пишет только каталог (граница: владелец products — каталог)
    shelf_life     SMALLINT CHECK (shelf_life > 0),  -- общий срок годности, дни; NULL — не задан
    pack_size      SMALLINT CHECK (pack_size > 0),   -- размер пачки, штук; NULL — не пачками (заказ/приёмка поштучно)
    inventory_type TEXT NOT NULL,  -- «Вид инвентаризации» из МС (копия строки); распределение по типам — логика инвентаризации
    short_list     BOOLEAN NOT NULL DEFAULT false,  -- показывать в короткой версии сроков
    track_weekly   BOOLEAN NOT NULL DEFAULT false   -- учитывать в недельном обороте
);

-- Тип учёта (штучный/весовой) не хранится колонкой: выводится из uom в коде
-- (uom в ('кг', 'г', 'т') → весовой, иначе штучный).

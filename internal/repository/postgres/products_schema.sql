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
    track_weekly   BOOLEAN NOT NULL DEFAULT false,  -- учитывать в недельном обороте
    site_url       TEXT,               -- адрес позиции на сайте steakhome.ru; NULL — url не задан, сверка сайта позицию не видит. Пишет ТОЛЬКО карточка позиции (GoodsEditSave): синки из МС (выгрузка дерева, ресинк) колонку не трогают — иначе затирали бы ручной url
    buy_price      BIGINT   CHECK (buy_price >= 0),  -- закупочная цена из МС (объект buyPrice.value, НЕ массив buyPrices), КОПЕЙКИ; NULL — МС не отдал. Пишет синк каталога
    sale_price     BIGINT   CHECK (sale_price >= 0), -- цена продажи из МС (salePrices[].value, у нас тип один — «Цена продажи»), КОПЕЙКИ; синк каталога
    effective_vat  SMALLINT,            -- реальный НДС, % (у нас 10/22); -1 = «без НДС», НАША метка (МС отдаёт 0 + effectiveVatEnabled=false, синк переводит в -1; отдельный флаг не храним — решение владельца 05.10.2026); NULL — МС значения не отдала: у товара useParentVat=true, НДС наследуется от группы и полей vat*/effectiveVat* в ответе нет вовсе (проверено 05.10.2026)
    vat_incoming   SMALLINT CHECK (vat_incoming >= 0 AND vat_incoming <= 100)  -- НДС входящий, % (0..100); ручное поле страницы «Проверка цен», пишет только она (SetIncomingVAT) — синки из МС и карточка позиции колонку не трогают; NULL — не задан
);

-- Тип учёта (штучный/весовой) не хранится колонкой: выводится из uom в коде
-- (uom в ('кг', 'г', 'т') → весовой, иначе штучный).

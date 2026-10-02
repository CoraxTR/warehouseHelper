-- Остатки по срокам годности и скидки: сколько товара с конкретным сроком годности
-- на складе и какие скидки действуют на этот срок. Одна строка = товар + срок годности.
-- Количество всегда в штуках: для весовых товаров остаток пересчитывается по среднему весу.
-- Зависит от products_schema.sql (FK products.id).
-- Владелец — модуль «Сроки» (internal/stock): чтение и запись только через его usecase
-- (БД → кэш → вебсокет). Приёмка и подбор — клиенты через интерфейсы модуля
-- (AcceptStock/PickStock, добавляются вместе с их модулями).
-- Применить до первого запуска: psql -f product_stock_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_stock;

CREATE TABLE product_stock (
    product_id             TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    qty                    BIGINT NOT NULL CHECK (qty >= 0),      -- количество, штук
    best_before            DATE NOT NULL,                         -- до какого дня годен, без времени
    produced_on            DATE,                                  -- дата выработки; NULL — не известна
    discount_general       SMALLINT CHECK (discount_general BETWEEN 0 AND 100),        -- скидка сайт, %; NULL — не задана («просто», пишет модуль расчёта скидок)
    discount_telegram      SMALLINT CHECK (discount_telegram BETWEEN 0 AND 100),       -- скидка телеграм, %; NULL — не задана («просто»)
    discount_general_manual SMALLINT CHECK (discount_general_manual BETWEEN 0 AND 100), -- ручная скидка сайт, %; NULL — не задана (пишет UI сроков)
    discount_telegram_manual SMALLINT CHECK (discount_telegram_manual BETWEEN 0 AND 100), -- ручная скидка телеграм, %; NULL — не задана
    discount_source        TEXT CHECK (discount_source IN ('manual', 'expiry', 'surplus')), -- метка источника «простой» скидки сайта (какую скидку снял/поднял движок: подсветка ячеек); NULL — метки нет
    -- Владельцы стоящих значений колонок (решение владельца 02.10.2026): кто
    -- поставил значение — движок по избытку («surplus»), ступень по сроку
    -- («expiry») или ТГ-день («escalation»: план 14:00/подъём 16:00). Метка
    -- discount_source отвечает на «почему скидка», владелец — на «кто поставил
    -- это значение»: расчёт снимает значение по владельцу, а не по числу
    -- (раньше снятие работало только на ровно 10 % и любое другое значение —
    -- 20 % от ТГ-дня, 30 % от лестницы — оставалось на паре навсегда).
    -- NULL — значения нет либо владелец не известен (строки до появления колонок).
    discount_general_owner  TEXT CHECK (discount_general_owner IN ('surplus', 'expiry', 'escalation', 'manual')),  -- владелец значения discount_general
    discount_telegram_owner TEXT CHECK (discount_telegram_owner IN ('surplus', 'expiry', 'escalation', 'manual')), -- владелец значения discount_telegram (пишет ТГ-день → 'escalation')
    PRIMARY KEY (product_id, best_before)
);

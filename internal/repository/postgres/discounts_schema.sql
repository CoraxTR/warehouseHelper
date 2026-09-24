-- Модуль скидок (internal/discounts): история слотов ТГ и маркеры дня.
--
-- Что хранит:
--   discount_telegram_digest      — рассылка (слот) целиком: день плана, канал, факт отправки;
--   discount_telegram_digest_item — позиции рассылки (лот + скидка + причина);
--   discount_day_flags            — по дню: какие фоновые пересчёты уже сделаны.
--
-- Зачем: антидубль «не был в предыдущей рассылке» (по ЛОТУ, не по товару) и
-- защита от повторной рассылки/повторного подъёма после рестарта («догон
-- после сна»). Решение владельца 14.09.2026: маркер дня — просто булево
-- значение на сегодня, отдельной таблицы эпизодов не нужно.
--
-- Таблицы создаются через IF NOT EXISTS: файл рассчитан на ЖИВУЮ БД
-- (Postgres на VM разработки нет — применяет владелец на препроде).
-- Зависит от products_schema.sql: product_id — id товара МС, ссылка
-- логическая (кросс-модульная граница, FK нет) — как в других модулях.
-- Порядок применения: после products_schema.sql и product_stock_schema.sql.
-- Порядок DROP — обратный зависимостям (items зависим от digest):
--   DROP TABLE IF EXISTS discount_telegram_digest_item;
--   DROP TABLE IF EXISTS discount_telegram_digest;
--   DROP TABLE IF EXISTS discount_day_flags;
-- created_at/updated_at не заводим (решение владельца, 14.09.2026).
--
-- ЖИВАЯ БД: таблица discount_day_flags могла быть создана раньше, до появления
-- колонки turnover_window_done — поэтому ниже идёт идемпотентный ALTER:
--   ALTER TABLE discount_day_flags ADD COLUMN IF NOT EXISTS turnover_window_done BOOLEAN NOT NULL DEFAULT false;
-- ЖИВАЯ БД: позиции рассылки могли быть созданы раньше, до появления колонок
-- контроля вечернего подъёма (16:00) — идемпотентный ALTER:
--   ALTER TABLE discount_telegram_digest_item ADD COLUMN IF NOT EXISTS initial_qty BIGINT;
--   ALTER TABLE discount_telegram_digest_item ADD COLUMN IF NOT EXISTS plan_qty BIGINT;

-- Рассылка (слот ТГ): одна строка = один собранный/отправленный дайджест.
-- 14:00 — план ТГ-слота в чат склада (chat_kind='warehouse'), 09:00 — дайджест
-- в общий чат (chat_kind='general'); значение chat_kind — свободный текст,
-- CHECK не ставим: каналы могут добавиться.
CREATE TABLE IF NOT EXISTS discount_telegram_digest (
    id         BIGSERIAL PRIMARY KEY,
    planned_at DATE NOT NULL,        -- день плана (локальная дата склада, для которой собирали слот)
    chat_kind  TEXT NOT NULL,        -- канал рассылки: 'warehouse' — чат склада, 'general' — общий
    sent_at    TIMESTAMPTZ           -- факт отправки; NULL — собрано, но не отправлено
);

-- Позиции рассылки: одна строка = лот в рассылке (товар + срок годности).
-- Антидубль «не было в предыдущей рассылке» — по паре (product_id, best_before):
-- по лоту, а не по товару (у товара бывает два лота).
CREATE TABLE IF NOT EXISTS discount_telegram_digest_item (
    digest_id         BIGINT NOT NULL REFERENCES discount_telegram_digest(id) ON DELETE CASCADE,
    product_id        TEXT NOT NULL,      -- id товара МС (логическая ссылка на products.id)
    best_before       DATE NOT NULL,      -- срок годности лота скидки
    percent           SMALLINT NOT NULL CHECK (percent BETWEEN 0 AND 100),  -- скидка дня, %
    coeff             NUMERIC(8,2),       -- коэффициент избытка Q/(v×D); NULL — позиция не избыточная
    reason            TEXT NOT NULL CHECK (reason IN ('manual', 'expiry', 'surplus')),  -- источник скидки
    -- Контроль вечернего подъёма (16:00, решение владельца 24.09.2026): у позиции
    -- добора из избытка считается, сколько штук пары надо продать до 16:00.
    -- initial_qty — остаток пары на 14:00 (момент плана), plan_qty — план продаж
    -- по паре. Пара считается проданной, если остаток к 16:00 опустился до
    -- initial_qty − plan_qty или ниже. Оба поля — только у добора из избытка:
    -- у сроковых позиций план продаж не считается, поэтому NULL («не задано»).
    initial_qty       BIGINT,             -- остаток пары на 14:00; NULL — не задано
    plan_qty          BIGINT,             -- сколько штук пары надо продать до 16:00; NULL — не задано
    general_raised_at TIMESTAMPTZ,        -- когда general подняли до telegram (16:00); NULL — не поднимали
    PRIMARY KEY (digest_id, product_id, best_before)
);

-- Антидубль: «был ли лот в предыдущей рассылке» ищется по лоту.
CREATE INDEX IF NOT EXISTS discount_telegram_digest_item_lot_idx
    ON discount_telegram_digest_item (product_id, best_before);

-- Маркеры дня: что модуль уже сделал за день. Нужны для «догона после сна»
-- (после рестарта флаг false → шаг дорабатывается) и защиты от повторной
-- рассылки/повторного подъёма.
CREATE TABLE IF NOT EXISTS discount_day_flags (
    date          DATE PRIMARY KEY,                 -- день (локальная дата склада)
    surplus_done  BOOLEAN NOT NULL DEFAULT false,   -- часовой пересчёт избытка за день сделан
    expiry_done   BOOLEAN NOT NULL DEFAULT false,   -- утренний пересчёт по сроку сделан
    digest_sent   BOOLEAN NOT NULL DEFAULT false,   -- дайджест 09:00 отправлен
    tg_plan_done  BOOLEAN NOT NULL DEFAULT false,   -- план ТГ-слота (14:00) собран и отправлен
    tg_raise_done BOOLEAN NOT NULL DEFAULT false,   -- подъём general до telegram (16:00) выполнен
    turnover_window_done BOOLEAN NOT NULL DEFAULT false  -- полное окно оборотов обновлено (09:00)
    );

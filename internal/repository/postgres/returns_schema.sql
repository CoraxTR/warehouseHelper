-- «Возврат в продажу» (модуль internal/returns): события аудита МС, на которые
-- склад реагирует расформированием (удаление отложенных позиций из заказа,
-- перевод заказа в «Отменён»). Снимков диффа НЕ храним — только id события
-- аудита (uuid): данные страницы возврата перечитываются GET-запросами к МС
-- (аудит хранится долго). Кросс-сервисные ссылки (order_id) — логические.
-- Владелец таблиц — модуль returns.

DROP TABLE IF EXISTS return_events;
DROP TABLE IF EXISTS return_cursor;

CREATE TABLE return_events (
    id           TEXT PRIMARY KEY,       -- uuid события аудита МС (audit/<id>); дедуп-ключ поллера
    kind         TEXT NOT NULL CHECK (kind IN ('order_cancelled', 'positions_removed')),
    order_id     TEXT NOT NULL,          -- uuid заказа МС (id, не href)
    order_name   TEXT NOT NULL,          -- номер заказа (name из events-раскрытия) — для текста сообщения
    moment       TIMESTAMPTZ NOT NULL,   -- момент события (UTC; из audit moment в TZ учётки = МСК)
    status       TEXT NOT NULL DEFAULT 'new'
                 CHECK (status IN ('new', 'sent', 'done')),
    manual_close BOOLEAN NOT NULL DEFAULT false,  -- закрыто вручную (куски не вернулись)
    chat_id      BIGINT,                 -- TG-чат, куда отправлено уведомление
    message_id   BIGINT,                 -- TG message_id (для deleteMessage после обработки)
    processed_at TIMESTAMPTZ             -- момент перехода в done
);

-- Активные события (new/sent) для повторной отправки и страницы «Возврат в продажу».
CREATE INDEX return_events_status_idx ON return_events (status) WHERE status <> 'done';

-- Курсор поллера аудита: единственная строка, last_moment — с какого момента
-- смотреть audit (UTC). Хранится в БД, чтобы после рестарта/сна не было
-- ни пропуска окна, ни дубля.
CREATE TABLE return_cursor (
    id          SMALLINT PRIMARY KEY CHECK (id = 1),
    last_moment TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

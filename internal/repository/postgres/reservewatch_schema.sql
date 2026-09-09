-- «Контроль резервов заказов» (модуль internal/reservewatch): активные
-- уведомления в чат склада о позициях без резерва («нужно отложить») и
-- с неверным резервом («отложены неверно»). Строка живёт, пока проблема
-- активна; позиции исправлены или заказ вышел из окна — модуль удаляет
-- сообщение из чата и строку. Строка вставляется ТОЛЬКО после успешной
-- отправки (message_id известен); сбой между send и insert — редкий дубль
-- в чате, дедуп по PK на следующем тике его не повторит.
-- Владелец таблицы — модуль reservewatch.

DROP TABLE IF EXISTS reserve_notices;

CREATE TABLE reserve_notices (
    order_id   TEXT NOT NULL,          -- uuid заказа МС (id, не href)
    kind       TEXT NOT NULL CHECK (kind IN ('reserve_missing', 'reserve_wrong')),
    message_id BIGINT NOT NULL,        -- TG message_id (deleteMessage после выздоровления)
    PRIMARY KEY (order_id, kind)
);

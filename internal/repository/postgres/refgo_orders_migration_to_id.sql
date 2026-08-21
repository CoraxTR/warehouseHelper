-- Миграция refgoOrders: первичный ключ href → id (UUID МойСклад).
-- Запускать ОДИН раз на базах, созданных до перехода.
-- id восстанавливается из href (последний сегмент пути);
-- существующие строки сохраняются.

ALTER TABLE refgoOrders ADD COLUMN IF NOT EXISTS id TEXT;

UPDATE refgoOrders SET id = regexp_replace(href, '^.*/', '')
WHERE id IS NULL OR id = '';

-- Контроль: не должно остаться строк с пустым id.
-- SELECT count(*) FROM refgoOrders WHERE id IS NULL OR id = '';

ALTER TABLE refgoOrders DROP CONSTRAINT IF EXISTS refgoOrders_pkey;
ALTER TABLE refgoOrders ADD PRIMARY KEY (id);

-- href больше не нужен коду и полностью выводится из id (URLstart + path + id).
ALTER TABLE refgoOrders DROP COLUMN IF EXISTS href;

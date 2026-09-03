-- Жалобы клиентов: обращения и связанные товары.
-- Фотографии жалобы хранятся НЕ в БД, а в zip-архиве на диске
-- (папка из COMPLAINTS_PHOTOS_DIR, файл <complaint_id>.zip) — см.
-- internal/complaints/photostore. Таблицы фото в БД нет: архив — источник истины.
-- Применить до первого запуска: psql -f complaints_schema.sql (или DataGrip),
-- аналогично qrcodes_schema.sql, wiki_schema.sql.

DROP TABLE IF EXISTS complaint_items;
DROP TABLE IF EXISTS complaints;
DROP TABLE IF EXISTS complaint_role_tags;

CREATE TABLE complaints (
    id              BIGSERIAL PRIMARY KEY,
    ms_order_number TEXT NOT NULL,              -- номер заказа в МойСклад (как введён менеджером)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),  -- дата создания (авто)
    phone           TEXT NOT NULL,              -- телефон клиента в нормализованном виде 7XXXXXXXXXX
    description     TEXT NOT NULL DEFAULT '',   -- описание (текст без ограничения длины)
    actions         TEXT NOT NULL DEFAULT '',   -- предпринятые действия
    status          TEXT NOT NULL DEFAULT 'created'
                    CHECK (status IN ('created', 'reviewing', 'warehouse', 'supplier', 'completed', 'client')),
    deadline        TIMESTAMPTZ NOT NULL        -- дедлайн: при сохранении обязан быть > now();
                                                -- по его наступлению шлётся напоминание, затем +24 часа
);

-- Список «Жалобы» (статус != completed) и «Полный список» — по убыванию даты создания.
CREATE INDEX complaints_created_idx ON complaints (created_at DESC);
-- Фильтр всех жалоб клиента.
CREATE INDEX complaints_phone_idx ON complaints (phone);
-- Тикер напоминаний: статус != completed AND deadline <= now().
CREATE INDEX complaints_deadline_idx ON complaints (deadline) WHERE status <> 'completed';

-- Товары обращения (одно обращение — несколько строк; минимум одна строка
-- обязательна — валидирует usecase). product_name — снимок названия на момент
-- сохранения: товар в каталоге могут переименовать или удалить, история жива.
CREATE TABLE complaint_items (
    id           BIGSERIAL PRIMARY KEY,
    complaint_id BIGINT NOT NULL REFERENCES complaints(id) ON DELETE CASCADE,
    product_id   TEXT REFERENCES products(id) ON DELETE SET NULL,  -- NULL — товар удалён из каталога
    product_name TEXT NOT NULL
);

CREATE INDEX complaint_items_complaint_idx ON complaint_items (complaint_id);
-- Поиск «все жалобы, где фигурирует товар».
CREATE INDEX complaint_items_product_idx ON complaint_items (product_id);

-- Телеграм-теги ролей для уведомлений в common_chat: «валидатор» и «склад».
-- Тег вставляется в начало сообщения, если зарегистрирован.
CREATE TABLE complaint_role_tags (
    role TEXT PRIMARY KEY CHECK (role IN ('validator', 'warehouse')),
    tag  TEXT NOT NULL                          -- например "@ivanov" (как зарегистрировано)
);

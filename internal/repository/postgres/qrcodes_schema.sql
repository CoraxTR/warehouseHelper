-- Честный знак: заказы и фото кодов маркировки.
-- Применить до первого запуска: psql -f qrcodes_schema.sql (или DataGrip),
-- аналогично refgo_orders_schema.sql и wiki_schema.sql.

DROP TABLE IF EXISTS qrcode_photos;
DROP TABLE IF EXISTS qrcode_orders;

CREATE TABLE qrcode_orders (
    id           BIGSERIAL PRIMARY KEY,
    order_number TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Один заказ = один номер; повторные добавления фото дополняют заказ.
CREATE UNIQUE INDEX qrcode_orders_number_uniq ON qrcode_orders (order_number);

CREATE TABLE qrcode_photos (
    id         TEXT PRIMARY KEY,   -- id фото, генерируется приложением; имя папки в QRCodes/
    order_id   BIGINT NOT NULL REFERENCES qrcode_orders(id) ON DELETE CASCADE,
    ext        TEXT NOT NULL,      -- расширение файла (jpg/png/webp/...)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX qrcode_photos_order_idx ON qrcode_photos (order_id);

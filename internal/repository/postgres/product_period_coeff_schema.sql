-- Коэффициент изменения заказа по периодам (неделя/месяц): одна строка = (товар, период).
-- Строка существует ТОЛЬКО для событийного периода (в нём произошло хотя бы одно
-- событие: скидка, «закончился», заморозка, недоступность, откаты). Чистые периоды
-- строк не имеют.
--
-- coeff — накопленное значение цепочки, «сидящее» на этом периоде. Перенос: при
-- событии в периоде P коэффициент P = коэффициент P-1 + вклад события, а у P-1
-- обнуляется (при этом его счётчики событий сохраняются — это факты).
-- sold_out/discount/frozen/unavailable — живые счётчики событий периода (нужны для
-- предусловий откатов «возвращать нечего» и для пересчёта coeff при баге).
--
-- Значения: скидка -1, закончился +1, заморозка -2, недоступен 0 (держит цепочку),
-- откаты снимают 1 с coeff и со счётчика соответствующего типа.
-- Зависит от products_schema.sql (FK products.id).
-- Применить до первого запуска: psql -f product_period_coeff_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_period_coeff;

CREATE TABLE product_period_coeff (
    product_id   TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    period_type  SMALLINT NOT NULL,          -- 1 = неделя (понедельник), 2 = месяц (1-е число)
    period_start DATE NOT NULL,              -- понедельник недели / первое число месяца
    coeff        SMALLINT NOT NULL DEFAULT 0, -- накопленное значение, «сидящее» на этом периоде
    sold_out     SMALLINT NOT NULL DEFAULT 0, -- живые «закончился» этого периода
    discount     SMALLINT NOT NULL DEFAULT 0, -- живые «скидка» этого периода
    frozen       SMALLINT NOT NULL DEFAULT 0, -- живые «заморозка» этого периода
    unavailable  SMALLINT NOT NULL DEFAULT 0, -- дни недоступности этого периода
    PRIMARY KEY (product_id, period_type, period_start)
);

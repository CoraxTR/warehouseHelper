-- Коды товаров у поставщика: связка «внешний код (вычитываемый из штрихкода)» → товар.
-- У одного товара у одного поставщика может быть несколько кодов;
-- один и тот же товар у разных поставщиков — с разными кодами.
-- Зависит от products_schema.sql и suppliers_schema.sql.
-- Применить до первого запуска: psql -f product_supplier_barcodes_schema.sql (или DataGrip).

DROP TABLE IF EXISTS product_supplier_barcodes;

CREATE TABLE product_supplier_barcodes (
    supplier_id   TEXT NOT NULL REFERENCES suppliers(id) ON DELETE CASCADE,
    external_code TEXT NOT NULL,  -- код товара у поставщика: базовая часть штрихкода, вычитываемая decode_rule
    product_id    TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    PRIMARY KEY (supplier_id, external_code)
);

CREATE INDEX product_supplier_barcodes_product_idx ON product_supplier_barcodes (product_id);

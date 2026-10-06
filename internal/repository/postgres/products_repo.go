package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"warehouseHelper/internal/domain"
)

// productColumns — колонки products в порядке SELECT/INSERT (без id в INSERT
// не обойтись, но сканирование идёт в этом порядке).
// buy_price/sale_price/effective_vat — цены из МС (копейки/%, см. products_schema.sql);
// порядок обязан совпадать с порядком Scan в scanProduct (иначе рассинхрон молчит).
const productColumns = `id, internal_code, name, uom, group_name, folder_id, average_weight,
    shelf_life, pack_size, inventory_type, short_list, track_weekly, site_url,
    buy_price, sale_price, effective_vat, vat_incoming`

// scanProduct сканирует строку в domain.Product (порядок productColumns).
//
// nullable TEXT-колонки каталога (internal_code, group_name, folder_id,
// site_url) читаются через *string: pgx не кладёт NULL в string, а по схеме
// products эти колонки nullable (код МС не задан, товар без папки, url на сайте
// не заполнен) — прямая запись в string падала бы на первом же таком товаре.
// Пустая строка = «не задано» (textValue).
//
// Цены (buy_price, sale_price, effective_vat) тоже nullable: у части товаров МС
// значения не отдаёт (НДС наследуется от группы, buyPrice/salePrices пусты), а
// домен различает «МС не отдала» (nil) и «без НДС» (-1). Поэтому читаем через
// *int64/*int16, а не прямо в int64/int16: pgx не кладёт NULL в неуказатель.
func scanProduct(row pgx.Row) (*domain.Product, error) {
	var (
		p                                          domain.Product
		internalCode, groupName, folderID, siteURL *string
		buyPrice, salePrice                        *int64
		effectiveVAT                               *int16
		vatIncoming                                *int16
	)
	if err := row.Scan(
		&p.ID, &internalCode, &p.Name, &p.UOM, &groupName, &folderID,
		&p.AverageWeight, &p.ShelfLife, &p.PackSize,
		&p.InventoryType, &p.ShortList, &p.TrackWeekly, &siteURL,
		&buyPrice, &salePrice, &effectiveVAT, &vatIncoming,
	); err != nil {
		return nil, err
	}
	p.InternalCode = textValue(internalCode)
	p.GroupName = textValue(groupName)
	p.FolderID = textValue(folderID)
	p.SiteURL = textValue(siteURL)
	p.BuyPrice = buyPrice
	p.SalePrice = salePrice
	p.EffectiveVat = effectiveVAT
	p.VATIncoming = vatIncoming

	return &p, nil
}

// GetProductsByIDs — товары каталога по списку id (для бэкфилла оборотов;
// порядок не гарантирован).
func (pg *PGClient) GetProductsByIDs(ctx context.Context, ids []string) ([]domain.Product, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+productColumns+`
        FROM products
        WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := make([]domain.Product, 0)
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		products = append(products, *p)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return products, nil
}

// LoadAllProducts возвращает все товары каталога, отсортированные по
// (group_name, name) — страницы состояния по дням (календарь, отчёт).
func (pg *PGClient) LoadAllProducts(ctx context.Context) ([]domain.Product, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+productColumns+`
        FROM products
        ORDER BY group_name, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := make([]domain.Product, 0)
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		products = append(products, *p)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return products, nil
}

// upsertProductSQL — INSERT ... ON CONFLICT (id) DO UPDATE товара каталога.
// Цены (buy_price/sale_price/effective_vat) при конфликте НЕ затираются пустым
// значением: COALESCE оставляет прежнее, если МС поля не отдала (у товара нет
// цены, НДС наследуется от группы) — решение владельца «МС не отдал цену — не
// обнуляем запись в БД». Для нового товара прежнего значения нет, поэтому
// вставляется NULL. Остальные поля товара синк по-прежнему перезаписывает
// снимком. `site_url` в запросе нет вовсе: это ручное поле карточки позиции
// (SetProductSiteURL), синк его не трогает.
const upsertProductSQL = `
        INSERT INTO products (
            id, internal_code, name, uom, group_name, folder_id, average_weight,
            shelf_life, pack_size, inventory_type, short_list, track_weekly,
            buy_price, sale_price, effective_vat
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
        ON CONFLICT (id) DO UPDATE SET
            internal_code = EXCLUDED.internal_code,
            name          = EXCLUDED.name,
            uom           = EXCLUDED.uom,
            group_name    = EXCLUDED.group_name,
            folder_id     = EXCLUDED.folder_id,
            average_weight = EXCLUDED.average_weight,
            shelf_life    = EXCLUDED.shelf_life,
            pack_size     = EXCLUDED.pack_size,
            inventory_type = EXCLUDED.inventory_type,
            short_list    = EXCLUDED.short_list,
            track_weekly  = EXCLUDED.track_weekly,
            buy_price     = COALESCE(EXCLUDED.buy_price, products.buy_price),
            sale_price    = COALESCE(EXCLUDED.sale_price, products.sale_price),
            effective_vat = COALESCE(EXCLUDED.effective_vat, products.effective_vat)
    `

// UpsertProduct создаёт или обновляет товар каталога (upsert по id).
// Дубль internal_code (уникальный индекс, занят другим товаром) →
// domain.ErrInternalCodeTaken.
func (pg *PGClient) UpsertProduct(ctx context.Context, p *domain.Product) error {
	_, err := pg.Pool.Exec(ctx, upsertProductSQL,
		p.ID, p.InternalCode, p.Name, p.UOM, p.GroupName, p.FolderID, p.AverageWeight,
		p.ShelfLife, p.PackSize, p.InventoryType, p.ShortList, p.TrackWeekly,
		p.BuyPrice, p.SalePrice, p.EffectiveVat,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrInternalCodeTaken
		}

		return fmt.Errorf("upsert product %s: %w", p.ID, err)
	}

	return nil
}

// LoadProductsWithSiteURL — позиции каталога с заданным url на сайте
// (products.site_url): вход сверки модуля «Проверка сайта». Позиции без url в
// срез не попадают — их на сайте либо нет, либо адрес ещё не заполнен.
func (pg *PGClient) LoadProductsWithSiteURL(ctx context.Context) ([]domain.Product, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+productColumns+`
        FROM products
        WHERE site_url IS NOT NULL AND site_url <> ''
        ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("load products with site url: %w", err)
	}
	defer rows.Close()

	products := make([]domain.Product, 0)
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("load products with site url: %w", err)
		}
		products = append(products, *p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load products with site url: %w", err)
	}

	return products, nil
}

// SearchProducts ищет товары каталога: точное совпадение internal_code
// или подстрока name (без учёта регистра). Результат по имени.
func (pg *PGClient) SearchProducts(ctx context.Context, query string) ([]domain.Product, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+productColumns+`
        FROM products
        WHERE internal_code = $1 OR name ILIKE '%' || $1 || '%'
        ORDER BY lower(name)
    `, query)
	if err != nil {
		return nil, fmt.Errorf("search products %q: %w", query, err)
	}
	defer rows.Close()

	products := make([]domain.Product, 0)
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("search products %q: %w", query, err)
		}
		products = append(products, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search products %q: %w", query, err)
	}

	return products, nil
}

// GetProduct — товар каталога по id; нет записи — domain.ErrProductNotFound.
func (pg *PGClient) GetProduct(ctx context.Context, id string) (*domain.Product, error) {
	p, err := scanProduct(pg.Pool.QueryRow(ctx, `
        SELECT `+productColumns+`
        FROM products
        WHERE id = $1
    `, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrProductNotFound
		}

		return nil, fmt.Errorf("get product %s: %w", id, err)
	}

	return p, nil
}

// SetProductSiteURL записывает адрес позиции на сайте (products.site_url) —
// единственное место записи колонки: url задаёт человек в карточке позиции, а
// синки из МС (выгрузка дерева, ресинк) колонку не трогают. Пустая строка
// означает «url не задан» и пишется как NULL. Товара нет →
// domain.ErrProductNotFound.
func (pg *PGClient) SetProductSiteURL(ctx context.Context, productID, siteURL string) error {
	tag, err := pg.Pool.Exec(ctx, `
        UPDATE products SET site_url = NULLIF($2, '') WHERE id = $1
    `, productID, siteURL)
	if err != nil {
		return fmt.Errorf("set product site url %s: %w", productID, err)
	}

	if tag.RowsAffected() == 0 {
		return domain.ErrProductNotFound
	}

	return nil
}

// SetProductIncomingVAT записывает входящий НДС товара (products.vat_incoming, %) —
// единственное место записи колонки: значение задаёт человек на странице
// «Проверка цен», синки из МС и карточка позиции колонку не трогают. nil
// означает «не задан» и пишется как NULL. Товара нет → domain.ErrProductNotFound.
func (pg *PGClient) SetProductIncomingVAT(ctx context.Context, productID string, vat *int16) error {
	tag, err := pg.Pool.Exec(ctx, `
        UPDATE products SET vat_incoming = $2 WHERE id = $1
    `, productID, vat)
	if err != nil {
		return fmt.Errorf("set product incoming vat %s: %w", productID, err)
	}

	if tag.RowsAffected() == 0 {
		return domain.ErrProductNotFound
	}

	return nil
}

// UpdateProductAverageWeight обновляет средний вес товара (кг) — вход модуля
// среднего веса (граница: products.average_weight пишет только каталог).
// Товара нет → domain.ErrProductNotFound.
func (pg *PGClient) UpdateProductAverageWeight(ctx context.Context, productID string, avgKg float64) error {
	tag, err := pg.Pool.Exec(ctx, `
        UPDATE products SET average_weight = $2 WHERE id = $1
    `, productID, avgKg)
	if err != nil {
		return fmt.Errorf("update average weight %s: %w", productID, err)
	}

	if tag.RowsAffected() == 0 {
		return domain.ErrProductNotFound
	}

	return nil
}

// LoadProductAverageWeights возвращает средние веса штучных товаров (кг) по
// списку products.id (average_weight — NUMERIC(12,4) КГ). Товары без заданного
// или неположительного веса в карту не попадают — вызывающий считает их
// пропусками (общий вес заказа). Только примитивы: модульные типы usecase здесь
// не импортируются (конвертацию делает DI-адаптер в app/di.go).
func (pg *PGClient) LoadProductAverageWeights(ctx context.Context, productIDs []string) (map[string]float64, error) {
	out := make(map[string]float64, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}

	rows, err := pg.Pool.Query(ctx, `
        SELECT id, average_weight
        FROM products
        WHERE id = ANY($1) AND average_weight IS NOT NULL AND average_weight > 0
    `, productIDs)
	if err != nil {
		return nil, fmt.Errorf("load product average weights: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id     string
			weight float64
		)
		if err := rows.Scan(&id, &weight); err != nil {
			return nil, fmt.Errorf("load product average weights: %w", err)
		}
		out[id] = weight
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load product average weights: %w", err)
	}

	return out, nil
}

// productIDsSQL — id всех товаров каталога (вход обновителя цен: сначала
// спрашиваем, кого вообще можно обновлять).
const productIDsSQL = `SELECT id FROM products ORDER BY id`

// ProductIDs отдаёт id всех товаров каталога (ORDER BY id — стабильный обход
// обновителем цен). Пустой каталог → пустой (не nil) срез.
func (pg *PGClient) ProductIDs(ctx context.Context) ([]string, error) {
	rows, err := pg.Pool.Query(ctx, productIDsSQL)
	if err != nil {
		return nil, fmt.Errorf("product ids: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("product ids: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("product ids: %w", err)
	}

	return ids, nil
}

// updateProductPriceSQL — обновление цен одной позиции.
//
// nil-поле (МС его не отдала — это НЕ «сняли цену») означает «не трогаем»:
// COALESCE($N, колонка) подставляет текущее значение строки. IS DISTINCT FROM
// в WHERE отсекает холостые UPDATE — если ни одно поле не изменилось, строка не
// обновляется и в счётчик (RowsAffected) не попадает. IS DISTINCT FROM, а не
// <>: он корректно сравнивает и NULL. effective_vat = -1 — наша метка «без
// НДС», обычное значение, отличное от NULL.
const updateProductPriceSQL = `
    UPDATE products SET
        buy_price     = COALESCE($2, buy_price),
        sale_price    = COALESCE($3, sale_price),
        effective_vat = COALESCE($4, effective_vat)
    WHERE id = $1
      AND (buy_price     IS DISTINCT FROM COALESCE($2, buy_price)
        OR sale_price    IS DISTINCT FROM COALESCE($3, sale_price)
        OR effective_vat IS DISTINCT FROM COALESCE($4, effective_vat))
`

// UpdateProductPrices массово обновляет цены товаров и возвращает число РЕАЛЬНО
// обновлённых строк (изменившиеся позиции; nil-поля не затирают известное).
// Каждая позиция — отдельный параметризованный UPDATE одним батчем в
// транзакции (как insertOrderPickingUnits): не N+1 по кругу, но и не один
// гигантский INSERT — семантика на элемент (COALESCE + отсев холостых).
// Пустой вход → (0, nil) без обращения к БД.
func (pg *PGClient) UpdateProductPrices(ctx context.Context, prices []domain.ProductPrice) (int, error) {
	if len(prices) == 0 {
		return 0, nil
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("update product prices: %w", err)
	}
	// Rollback после Commit вернёт sql.ErrTxDone — это норма (defer-страховка).
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, p := range prices {
		batch.Queue(updateProductPriceSQL, p.ID, p.BuyPrice, p.SalePrice, p.EffectiveVat)
	}

	results := tx.SendBatch(ctx, batch)
	updated := 0
	for i := range prices {
		tag, err := results.Exec()
		if err != nil {
			_ = results.Close()
			return 0, fmt.Errorf("update product price %s: %w", prices[i].ID, err)
		}
		updated += int(tag.RowsAffected())
	}
	if err := results.Close(); err != nil {
		return 0, fmt.Errorf("update product prices close: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("update product prices commit: %w", err)
	}

	return updated, nil
}

// productPriceCursorGetSQL — курсор обновителя цен (таблица product_price_cursor,
// единственная строка id=1): момент следующего инкремента и МСК-дата последнего
// полного прохода по всем товарам.
const productPriceCursorGetSQL = `SELECT next_scan_at, last_full_scan_at FROM product_price_cursor WHERE id = 1`

// productPriceCursorSetSQL — upsert единственной строки курсора id=1.
// Дата приходит строкой МСК-календаря (DateOnly) и кладётся в DATE как есть —
// её не конвертируем через timestamptz, иначе таймзона сессии сдвинула бы
// календарные сутки. NULLIF — «полного прохода ещё не было».
const productPriceCursorSetSQL = `
    INSERT INTO product_price_cursor (id, next_scan_at, last_full_scan_at)
    VALUES (1, $1, NULLIF($2, '')::date)
    ON CONFLICT (id) DO UPDATE SET next_scan_at = EXCLUDED.next_scan_at,
        last_full_scan_at = EXCLUDED.last_full_scan_at, updated_at = now()`

// GetPriceCursor — курсор обновителя цен: момент следующего инкремента,
// МСК-дата последнего полного прохода ("" — прохода ещё не было) и признак
// наличия строки. Exists=false — строки нет вовсе (первый запуск: модуль
// каталога делает полный проход и начинает вести курсор).
func (pg *PGClient) GetPriceCursor(ctx context.Context) (domain.ProductPriceCursor, error) {
	return scanPriceCursor(pg.Pool.QueryRow(ctx, productPriceCursorGetSQL))
}

// scanPriceCursor разбирает строку курсора: ErrNoRows — не ошибка, а «курсора
// нет» (Exists=false). NULL в last_full_scan_at (дата ещё не писалась) отдаётся
// пустой строкой — сравнение с сегодняшней МСК-датой тогда не равно и запускает
// полный проход. Вынесен из GetPriceCursor, чтобы ветку проверять подделкой
// pgx.Row без БД (как прочие scan-хелперы репозитория).
func scanPriceCursor(row pgx.Row) (domain.ProductPriceCursor, error) {
	var (
		next     time.Time
		lastFull *time.Time
	)
	if err := row.Scan(&next, &lastFull); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ProductPriceCursor{}, nil
		}

		return domain.ProductPriceCursor{}, fmt.Errorf("get product price cursor: %w", err)
	}

	cur := domain.ProductPriceCursor{Next: next, Exists: true}
	if lastFull != nil {
		cur.LastFullScan = lastFull.Format(time.DateOnly)
	}

	return cur, nil
}

// SetPriceCursor сохраняет момент, с которого сканировать следующий инкремент
// каталога, и МСК-дату последнего полного прохода (upsert строки id=1).
func (pg *PGClient) SetPriceCursor(ctx context.Context, next time.Time, lastFullScan string) error {
	if _, err := pg.Pool.Exec(ctx, productPriceCursorSetSQL, next, lastFullScan); err != nil {
		return fmt.Errorf("set product price cursor: %w", err)
	}

	return nil
}

// listInventoryTypesSQL — непустые виды инвентаризации каталога (products.inventory_type),
// по алфавиту: выбор на странице «Инвентаризация».
const listInventoryTypesSQL = `
        SELECT DISTINCT inventory_type
        FROM products
        WHERE inventory_type <> ''
        ORDER BY inventory_type`

// loadProductsByInventoryTypeSQL — товары одного вида инвентаризации. Порядок —
// код склада, затем название; товары без кода (NULL/пусто) идут в конец: в документе
// они всё равно есть (нулевым количеством), но сканировать их нельзя.
const loadProductsByInventoryTypeSQL = `
        SELECT ` + productColumns + `
        FROM products
        WHERE inventory_type = $1
        ORDER BY (internal_code IS NULL OR internal_code = ''), internal_code, name`

// ListInventoryTypes — уникальные непустые виды инвентаризации каталога.
func (pg *PGClient) ListInventoryTypes(ctx context.Context) ([]string, error) {
	rows, err := pg.Pool.Query(ctx, listInventoryTypesSQL)
	if err != nil {
		return nil, fmt.Errorf("list inventory types: %w", err)
	}
	defer rows.Close()

	types := make([]string, 0)
	for rows.Next() {
		var inventoryType string
		if err := rows.Scan(&inventoryType); err != nil {
			return nil, fmt.Errorf("list inventory types: %w", err)
		}
		types = append(types, inventoryType)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list inventory types: %w", err)
	}

	return types, nil
}

// LoadProductsByInventoryType — товары указанного вида инвентаризации
// (страница инвентаризации: состав группы и строки документа).
func (pg *PGClient) LoadProductsByInventoryType(ctx context.Context, inventoryType string) ([]domain.Product, error) {
	rows, err := pg.Pool.Query(ctx, loadProductsByInventoryTypeSQL, inventoryType)
	if err != nil {
		return nil, fmt.Errorf("load products by inventory type %q: %w", inventoryType, err)
	}
	defer rows.Close()

	products := make([]domain.Product, 0)
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("load products by inventory type %q: %w", inventoryType, err)
		}
		products = append(products, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load products by inventory type %q: %w", inventoryType, err)
	}

	return products, nil
}

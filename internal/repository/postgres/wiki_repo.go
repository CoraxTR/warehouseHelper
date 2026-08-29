package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"warehouseHelper/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// wikiPageColumns — список колонок таблицы wiki_pages, общий для выборок страницы.
// Порядок должен совпадать с порядком Scan в scanWikiPage.
const wikiPageColumns = `
    id, page_type, title, content, contacts, order_days, delivery_days,
    average_weight, suppliers, products, supplier_id, (photo IS NOT NULL)`

// GetPage возвращает вики-страницу по заголовку (без учёта регистра),
// включая теги. Если страница не найдена, возвращает (nil, nil).
func (pg *PGClient) GetPage(ctx context.Context, title string) (*domain.WikiPage, error) {
	row := pg.Pool.QueryRow(ctx, `
        SELECT `+wikiPageColumns+`
        FROM wiki_pages
        WHERE lower(title) = lower($1)
    `, title)

	page, err := scanWikiPage(row)
	if err != nil {
		return nil, err
	}
	if page == nil {
		//nolint:nilnil // контракт: страница не найдена
		return nil, nil
	}

	// Теги страницы — отдельным запросом.
	tagRows, err := pg.Pool.Query(ctx, `
        SELECT t.name
        FROM wiki_page_tags pt
        JOIN wiki_tags t ON t.id = pt.tag_id
        WHERE pt.page_id = $1
        ORDER BY lower(t.name)
    `, page.ID)
	if err != nil {
		return nil, err
	}
	defer tagRows.Close()

	for tagRows.Next() {
		var name string

		if err := tagRows.Scan(&name); err != nil {
			return nil, err
		}

		page.Tags = append(page.Tags, name)
	}

	if err := tagRows.Err(); err != nil {
		return nil, err
	}

	return page, nil
}

// scanWikiPage читает строку результата запроса в WikiPage.
// Если строк нет (pgx.ErrNoRows), возвращает (nil, nil).
//
//nolint:nilnil // контракт репозитория: (nil, nil) = страница не найдена
func scanWikiPage(row pgx.Row) (*domain.WikiPage, error) {
	var (
		id                       int64
		pageType, title, content string
		averageWeight            string
		contactsJSON             []byte
		orderDays, deliveryDays  []int16
		suppliers                []string
		products                 []string
		supplierID               *string
		hasPhoto                 bool
	)

	err := row.Scan(
		&id, &pageType, &title, &content, &contactsJSON,
		&orderDays, &deliveryDays, &averageWeight, &suppliers, &products, &supplierID, &hasPhoto,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	page := &domain.WikiPage{
		ID:            id,
		Type:          domain.PageType(pageType),
		Title:         title,
		Content:       content,
		OrderDays:     int16ToInt(orderDays),
		DeliveryDays:  int16ToInt(deliveryDays),
		AverageWeight: averageWeight,
		Suppliers:     suppliers,
		Products:      products,
		HasPhoto:      hasPhoto,
	}
	if supplierID != nil {
		page.SupplierID = *supplierID
	}

	if len(contactsJSON) > 0 {
		if err = json.Unmarshal(contactsJSON, &page.Contacts); err != nil {
			return nil, fmt.Errorf("unmarshal page contacts: %w", err)
		}
	}

	return page, nil
}

// int16ToInt конвертирует SMALLINT[] (pgx) в []int.
func int16ToInt(src []int16) []int {
	res := make([]int, len(src))
	for i, v := range src {
		res[i] = int(v)
	}

	return res
}

// intsToInt16 конвертирует []int в SMALLINT[] (pgx).
func intsToInt16(src []int) []int16 {
	res := make([]int16, len(src))
	for i, v := range src {
		//nolint:gosec // G115: значения валидируются до записи (дни недели 1..7), переполнение невозможно
		res[i] = int16(v)
	}

	return res
}

// lowerStrings возвращает элементы списка в нижнем регистре
// (для регистронезависимого сравнения в SQL).
func lowerStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, strings.ToLower(it))
	}

	return out
}

// GetPhoto возвращает фото страницы и его MIME-тип.
// Если страницы нет или фото не загружено — (nil, "", nil).
func (pg *PGClient) GetPhoto(ctx context.Context, title string) (data []byte, contentType string, err error) {
	var photo []byte

	err = pg.Pool.QueryRow(ctx, `
        SELECT photo, photo_type
        FROM wiki_pages
        WHERE lower(title) = lower($1)
    `, title).Scan(&photo, &contentType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", nil
		}

		return nil, "", err
	}

	if photo == nil {
		return nil, "", nil
	}

	return photo, contentType, nil
}

// escapeLike экранирует спецсимволы шаблона LIKE (\, %, _) для ESCAPE '\'.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)

	return s
}

// GetBacklinks возвращает заголовки страниц, в содержимом которых
// есть ссылка [[title]] (без учёта регистра ссылки и заголовка).
func (pg *PGClient) GetBacklinks(ctx context.Context, title string) ([]string, error) {
	// lower(content) с обеих сторон: вики-ссылки пишутся в любом регистре.
	pattern := `%[[` + escapeLike(strings.ToLower(title)) + `]]%`

	rows, err := pg.Pool.Query(ctx, `
        SELECT title
        FROM wiki_pages
        WHERE lower(content) LIKE $1 ESCAPE '\'
    `, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	titles := make([]string, 0)

	for rows.Next() {
		var t string

		if err := rows.Scan(&t); err != nil {
			return nil, err
		}

		titles = append(titles, t)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return titles, nil
}

// ResolveLinkTargets сопоставляет сырые заголовки ссылок с существующими
// страницами: ключ — заголовок в нижнем регистре, значение — фактический.
// Пустой вход возвращает (nil, nil).
//
//nolint:nilnil // контракт репозитория: пустой вход = (nil, nil)
func (pg *PGClient) ResolveLinkTargets(ctx context.Context, rawTitles []string) (map[string]string, error) {
	if len(rawTitles) == 0 {
		return nil, nil
	}

	// Приводим цели к нижнему регистру — сравнение идёт с lower(title).
	keys := make([]string, 0, len(rawTitles))
	for _, t := range rawTitles {
		keys = append(keys, strings.ToLower(strings.TrimSpace(t)))
	}

	rows, err := pg.Pool.Query(ctx, `
        SELECT title
        FROM wiki_pages
        WHERE lower(title) = ANY($1::text[])
    `, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resolved := make(map[string]string, len(rawTitles))

	for rows.Next() {
		var title string

		if err := rows.Scan(&title); err != nil {
			return nil, err
		}

		resolved[strings.ToLower(title)] = title
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return resolved, nil
}

// ListPageTitlesByType возвращает заголовки страниц заданного типа,
// отсортированные без учёта регистра.
func (pg *PGClient) ListPageTitlesByType(ctx context.Context, typ domain.PageType) ([]string, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT title
        FROM wiki_pages
        WHERE page_type = $1
        ORDER BY lower(title)
    `, string(typ))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	titles := make([]string, 0)

	for rows.Next() {
		var title string

		if err := rows.Scan(&title); err != nil {
			return nil, err
		}

		titles = append(titles, title)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return titles, nil
}

// TagCloud возвращает теги и количество страниц с каждым из них,
// отсортированные по названию без учёта регистра.
func (pg *PGClient) TagCloud(ctx context.Context) ([]domain.WikiTagCount, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT t.name, count(pt.page_id)
        FROM wiki_tags t
        JOIN wiki_page_tags pt ON pt.tag_id = t.id
        GROUP BY t.name
        ORDER BY lower(t.name)
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cloud := make([]domain.WikiTagCount, 0)

	for rows.Next() {
		var (
			name  string
			count int64
		)

		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}

		cloud = append(cloud, domain.WikiTagCount{Name: name, Count: int(count)})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return cloud, nil
}

// DeletePage удаляет страницу по заголовку (без учёта регистра).
// Связи с тегами удаляются каскадом; ссылки на удаляемую страницу
// вычищаются из массивов остальных страниц. Удаление несуществующей
// страницы не является ошибкой.
func (pg *PGClient) DeletePage(ctx context.Context, title string) error {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Вычищаем заголовок из suppliers и products всех остальных страниц.
	if _, err = tx.Exec(ctx, `
        UPDATE wiki_pages SET
            suppliers = ARRAY(SELECT x FROM unnest(suppliers) x WHERE lower(x) <> lower($1)),
            products  = ARRAY(SELECT x FROM unnest(products) x WHERE lower(x) <> lower($1))
        WHERE EXISTS (SELECT 1 FROM unnest(suppliers) x WHERE lower(x) = lower($1))
           OR EXISTS (SELECT 1 FROM unnest(products) x WHERE lower(x) = lower($1))
    `, title); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `
        DELETE FROM wiki_pages
        WHERE lower(title) = lower($1)
    `, title); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RemovePhoto удаляет фото страницы по заголовку (без учёта регистра).
func (pg *PGClient) RemovePhoto(ctx context.Context, title string) error {
	_, err := pg.Pool.Exec(ctx, `
        UPDATE wiki_pages
        SET photo = NULL, photo_type = '', photo_name = ''
        WHERE lower(title) = lower($1)
    `, title)

	return err
}

// ListIndex возвращает вики-страницы с фильтрами: query — подстрока
// в заголовке или содержимом (без учёта регистра), tags — все указанные
// теги (AND-семантика), typ — тип страницы. Результат отсортирован
// по заголовку без учёта регистра; теги страниц подтягиваются отдельным
// запросом.
func (pg *PGClient) ListIndex(ctx context.Context, query string, tags []string, typ domain.PageType) ([]domain.WikiIndexEntry, error) {
	var (
		where []string
		args  []any
	)

	if query != "" {
		args = append(args, escapeLike(query))
		where = append(where, fmt.Sprintf(`
            (p.title ILIKE '%%' || $%d || '%%' ESCAPE '\' OR
             p.content ILIKE '%%' || $%d || '%%' ESCAPE '\')`, len(args), len(args)))
	}

	if len(tags) > 0 {
		// Регистронезависимая фильтрация: и теги, и параметры — в нижнем
		// регистре; дубли параметров убираем (иначе сломается HAVING count).
		tagKeys := make([]string, 0, len(tags))
		seen := make(map[string]struct{}, len(tags))
		for _, t := range tags {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" {
				continue
			}
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			tagKeys = append(tagKeys, t)
		}
		if len(tagKeys) > 0 {
			args = append(args, tagKeys)
			where = append(where, fmt.Sprintf(`
            p.id IN (
                SELECT pt.page_id FROM wiki_page_tags pt
                JOIN wiki_tags t ON t.id = pt.tag_id
                WHERE lower(t.name) = ANY($%d::text[])
                GROUP BY pt.page_id
                HAVING count(*) = cardinality($%d::text[])
            )`, len(args), len(args)))
		}
	}

	if typ != "" {
		args = append(args, string(typ))
		where = append(where, fmt.Sprintf("p.page_type = $%d", len(args)))
	}

	sqlQuery := `SELECT p.id, p.page_type, p.title FROM wiki_pages p`
	if len(where) > 0 {
		sqlQuery += ` WHERE ` + strings.Join(where, ` AND `)
	}
	sqlQuery += ` ORDER BY lower(p.title)`

	rows, err := pg.Pool.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pageIDs := make([]int64, 0)
	entries := make([]domain.WikiIndexEntry, 0)

	for rows.Next() {
		var (
			id       int64
			pageType string
			title    string
		)

		if err := rows.Scan(&id, &pageType, &title); err != nil {
			return nil, err
		}

		pageIDs = append(pageIDs, id)
		entries = append(entries, domain.WikiIndexEntry{Type: domain.PageType(pageType), Title: title})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(pageIDs) == 0 {
		return entries, nil
	}

	// Теги страниц — отдельным запросом.
	tagRows, err := pg.Pool.Query(ctx, `
        SELECT pt.page_id, t.name
        FROM wiki_page_tags pt
        JOIN wiki_tags t ON t.id = pt.tag_id
        WHERE pt.page_id = ANY($1::bigint[])
        ORDER BY lower(t.name)
    `, pageIDs)
	if err != nil {
		return nil, err
	}
	defer tagRows.Close()

	tagsByPage := make(map[int64][]string)

	for tagRows.Next() {
		var (
			pageID int64
			name   string
		)

		if err := tagRows.Scan(&pageID, &name); err != nil {
			return nil, err
		}

		tagsByPage[pageID] = append(tagsByPage[pageID], name)
	}

	if err := tagRows.Err(); err != nil {
		return nil, err
	}

	for i := range entries {
		entries[i].Tags = tagsByPage[pageIDs[i]]
	}

	return entries, nil
}

// SavePage создаёт или обновляет вики-страницу в одной транзакции.
// currentTitle == "" — создание; иначе — обновление, в том числе
// переименование. При занятом заголовке возвращает domain.ErrTitleTaken.
// photo != nil — обновить фото страницы.
func (pg *PGClient) SavePage(ctx context.Context, page *domain.WikiPage, currentTitle string, photo *domain.PhotoUpload) error {
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	contactsJSON, err := json.Marshal(page.Contacts)
	if err != nil {
		return fmt.Errorf("marshal page contacts: %w", err)
	}
	if page.Contacts == nil {
		contactsJSON = []byte("[]")
	}

	orderDays := intsToInt16(page.OrderDays)
	deliveryDays := intsToInt16(page.DeliveryDays)

	suppliers := page.Suppliers
	if suppliers == nil {
		suppliers = []string{}
	}

	products := page.Products
	if products == nil {
		products = []string{}
	}

	if currentTitle == "" {
		// Создание новой страницы. ON CONFLICT DO NOTHING без целевого
		// индекса: единственный уникальный индекс — lower(title), поэтому
		// 0 затронутых строк означает занятый заголовок.
		if err := insertPage(ctx, tx, page, contactsJSON, orderDays, deliveryDays, suppliers, products); err != nil {
			return err
		}
	} else {
		// Обновление или переименование существующей страницы.
		if err := updatePage(ctx, tx, page, currentTitle, contactsJSON, orderDays, deliveryDays, suppliers, products); err != nil {
			return err
		}
	}

	if photo != nil {
		if err := setPagePhoto(ctx, tx, page.Title, photo); err != nil {
			return err
		}
	}

	// ID страницы для перезаписи связей с тегами.
	var pageID int64

	err = tx.QueryRow(ctx, `
        SELECT id FROM wiki_pages WHERE lower(title) = lower($1)
    `, page.Title).Scan(&pageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrPageNotFound
		}

		return err
	}

	if err := syncPageLinks(ctx, tx, page, currentTitle); err != nil {
		return err
	}

	if err := replacePageTags(ctx, tx, pageID, page.Tags); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// insertPage создаёт новую страницу; занятый заголовок → domain.ErrTitleTaken.
func insertPage(ctx context.Context, tx pgx.Tx, page *domain.WikiPage, contactsJSON []byte, orderDays, deliveryDays []int16, suppliers, products []string) error {
	tag, err := tx.Exec(ctx, `
        INSERT INTO wiki_pages (
            page_type, title, content, contacts, order_days, delivery_days,
            average_weight, suppliers, products
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT DO NOTHING
    `,
		string(page.Type), page.Title, page.Content, contactsJSON,
		orderDays, deliveryDays, page.AverageWeight, suppliers, products,
	)
	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		return domain.ErrTitleTaken
	}

	return nil
}

// updatePage обновляет страницу; занятый заголовок → domain.ErrTitleTaken,
// страница удалена конкурентно → domain.ErrPageNotFound.
func updatePage(ctx context.Context, tx pgx.Tx, page *domain.WikiPage, currentTitle string, contactsJSON []byte, orderDays, deliveryDays []int16, suppliers, products []string) error {
	tag, err := tx.Exec(ctx, `
        UPDATE wiki_pages SET
            page_type = $1, title = $2, content = $3, contacts = $4,
            order_days = $5, delivery_days = $6, average_weight = $7,
            suppliers = $8, products = $9
        WHERE lower(title) = lower($10)
    `,
		string(page.Type), page.Title, page.Content, contactsJSON,
		orderDays, deliveryDays, page.AverageWeight, suppliers, products, currentTitle,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrTitleTaken
		}

		return err
	}

	// Гонка: страница удалена между проверкой в usecase и UPDATE.
	// Не продолжаем — иначе SELECT id ниже попадёт на чужую страницу
	// и перезапишет её теги.
	if tag.RowsAffected() == 0 {
		return domain.ErrPageNotFound
	}

	return nil
}

// setPagePhoto записывает фото страницы (при photo != nil).
func setPagePhoto(ctx context.Context, tx pgx.Tx, title string, photo *domain.PhotoUpload) error {
	if _, err := tx.Exec(ctx, `
        UPDATE wiki_pages
        SET photo = $2, photo_type = $3, photo_name = ''
        WHERE lower(title) = lower($1)
    `, title, photo.Data, photo.ContentType); err != nil {
		return err
	}

	return nil
}

// syncPageLinks поддерживает инвариант «поставщик ⇄ продукт»: при
// переименовании заменяет старый заголовок в массивах остальных страниц,
// затем зеркально синхронизирует suppliers/products с выбранным списком.
// Сама сохраняемая страница не задевается: условия фильтруют по
// page_type противоположного типа, а lower(title) уникален по таблице.
func syncPageLinks(ctx context.Context, tx pgx.Tx, page *domain.WikiPage, currentTitle string) error {
	// Переименование: старый заголовок заменяется новым в массивах
	// остальных страниц — иначе ссылки после переименования гниют.
	if currentTitle != "" && !strings.EqualFold(currentTitle, page.Title) {
		if _, err := tx.Exec(ctx, `
            UPDATE wiki_pages SET
                suppliers = ARRAY(SELECT CASE WHEN lower(x) = lower($1) THEN $2 ELSE x END
                                  FROM unnest(suppliers) x)
            WHERE EXISTS (SELECT 1 FROM unnest(suppliers) x WHERE lower(x) = lower($1))
        `, currentTitle, page.Title); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
            UPDATE wiki_pages SET
                products = ARRAY(SELECT CASE WHEN lower(x) = lower($1) THEN $2 ELSE x END
                                 FROM unnest(products) x)
            WHERE EXISTS (SELECT 1 FROM unnest(products) x WHERE lower(x) = lower($1))
        `, currentTitle, page.Title); err != nil {
			return err
		}
	}

	if page.Type == domain.PageTypeProduct {
		// Добавление: продукт в products каждого выбранного поставщика.
		if _, err := tx.Exec(ctx, `
            UPDATE wiki_pages SET products = products || ARRAY[$1]
            WHERE page_type = 'supplier'
              AND lower(title) = ANY(COALESCE($2::text[], '{}'))
              AND NOT EXISTS (SELECT 1 FROM unnest(products) x WHERE lower(x) = lower($1))
        `, page.Title, lowerStrings(page.Suppliers)); err != nil {
			return err
		}

		// Удаление: продукт из products всех поставщиков, не выбранных.
		// COALESCE: пустой выбор (nil -> NULL у pgx) должен удалять
		// из ВСЕХ, а не молча не срабатывать (ANY(NULL) = NULL).
		if _, err := tx.Exec(ctx, `
            UPDATE wiki_pages SET
                products = ARRAY(SELECT x FROM unnest(products) x WHERE lower(x) <> lower($1))
            WHERE page_type = 'supplier'
              AND NOT (lower(title) = ANY(COALESCE($2::text[], '{}')))
              AND EXISTS (SELECT 1 FROM unnest(products) x WHERE lower(x) = lower($1))
        `, page.Title, lowerStrings(page.Suppliers)); err != nil {
			return err
		}
	} else {
		// Зеркально: поставщик в suppliers каждого выбранного продукта.
		if _, err := tx.Exec(ctx, `
            UPDATE wiki_pages SET suppliers = suppliers || ARRAY[$1]
            WHERE page_type = 'product'
              AND lower(title) = ANY(COALESCE($2::text[], '{}'))
              AND NOT EXISTS (SELECT 1 FROM unnest(suppliers) x WHERE lower(x) = lower($1))
        `, page.Title, lowerStrings(page.Products)); err != nil {
			return err
		}

		// Удаление: поставщик из suppliers всех продуктов, не выбранных.
		if _, err := tx.Exec(ctx, `
            UPDATE wiki_pages SET
                suppliers = ARRAY(SELECT x FROM unnest(suppliers) x WHERE lower(x) <> lower($1))
            WHERE page_type = 'product'
              AND NOT (lower(title) = ANY(COALESCE($2::text[], '{}')))
              AND EXISTS (SELECT 1 FROM unnest(suppliers) x WHERE lower(x) = lower($1))
        `, page.Title, lowerStrings(page.Products)); err != nil {
			return err
		}
	}

	return nil
}

// replacePageTags пересоздаёт связи страницы с тегами и подчищает
// осиротевшие теги.
func replacePageTags(ctx context.Context, tx pgx.Tx, pageID int64, pageTags []string) error {
	tags := pageTags
	if tags == nil {
		tags = []string{}
	}

	if _, err := tx.Exec(ctx, `
        DELETE FROM wiki_page_tags WHERE page_id = $1
    `, pageID); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
        INSERT INTO wiki_tags (name)
        SELECT n FROM unnest($1::text[]) AS n
        ON CONFLICT DO NOTHING
    `, tags); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
        INSERT INTO wiki_page_tags (page_id, tag_id)
        SELECT $1, t.id
        FROM unnest($2::text[]) AS n(name)
        JOIN wiki_tags t ON lower(t.name) = lower(n.name)
    `, pageID, tags); err != nil {
		return err
	}

	// Подчистка осиротевших тегов (не связанных ни с одной страницей).
	if _, err := tx.Exec(ctx, `
        DELETE FROM wiki_tags
        WHERE id NOT IN (SELECT tag_id FROM wiki_page_tags)
    `); err != nil {
		return err
	}

	return nil
}

// GetPageBySupplierID возвращает страницу поставщика по привязке
// к справочнику (wiki_pages.supplier_id). Если страницы нет — (nil, nil).
// Теги не подгружаются: для синка нужны только данные страницы.
func (pg *PGClient) GetPageBySupplierID(ctx context.Context, supplierID string) (*domain.WikiPage, error) {
	row := pg.Pool.QueryRow(ctx, `
        SELECT `+wikiPageColumns+`
        FROM wiki_pages
        WHERE supplier_id = $1
    `, supplierID)

	return scanWikiPage(row)
}

// GetUnlinkedSupplierPageByTitle ищет страницу поставщика без привязки
// к справочнику (создана вручную, supplier_id IS NULL). Если нет — (nil, nil).
func (pg *PGClient) GetUnlinkedSupplierPageByTitle(ctx context.Context, title string) (*domain.WikiPage, error) {
	row := pg.Pool.QueryRow(ctx, `
        SELECT `+wikiPageColumns+`
        FROM wiki_pages
        WHERE page_type = 'supplier' AND supplier_id IS NULL AND lower(title) = lower($1)
    `, title)

	return scanWikiPage(row)
}

// CreateSupplierPage создаёт страницу поставщика с привязкой к справочнику.
// Занятый заголовок (или уже существующая привязка) → domain.ErrTitleTaken.
func (pg *PGClient) CreateSupplierPage(ctx context.Context, page *domain.WikiPage) error {
	tag, err := pg.Pool.Exec(ctx, `
        INSERT INTO wiki_pages (page_type, title, order_days, delivery_days, supplier_id)
        VALUES ('supplier', $1, $2, $3, $4)
        ON CONFLICT DO NOTHING
    `, page.Title, intsToInt16(page.OrderDays), intsToInt16(page.DeliveryDays), page.SupplierID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTitleTaken
	}

	return nil
}

// UpdateSupplierPage обновляет данные страницы поставщика из справочника:
// заголовок (= name), дни заказа/доставки и привязку supplier_id (для
// непривязанной вручную созданной страницы — claim). Занятый заголовок →
// domain.ErrTitleTaken; страница удалена конкурентно → domain.ErrPageNotFound.
func (pg *PGClient) UpdateSupplierPage(ctx context.Context, pageID int64, supplierID, title string, orderDays, deliveryDays []int) error {
	tag, err := pg.Pool.Exec(ctx, `
        UPDATE wiki_pages SET
            title = $1, order_days = $2, delivery_days = $3, supplier_id = $4
        WHERE id = $5
    `, title, intsToInt16(orderDays), intsToInt16(deliveryDays), supplierID, pageID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrTitleTaken
		}

		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPageNotFound
	}

	return nil
}

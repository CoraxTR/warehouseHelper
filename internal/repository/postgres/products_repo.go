package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"warehouseHelper/internal/domain"
)

// productColumns — колонки products в порядке SELECT/INSERT (без id в INSERT
// не обойтись, но сканирование идёт в этом порядке).
const productColumns = `id, internal_code, name, uom, group_name, average_weight,
    shelf_life, pack_size, inventory_type, short_list, track_weekly`

// scanProduct сканирует строку в domain.Product (порядок productColumns).
func scanProduct(row pgx.Row) (*domain.Product, error) {
	var p domain.Product
	if err := row.Scan(
		&p.ID, &p.InternalCode, &p.Name, &p.UOM, &p.GroupName,
		&p.AverageWeight, &p.ShelfLife, &p.PackSize,
		&p.InventoryType, &p.ShortList, &p.TrackWeekly,
	); err != nil {
		return nil, err
	}

	return &p, nil
}

// UpsertProduct создаёт или обновляет товар каталога (upsert по id).
// Дубль internal_code (уникальный индекс, занят другим товаром) →
// domain.ErrInternalCodeTaken.
func (pg *PGClient) UpsertProduct(ctx context.Context, p *domain.Product) error {
	_, err := pg.Pool.Exec(ctx, `
        INSERT INTO products (
            id, internal_code, name, uom, group_name, average_weight,
            shelf_life, pack_size, inventory_type, short_list, track_weekly
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
        ON CONFLICT (id) DO UPDATE SET
            internal_code = EXCLUDED.internal_code,
            name          = EXCLUDED.name,
            uom           = EXCLUDED.uom,
            group_name    = EXCLUDED.group_name,
            average_weight = EXCLUDED.average_weight,
            shelf_life    = EXCLUDED.shelf_life,
            pack_size     = EXCLUDED.pack_size,
            inventory_type = EXCLUDED.inventory_type,
            short_list    = EXCLUDED.short_list,
            track_weekly  = EXCLUDED.track_weekly
    `,
		p.ID, p.InternalCode, p.Name, p.UOM, p.GroupName, p.AverageWeight,
		p.ShelfLife, p.PackSize, p.InventoryType, p.ShortList, p.TrackWeekly,
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

	var products []domain.Product
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

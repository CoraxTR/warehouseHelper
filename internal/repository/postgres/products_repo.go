package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"warehouseHelper/internal/domain"
)

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

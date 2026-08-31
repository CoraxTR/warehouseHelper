package postgres

import (
	"context"
	"fmt"
)

// TableSizes возвращает размеры таблиц кластера в байтах (данные + индексы
// + TOAST через pg_total_relation_size). Ключ — "schema.table". Используется
// фоновым опросом метрик приложения (internal/metrics.SetTableSizes).
func (pg *PGClient) TableSizes(ctx context.Context) (map[string]int64, error) {
	rows, err := pg.Pool.Query(ctx, `
		SELECT schemaname, tablename,
		       pg_total_relation_size(quote_ident(schemaname) || '.' || quote_ident(tablename)) AS size_bytes
		FROM pg_tables
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`)
	if err != nil {
		return nil, fmt.Errorf("query table sizes: %w", err)
	}
	defer rows.Close()

	sizes := make(map[string]int64)
	for rows.Next() {
		var schema, table string
		var size int64
		if err := rows.Scan(&schema, &table, &size); err != nil {
			return nil, fmt.Errorf("scan table size: %w", err)
		}
		sizes[schema+"."+table] = size
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table sizes: %w", err)
	}
	return sizes, nil
}

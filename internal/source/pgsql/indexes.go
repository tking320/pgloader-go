package pgsql

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// Indexes
// ---------------------------------------------------------------------------

func (s *PgSQLSource) fetchTableIndexes(ctx context.Context, conn *pgxpool.Conn, tableOID uint32, tableName string) ([]*catalog.Index, error) {
	rows, err := conn.Query(ctx, `
		SELECT ci.relname AS index_name,
		       i.indisprimary,
		       i.indisunique,
		       am.amname AS index_type,
		       pg_catalog.pg_get_expr(i.indpred, i.indrelid) AS filter,
		       pg_catalog.pg_get_indexdef(i.indexrelid) AS indexdef
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class ct ON ct.oid = i.indrelid
		JOIN pg_catalog.pg_class ci ON ci.oid = i.indexrelid
		JOIN pg_catalog.pg_am am ON am.oid = ci.relam
		WHERE i.indrelid = $1
		ORDER BY i.indisprimary DESC, ci.relname
	`, tableOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexes []*catalog.Index
	for rows.Next() {
		var idxName string
		var isPrimary, isUnique bool
		var idxType string
		var filter, indexdef *string
		if err := rows.Scan(&idxName, &isPrimary, &isUnique, &idxType, &filter, &indexdef); err != nil {
			return nil, err
		}

		var indexdefStr string
		if indexdef != nil {
			indexdefStr = *indexdef
		}
		idx := &catalog.Index{
			Name:    idxName,
			Table:   tableName,
			Primary: isPrimary,
			Unique:  isUnique,
			Type:    idxType,
			Schema:  s.srcSchema,
			SQL:     indexdefStr,
		}
		if filter != nil && *filter != "" {
			idx.Filter = *filter
		}
		indexes = append(indexes, idx)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, idx := range indexes {
		cols, err := s.fetchIndexColumns(ctx, conn, tableOID, idx.Name)
		if err != nil {
			return nil, fmt.Errorf("index columns for %s: %w", idx.Name, err)
		}
		idx.Columns = cols
	}

	return indexes, nil
}

func (s *PgSQLSource) fetchIndexColumns(ctx context.Context, conn *pgxpool.Conn, tableOID uint32, indexName string) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT a.attname
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class ci ON ci.oid = i.indexrelid
		CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(key, ord)
		JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.key
		WHERE i.indrelid = $1
		  AND ci.relname = $2
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		ORDER BY k.ord
	`, tableOID, indexName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

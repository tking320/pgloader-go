package pgsql

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// Partition info
// ---------------------------------------------------------------------------

func (s *PgSQLSource) fetchPartitionInfo(ctx context.Context, conn *pgxpool.Conn, tableOID uint32) (*catalog.PartitionInfo, error) {
	// pg_get_partkeydef returns "RANGE (logdate)" or "HASH (id)"
	row := conn.QueryRow(ctx, `
		SELECT pg_catalog.pg_get_partkeydef($1)
	`, tableOID)

	var partKeyDef string
	if err := row.Scan(&partKeyDef); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	// Parse "STRATEGY (col1, col2, ...)"
	parenIdx := strings.IndexByte(partKeyDef, '(')
	if parenIdx < 0 {
		return nil, fmt.Errorf("unexpected partkey format: %s", partKeyDef)
	}

	strategy := strings.TrimSpace(partKeyDef[:parenIdx])
	keyPart := strings.TrimRight(partKeyDef[parenIdx:], " )")
	keyPart = strings.TrimLeft(keyPart, " (")

	// Split keys by comma, respecting parentheses
	var keys []string
	depth := 0
	start := 0
	for i, ch := range keyPart + "," {
		switch {
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case ch == ',':
			if depth == 0 {
				k := strings.TrimSpace(keyPart[start:i])
				if k != "" {
					keys = append(keys, k)
				}
				start = i + 1
			}
		}
	}

	// Separate column names from expressions
	var colNames, colExprs []string
	for _, k := range keys {
		isExpr := strings.ContainsAny(k, "() \t\n")
		if isExpr {
			colExprs = append(colExprs, k)
			colNames = append(colNames, "")
		} else {
			colNames = append(colNames, k)
			colExprs = append(colExprs, "")
		}
	}

	return &catalog.PartitionInfo{
		Strategy:       strategy,
		KeyColumns:     colNames,
		KeyExpressions: colExprs,
	}, nil
}

// fetchPartitionChildren discovers child partitions for a partitioned table.
func (s *PgSQLSource) fetchPartitionChildren(ctx context.Context, conn *pgxpool.Conn, parentOID uint32) ([]catalog.PartitionChild, error) {
	rows, err := conn.Query(ctx, `
		SELECT c.relname,
		       pg_catalog.pg_get_expr(c.relpartbound, c.oid) AS bound
		FROM pg_catalog.pg_inherits i
		JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = $1
		ORDER BY c.relname
	`, parentOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var children []catalog.PartitionChild
	for rows.Next() {
		var name, bound string
		if err := rows.Scan(&name, &bound); err != nil {
			return nil, err
		}
		children = append(children, catalog.PartitionChild{Name: name, Bound: bound})
	}
	return children, rows.Err()
}

func (s *PgSQLSource) fetchParentTable(ctx context.Context, conn *pgxpool.Conn, tableOID uint32) (string, error) {
	row := conn.QueryRow(ctx, `
		SELECT c.relname
		FROM pg_catalog.pg_inherits i
		JOIN pg_catalog.pg_class c ON c.oid = i.inhparent
		WHERE i.inhrelid = $1
	`, tableOID)

	var parent string
	if err := row.Scan(&parent); err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return parent, nil
}

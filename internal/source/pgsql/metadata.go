package pgsql

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// FetchMetadata — full schema introspection
// ---------------------------------------------------------------------------

// FetchMetadata reads the source PostgreSQL schema into the catalog.
func (s *PgSQLSource) FetchMetadata(ctx context.Context) error {
	if s.srcPool == nil {
		return fmt.Errorf("not connected: call Connect first")
	}

	s.catalog = &catalog.Catalog{}
	s.schema_ = &catalog.Schema{Name: s.srcSchema, Catalog: s.catalog}
	s.catalog.Schemas = append(s.catalog.Schemas, s.schema_)

	conn, err := s.srcPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire source connection: %w", err)
	}
	defer conn.Release()

	// 1. Extensions
	if err := s.discoverExtensions(ctx, conn); err != nil {
		return fmt.Errorf("discover extensions: %w", err)
	}

	// 2. Custom types (enums, domains, composites)
	if err := s.discoverCustomTypes(ctx, conn); err != nil {
		return fmt.Errorf("discover custom types: %w", err)
	}

	// 3. User-defined functions (for triggers)
	if err := s.fetchFunctions(ctx, conn); err != nil {
		return fmt.Errorf("discover functions: %w", err)
	}

	if s.table != "" {
		// Single table mode
		table, err := s.fetchTableMetadata(ctx, conn, s.table)
		if err != nil {
			return err
		}
		s.schema_.Tables = append(s.schema_.Tables, table)
	} else {
		// Full schema mode — discover all tables
		tables, err := s.discoverTables(ctx, conn)
		if err != nil {
			return err
		}
		for _, tbl := range tables {
			table, err := s.fetchTableMetadata(ctx, conn, tbl)
			if err != nil {
				return fmt.Errorf("fetch table %s: %w", tbl, err)
			}
			s.schema_.Tables = append(s.schema_.Tables, table)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Extension discovery
// ---------------------------------------------------------------------------

func (s *PgSQLSource) discoverExtensions(ctx context.Context, conn *pgxpool.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT e.extname, COALESCE(n.nspname, 'public')
		FROM pg_catalog.pg_extension e
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
		ORDER BY e.extname
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, schema string
		if err := rows.Scan(&name, &schema); err != nil {
			return err
		}
		s.schema_.Extensions = append(s.schema_.Extensions, &catalog.Extension{
			Name:   name,
			Schema: schema,
		})
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// Custom type discovery (enums, domains, composites)
// ---------------------------------------------------------------------------

func (s *PgSQLSource) discoverCustomTypes(ctx context.Context, conn *pgxpool.Conn) error {
	// Enums
	if err := s.discoverEnums(ctx, conn); err != nil {
		return err
	}
	// Domains
	if err := s.discoverDomains(ctx, conn); err != nil {
		return err
	}
	// Composites
	if err := s.discoverComposites(ctx, conn); err != nil {
		return err
	}
	return nil
}

func (s *PgSQLSource) discoverEnums(ctx context.Context, conn *pgxpool.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT t.typname,
		       COALESCE(n.nspname, 'public'),
		       array_agg(e.enumlabel ORDER BY e.enumsortorder) AS labels
		FROM pg_catalog.pg_type t
		JOIN pg_catalog.pg_enum e ON t.oid = e.enumtypid
		JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = $1
		GROUP BY t.typname, n.nspname
		ORDER BY t.typname
	`, s.srcSchema)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, schema string
		var labels []string
		if err := rows.Scan(&name, &schema, &labels); err != nil {
			return err
		}
		s.schema_.Types = append(s.schema_.Types, &catalog.SQLType{
			Name:     name,
			Schema:   schema,
			Type:     "enum",
			Elements: labels,
		})
	}
	return rows.Err()
}

func (s *PgSQLSource) discoverDomains(ctx context.Context, conn *pgxpool.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT t.typname,
		       COALESCE(n.nspname, 'public'),
		       pg_catalog.format_type(t.typbasetype, t.typtypmod) AS base_type
		FROM pg_catalog.pg_type t
		JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
		WHERE t.typtype = 'd'
		  AND n.nspname = $1
		ORDER BY t.typname
	`, s.srcSchema)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, schema, baseType string
		if err := rows.Scan(&name, &schema, &baseType); err != nil {
			return err
		}
		s.schema_.Types = append(s.schema_.Types, &catalog.SQLType{
			Name:     name,
			Schema:   schema,
			Type:     "domain",
			BaseType: baseType,
		})
	}
	return rows.Err()
}

// discoverComposites — two-pass to avoid "conn busy" from nested result sets
func (s *PgSQLSource) discoverComposites(ctx context.Context, conn *pgxpool.Conn) error {
	type compInfo struct {
		name   string
		schema string
	}
	var composites []compInfo

	rows, err := conn.Query(ctx, `
		SELECT t.typname,
		       COALESCE(n.nspname, 'public')
		FROM pg_catalog.pg_type t
		JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
		WHERE t.typtype = 'c'
		  AND n.nspname = $1
		  AND t.typrelid IN (SELECT oid FROM pg_catalog.pg_class WHERE relkind = 'c')
		ORDER BY t.typname
	`, s.srcSchema)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c compInfo
		if err := rows.Scan(&c.name, &c.schema); err != nil {
			rows.Close()
			return err
		}
		composites = append(composites, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range composites {
		attrRows, err := conn.Query(ctx, `
			SELECT a.attname,
			       pg_catalog.format_type(a.atttypid, a.atttypmod) AS attr_type
			FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
			WHERE c.reltype = (
			      SELECT t.oid FROM pg_catalog.pg_type t
			      WHERE t.typname = $1 AND t.typnamespace = (
			          SELECT n2.oid FROM pg_catalog.pg_namespace n2 WHERE n2.nspname = $2
			      ))
			  AND a.attnum > 0
			  AND NOT a.attisdropped
			ORDER BY a.attnum
		`, c.name, c.schema)
		if err != nil {
			return err
		}

		var attrs []catalog.CompositeAttr
		for attrRows.Next() {
			var attrName, attrType string
			if err := attrRows.Scan(&attrName, &attrType); err != nil {
				attrRows.Close()
				return err
			}
			attrs = append(attrs, catalog.CompositeAttr{Name: attrName, TypeName: attrType})
		}
		attrRows.Close()
		if err := attrRows.Err(); err != nil {
			return err
		}

		s.schema_.Types = append(s.schema_.Types, &catalog.SQLType{
			Name:     c.name,
			Schema:   c.schema,
			Type:     "composite",
			AttrDefs: attrs,
		})
	}
	return nil
}

// ---------------------------------------------------------------------------
// Functions discovery
// ---------------------------------------------------------------------------

// fetchFunctions discovers user-defined functions needed for triggers.
func (s *PgSQLSource) fetchFunctions(ctx context.Context, conn *pgxpool.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT p.proname,
		       n.nspname,
		       pg_catalog.pg_get_functiondef(p.oid) AS funcdef
		FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		LEFT JOIN pg_catalog.pg_depend d ON d.objid = p.oid AND d.deptype = 'e'
		WHERE n.nspname = $1
		  AND p.prokind = 'f'
		  AND d.objid IS NULL  -- exclude extension-owned functions
		ORDER BY p.proname
	`, s.srcSchema)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, schema, def string
		if err := rows.Scan(&name, &schema, &def); err != nil {
			return err
		}
		s.schema_.Functions = append(s.schema_.Functions, &catalog.Function{
			Name:       name,
			Schema:     schema,
			Definition: def,
		})
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// Table discovery
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------

func (s *PgSQLSource) discoverTables(ctx context.Context, conn *pgxpool.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT c.relname
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relkind IN ('r', 'p')
		  AND NOT c.relispartition
		ORDER BY c.relname
	`, s.srcSchema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// ---------------------------------------------------------------------------
// Single table metadata
// ---------------------------------------------------------------------------

func (s *PgSQLSource) fetchTableMetadata(ctx context.Context, conn *pgxpool.Conn, tableName string) (*catalog.Table, error) {
	t := &catalog.Table{
		Name:   tableName,
		Schema: s.schema_,
	}

	// Get table OID, row estimate, comment, storage params
	// Note: relkind is type "char" in PG, cast to text for pgx scan
	row := conn.QueryRow(ctx, `
		SELECT c.oid,
		       c.reltuples::bigint AS row_est,
		       COALESCE(d.description, ''),
		       c.reloptions,
		       c.relispartition,
		       c.relkind::text
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_description d ON d.objoid = c.oid AND d.objsubid = 0
		WHERE n.nspname = $1 AND c.relname = $2
	`, s.srcSchema, tableName)

	var oid uint32
	var rowEst int64
	var comment string
	var relOptions []string
	var relIsPartition bool
	var relKind string

	if err := row.Scan(&oid, &rowEst, &comment, &relOptions, &relIsPartition, &relKind); err != nil {
		return nil, fmt.Errorf("table %s: %w", tableName, err)
	}

	t.OID = oid
	t.RowCountEstimate = rowEst
	t.Comment = comment
	if len(relOptions) > 0 {
		t.StorageParams = make(map[string]string)
		for _, opt := range relOptions {
			if parts := strings.SplitN(opt, "=", 2); len(parts) == 2 {
				t.StorageParams[parts[0]] = parts[1]
			}
		}
	}

	// Partition info
	if relKind == "p" {
		pi, err := s.fetchPartitionInfo(ctx, conn, oid)
		if err != nil {
			return nil, fmt.Errorf("partition info %s: %w", tableName, err)
		}
		if pi != nil {
			children, err := s.fetchPartitionChildren(ctx, conn, oid)
			if err != nil {
				return nil, fmt.Errorf("partition children %s: %w", tableName, err)
			}
			pi.Partitions = children
		}
		t.PartitionInfo = pi
	}

	// Inheritance (parent table)
	if relIsPartition {
		parent, err := s.fetchParentTable(ctx, conn, oid)
		if err != nil {
			return nil, fmt.Errorf("parent table %s: %w", tableName, err)
		}
		t.ParentTable = parent
	}

	// Columns
	columns, err := s.fetchTableColumns(ctx, conn, oid)
	if err != nil {
		return nil, fmt.Errorf("columns %s: %w", tableName, err)
	}
	t.Columns = columns

	// Indexes
	indexes, err := s.fetchTableIndexes(ctx, conn, oid, tableName)
	if err != nil {
		return nil, fmt.Errorf("indexes %s: %w", tableName, err)
	}
	t.Indexes = indexes

	// Foreign keys
	fkeys, err := s.fetchTableForeignKeys(ctx, conn, oid, tableName)
	if err != nil {
		return nil, fmt.Errorf("foreign keys %s: %w", tableName, err)
	}
	t.ForeignKeys = fkeys

	// Triggers
	triggers, err := s.fetchTableTriggers(ctx, conn, oid)
	if err != nil {
		return nil, fmt.Errorf("triggers %s: %w", tableName, err)
	}
	t.Triggers = triggers

	// Sequence names for auto-increment columns
	if err := s.fetchSequenceNames(ctx, conn, t); err != nil {
		return nil, fmt.Errorf("sequence names %s: %w", tableName, err)
	}

	return t, nil
}

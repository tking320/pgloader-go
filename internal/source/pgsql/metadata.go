package pgsql

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/catalog"
	"github.com/tking320/pgloader-go/internal/source"
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

// ---------------------------------------------------------------------------
// Columns
// ---------------------------------------------------------------------------

func (s *PgSQLSource) fetchTableColumns(ctx context.Context, conn *pgxpool.Conn, tableOID uint32) ([]*catalog.Column, error) {
	// Get PK columns first (separate query to avoid "conn busy")
	pkCols, err := s.fetchPKColumns(ctx, conn, tableOID)
	if err != nil {
		return nil, err
	}
	pkSet := make(map[string]bool)
	for _, pk := range pkCols {
		pkSet[pk] = true
	}

	rows, err := conn.Query(ctx, `
		SELECT a.attname,
		       pg_catalog.format_type(a.atttypid, a.atttypmod) AS full_type,
		       a.attnotnull,
		       pg_catalog.pg_get_expr(ad.adbin, ad.adrelid) AS default_expr,
		       a.attidentity::text,
		       COALESCE(d.description, '')
		FROM pg_catalog.pg_attribute a
		LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		LEFT JOIN pg_catalog.pg_description d ON d.objoid = a.attrelid AND d.objsubid = a.attnum
		WHERE a.attrelid = $1
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		ORDER BY a.attnum
	`, tableOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []*catalog.Column
	for rows.Next() {
		var name, fullType string
		var notNull bool
		var defaultExpr, attIdentity, comment *string
		if err := rows.Scan(&name, &fullType, &notNull, &defaultExpr, &attIdentity, &comment); err != nil {
			return nil, err
		}

		col := &catalog.Column{
			Name:       name,
			SourceType: fullType,
			IsPK:       pkSet[name],
		}

		col.TypeName = fullType
		col.TypeMod = ""

		if idx := strings.IndexByte(fullType, '('); idx > 0 {
			col.TypeName = fullType[:idx]
			col.TypeMod = fullType[idx:]
		}

		if notNull {
			col.Nullable = false
		} else {
			col.Nullable = true
		}

		if defaultExpr != nil && *defaultExpr != "" {
			col.Default = *defaultExpr
		}

		if attIdentity != nil && *attIdentity != "" {
			col.IsAutoInc = true
			if *attIdentity == "a" {
				col.Extra = "GENERATED ALWAYS AS IDENTITY"
			} else if *attIdentity == "d" {
				col.Extra = "GENERATED BY DEFAULT AS IDENTITY"
			}
		} else if strings.Contains(col.Default, "nextval(") {
			col.IsAutoInc = true
		}

		if comment != nil {
			col.Comment = *comment
		}

		// Apply CAST rules
		if s.castEngine != nil {
			baseType := parseBaseType(fullType)
			result := s.castEngine.Apply(baseType, fullType, col.Extra)
			col.TypeName = result.TargetType
			if result.DropTypemod {
				col.TypeMod = ""
			}
			col.Transform = result.Transform
		}

		columns = append(columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return columns, nil
}

func (s *PgSQLSource) fetchPKColumns(ctx context.Context, conn *pgxpool.Conn, tableOID uint32) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT a.attname
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid
		     AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1 AND i.indisprimary
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum
	`, tableOID)
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

// fetchSequenceNames resolves sequence names for auto-increment columns.
func (s *PgSQLSource) fetchSequenceNames(ctx context.Context, conn *pgxpool.Conn, t *catalog.Table) error {
	for _, col := range t.Columns {
		if col.IsAutoInc {
			row := conn.QueryRow(ctx, `
				SELECT pg_catalog.pg_get_serial_sequence($1, $2)
			`, t.QualifiedName(), col.Name)
			var seqName *string
			if err := row.Scan(&seqName); err == nil && seqName != nil {
				// pg_get_serial_sequence returns "schema.seqname" — strip schema prefix
				if parts := strings.Split(*seqName, "."); len(parts) > 1 {
					col.SequenceName = parts[len(parts)-1]
				} else {
					col.SequenceName = *seqName
				}
			}
		}
	}
	return nil
}

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
			SQL:      indexdefStr,
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

func (s *PgSQLSource) fetchTableForeignKeys(ctx context.Context, conn *pgxpool.Conn, tableOID uint32, tableName string) ([]*catalog.ForeignKey, error) {
	rows, err := conn.Query(ctx, `
		SELECT con.conname,
		       con.confupdtype::text,
		       con.confdeltype::text,
		       con.confmatchtype::text,
		       con.condeferrable,
		       con.condeferred,
		       refn.nspname || '.' || refc.relname AS foreign_table,
		       array_agg(att.attname ORDER BY u.ord) AS fk_cols,
		       array_agg(refa.attname ORDER BY u.ord) AS ref_cols
		FROM pg_catalog.pg_constraint con
		CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS u(fk_attnum, ref_attnum, ord)
		JOIN pg_catalog.pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = u.fk_attnum
		JOIN pg_catalog.pg_attribute refa ON refa.attrelid = con.confrelid AND refa.attnum = u.ref_attnum
		JOIN pg_catalog.pg_class refc ON refc.oid = con.confrelid
		JOIN pg_catalog.pg_namespace refn ON refn.oid = refc.relnamespace
		WHERE con.conrelid = $1 AND con.contype = 'f'
		GROUP BY con.conname, con.confupdtype::text, con.confdeltype::text, con.confmatchtype::text,
		         con.condeferrable, con.condeferred, refn.nspname, refc.relname
		ORDER BY con.conname
	`, tableOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ruleMap := map[string]string{
		"a": "NO ACTION",
		"r": "RESTRICT",
		"c": "CASCADE",
		"n": "SET NULL",
		"d": "SET DEFAULT",
	}
	matchMap := map[string]string{
		"s": "SIMPLE",
		"f": "FULL",
		"p": "PARTIAL",
	}

	var fkeys []*catalog.ForeignKey
	for rows.Next() {
		var name, updRule, delRule, matchRule string
		var deferrable, deferred bool
		var foreignTable string
		var fkCols, refCols []string
		if err := rows.Scan(&name, &updRule, &delRule, &matchRule,
			&deferrable, &deferred, &foreignTable, &fkCols, &refCols); err != nil {
			return nil, err
		}

		fk := &catalog.ForeignKey{
			Name:              name,
			TableName:         tableName,
			Columns:           fkCols,
			ForeignTable:      foreignTable,
			ForeignColumns:    refCols,
			UpdateRule:        ruleMap[updRule],
			DeleteRule:        ruleMap[delRule],
			MatchRule:         matchMap[matchRule],
			Deferrable:        deferrable,
			InitiallyDeferred: deferred,
		}
		fkeys = append(fkeys, fk)
	}
	return fkeys, rows.Err()
}


func (s *PgSQLSource) fetchTableTriggers(ctx context.Context, conn *pgxpool.Conn, tableOID uint32) ([]*catalog.Trigger, error) {
	rows, err := conn.Query(ctx, `
		SELECT tgname,
		       CASE tgenabled::text
		       WHEN 'O' THEN 'ENABLED'
		       WHEN 'D' THEN 'DISABLED'
		       ELSE 'UNKNOWN'
		       END,
		       pg_catalog.pg_get_triggerdef(t.oid) AS trigger_def
		FROM pg_catalog.pg_trigger t
		WHERE t.tgrelid = $1 AND NOT t.tgisinternal
		ORDER BY tgname
	`, tableOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var triggers []*catalog.Trigger
	for rows.Next() {
		var name, status, def string
		if err := rows.Scan(&name, &status, &def); err != nil {
			return nil, err
		}

		timing, events := parseTriggerDef(def)

		triggers = append(triggers, &catalog.Trigger{
			Name:   name,
			Timing: timing,
			Events: events,
			Action: def,
		})
	}
	return triggers, rows.Err()
}

// parseTriggerDef extracts timing and events from pg_get_triggerdef output.
func parseTriggerDef(def string) (timing string, events string) {
	parts := strings.Fields(def)
	for i, p := range parts {
		upper := strings.ToUpper(p)
		var eventStart int
		if upper == "BEFORE" || upper == "AFTER" {
			timing = upper
			eventStart = i + 1
		} else if upper == "INSTEAD" && i+1 < len(parts) && strings.ToUpper(parts[i+1]) == "OF" {
			timing = "INSTEAD OF"
			eventStart = i + 2
		} else {
			continue
		}
		var evts []string
		for j := eventStart; j < len(parts); j++ {
			u := strings.ToUpper(parts[j])
			if u == "ON" {
				break
			}
			if u != "OR" {
				evts = append(evts, u)
			}
		}
		events = strings.Join(evts, ", ")
		break
	}
	return timing, events
}

// parseBaseType extracts the base type name from a PostgreSQL format_type output.
func parseBaseType(fullType string) string {
	if idx := strings.IndexByte(fullType, '('); idx > 0 {
		return strings.TrimSpace(fullType[:idx])
	}
	return fullType
}

// MapRows — reads data via COPY ... TO STDOUT
func (s *PgSQLSource) MapRows(ctx context.Context, processRow func(source.Row) error) error {
	if s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return fmt.Errorf("no table metadata: call FetchMetadata first")
	}

	t := s.ActiveTable()
	if t == nil {
		return fmt.Errorf("no active table")
	}

	conn, err := s.srcPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire source connection: %w", err)
	}
	defer conn.Release()

	colNames := make([]string, len(t.Columns))
	for i, col := range t.Columns {
			if col.Transform == "money-to-numeric" {
				colNames[i] = fmt.Sprintf("%s::numeric AS %s", quoteIdent(col.Name), quoteIdent(col.Name))
			} else {
				colNames[i] = quoteIdent(col.Name)
			}
		}
		colList := strings.Join(colNames, ", ")

	query := fmt.Sprintf("COPY (SELECT %s FROM %s) TO STDOUT WITH (FORMAT text)",
		colList, t.QualifiedName())
	if s.whereClause != "" {
		query = fmt.Sprintf("COPY (SELECT %s FROM %s WHERE %s) TO STDOUT WITH (FORMAT text)",
			colList, t.QualifiedName(), s.whereClause)
	}

	pr, pw := io.Pipe()

	go func() {
		_, err := conn.Conn().PgConn().CopyTo(ctx, pw, query)
		pw.CloseWithError(err)
	}()

	const maxRowSize = 256 * 1024 * 1024
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 1024*1024), maxRowSize)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			pr.Close()
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		rowData := make([]byte, len(line))
		copy(rowData, line)
		rowData = append(rowData, '\n')

		if err := processRow(source.Row{rowData}); err != nil {
			pr.Close()
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read COPY stream: %w", err)
	}

	return nil
}

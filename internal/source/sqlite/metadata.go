package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/catalog"
	"github.com/tking320/pgloader-go/internal/source"
)

// ---------------------------------------------------------------------------
// Table discovery
// ---------------------------------------------------------------------------

// discoverTables returns all non-system table names in the SQLite database.
func (s *SQLiteSource) discoverTables(ctx context.Context) ([]string, error) {
	query := `SELECT name FROM sqlite_master
WHERE type='table' AND name != 'sqlite_sequence'
ORDER BY name`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("discover tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		// Apply INCLUDING ONLY filter
		if len(s.includingOnly) > 0 {
			match, err := tableMatches(name, s.includingOnly)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
		}
		// Apply EXCLUDING filter
		if len(s.excluding) > 0 {
			match, err := tableMatches(name, s.excluding)
			if err != nil {
				return nil, err
			}
			if match {
				continue
			}
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// ---------------------------------------------------------------------------
// FetchMetadata (DbSource interface)
// ---------------------------------------------------------------------------

// FetchMetadata reads the SQLite schema into the catalog.
func (s *SQLiteSource) FetchMetadata(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("not connected: call Connect first")
	}

	// Check if sqlite_sequence exists (for auto-increment detection)
	var seqCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sqlite_sequence'`).Scan(&seqCount); err == nil {
		s.seqFound = seqCount > 0
	}

	s.catalog = &catalog.Catalog{}
	s.schema_ = &catalog.Schema{Name: s.schema, Catalog: s.catalog}
	s.catalog.Schemas = append(s.catalog.Schemas, s.schema_)

	if s.table != "" {
		table, err := s.fetchTableMetadata(ctx, s.table)
		if err != nil {
			return err
		}
		s.schema_.Tables = append(s.schema_.Tables, table)
	} else {
		tables, err := s.discoverTables(ctx)
		if err != nil {
			return err
		}
		for _, tbl := range tables {
			table, err := s.fetchTableMetadata(ctx, tbl)
			if err != nil {
				return fmt.Errorf("fetch table %s: %w", tbl, err)
			}
			s.schema_.Tables = append(s.schema_.Tables, table)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Table metadata
// ---------------------------------------------------------------------------

// fetchTableMetadata reads the full metadata for a single table.
func (s *SQLiteSource) fetchTableMetadata(ctx context.Context, tableName string) (*catalog.Table, error) {
	t := &catalog.Table{
		Name:   tableName,
		Schema: s.schema_,
	}

	// Fetch columns
	columns, err := s.fetchColumns(ctx, tableName)
	if err != nil {
		return nil, err
	}
	t.Columns = columns

	// Fetch indexes
	indexes, err := s.fetchIndexes(ctx, tableName)
	if err != nil {
		return nil, err
	}
	t.Indexes = indexes

	// Fetch foreign keys
	fkeys, err := s.fetchForeignKeys(ctx, tableName)
	if err != nil {
		return nil, err
	}
	t.ForeignKeys = fkeys

	// Detect auto-increment columns (INTEGER PRIMARY KEY in CREATE TABLE SQL)
	if err := s.detectAutoIncrement(ctx, tableName, t); err != nil {
		return nil, err
	}

	return t, nil
}

// ---------------------------------------------------------------------------
// Columns
// ---------------------------------------------------------------------------

type sqliteColumnRow struct {
	CID     int
	Name    string
	Type    sql.NullString
	NotNull int
	Default sql.NullString
	PK      int
}

func (s *SQLiteSource) fetchColumns(ctx context.Context, tableName string) ([]*catalog.Column, error) {
	// SQLite PRAGMA table_info returns: cid, name, type, notnull, dflt_value, pk
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info('%s')", escapeIdent(tableName)))
	if err != nil {
		return nil, fmt.Errorf("fetch columns for %s: %w", tableName, err)
	}
	defer rows.Close()

	var cols []*catalog.Column
	for rows.Next() {
		var r sqliteColumnRow
		if err := rows.Scan(&r.CID, &r.Name, &r.Type, &r.NotNull, &r.Default, &r.PK); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}

		sourceType := "text"
		if r.Type.Valid {
			sourceType = r.Type.String
		}

		// Apply CAST rules
		result := s.castEngine.Apply(sourceType, sourceType, "")

		targetType := result.TargetType

		col := &catalog.Column{
			Name:       r.Name,
			TypeName:   targetType,
			Nullable:   r.NotNull == 0,
			Extra:      "",
			Transform:  result.Transform,
			SourceType: sourceType,
			IsPK:       r.PK > 0,
		}

		// Handle default value
		if r.Default.Valid {
			dflt := r.Default.String
			if dflt == "NULL" {
				col.Default = "NULL"
			} else if dflt == "CURRENT_TIMESTAMP" || dflt == "CURRENT_TIME" || dflt == "CURRENT_DATE" {
				col.Default = dflt
			} else if strings.HasPrefix(dflt, "datetime(") || strings.HasPrefix(dflt, "strftime(") {
				col.Default = "CURRENT_TIMESTAMP"
			} else if isTextSourceType(sourceType) {
				col.Default = fmt.Sprintf("'%s'", strings.ReplaceAll(dflt, "'", "''"))
			} else {
				col.Default = dflt
			}
		}

		cols = append(cols, col)
	}

	return cols, rows.Err()
}

// ---------------------------------------------------------------------------
// Indexes
// ---------------------------------------------------------------------------

type sqliteIndexRow struct {
	Seq     int
	Name    string
	Unique  int
	Origin  string
	Partial int
}

type sqliteIndexColRow struct {
	Seqno int
	CID   int
	Name  string
}

func (s *SQLiteSource) fetchIndexes(ctx context.Context, tableName string) ([]*catalog.Index, error) {
	// Get index list via PRAGMA
	idxRows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list('%s')", escapeIdent(tableName)))
	if err != nil {
		return nil, fmt.Errorf("fetch index list for %s: %w", tableName, err)
	}
	defer idxRows.Close()

	var indexes []*catalog.Index
	for idxRows.Next() {
		var r sqliteIndexRow
		if err := idxRows.Scan(&r.Seq, &r.Name, &r.Unique, &r.Origin, &r.Partial); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}

		// Skip auto-generated indexes (sqlite_autoindex_*)
		if strings.HasPrefix(r.Name, "sqlite_autoindex_") {
			continue
		}

		// Get index columns via PRAGMA index_info
		colRows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info('%s')", escapeIdent(r.Name)))
		if err != nil {
			return nil, fmt.Errorf("fetch index info for %s: %w", r.Name, err)
		}

		var colNames []string
		for colRows.Next() {
			var ci sqliteIndexColRow
			if err := colRows.Scan(&ci.Seqno, &ci.CID, &ci.Name); err != nil {
				colRows.Close()
				return nil, fmt.Errorf("scan index col: %w", err)
			}
			colNames = append(colNames, ci.Name)
		}
		colRows.Close()

		idx := &catalog.Index{
			Name:    r.Name,
			Type:    "btree",
			Schema:  s.schema,
			Table:   tableName,
			Primary: r.Origin == "pk",
			Unique:  r.Unique != 0,
			Columns: colNames,
		}
		indexes = append(indexes, idx)
	}

	return indexes, idxRows.Err()
}

// ---------------------------------------------------------------------------
// Foreign keys
// ---------------------------------------------------------------------------

type sqliteFKRow struct {
	ID       int
	Seq      int
	Table    string
	From     string
	To       string
	OnUpdate string
	OnDelete string
	Match    string
}

func (s *SQLiteSource) fetchForeignKeys(ctx context.Context, tableName string) ([]*catalog.ForeignKey, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list('%s')", escapeIdent(tableName)))
	if err != nil {
		return nil, fmt.Errorf("fetch foreign keys for %s: %w", tableName, err)
	}
	defer rows.Close()

	// SQLite's PRAGMA foreign_key_list returns one row per column per FK.
	// Multiple columns in a composite FK are grouped by the same ID.
	//
	// IMPORTANT: Each row contains the full metadata (id, seq, table, from, to,
	// on_update, on_delete). Rows with the same id belong to the same FK constraint.
	// Row with seq=0 has the first column; seq=1 has the second column, etc.
	// The "table" field (referenced table) and on_update/on_delete are the same
	// for all rows of the same FK constraint.

	// Group by FK id
	type partialFK struct {
		id       int
		fromCols []string
		toCols   []string
		refTable string
		onUpdate string
		onDelete string
	}
	fkMap := make(map[int]*partialFK)
	var fkOrder []int

	for rows.Next() {
		var r sqliteFKRow
		if err := rows.Scan(&r.ID, &r.Seq, &r.Table, &r.From, &r.To, &r.OnUpdate, &r.OnDelete, &r.Match); err != nil {
			return nil, fmt.Errorf("scan fk: %w", err)
		}

		p, exists := fkMap[r.ID]
		if !exists {
			p = &partialFK{id: r.ID, refTable: r.Table, onUpdate: r.OnUpdate, onDelete: r.OnDelete}
			fkMap[r.ID] = p
			fkOrder = append(fkOrder, r.ID)
		}
		p.fromCols = append(p.fromCols, r.From)
		p.toCols = append(p.toCols, r.To)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	var fkeys []*catalog.ForeignKey
	for _, id := range fkOrder {
		p := fkMap[id]
		fk := &catalog.ForeignKey{
			Name:           fmt.Sprintf("fk_%s_%s", tableName, p.refTable),
			TableName:      tableName,
			Columns:        p.fromCols,
			ForeignTable:   p.refTable,
			ForeignColumns: p.toCols,
			UpdateRule:     p.onUpdate,
			DeleteRule:     p.onDelete,
		}
		fkeys = append(fkeys, fk)
	}

	return fkeys, nil
}

// ---------------------------------------------------------------------------
// Auto-increment detection
// ---------------------------------------------------------------------------

// detectAutoIncrement checks if a table has auto-increment columns.
// SQLite AUTOINCREMENT is only valid for INTEGER PRIMARY KEY columns.
func (s *SQLiteSource) detectAutoIncrement(ctx context.Context, tableName string, t *catalog.Table) error {
	// Method 1: Check sqlite_sequence table
	if s.seqFound {
		var seqVal int64
		err := s.db.QueryRowContext(ctx,
			`SELECT seq FROM sqlite_sequence WHERE name = ?`, tableName).Scan(&seqVal)
		if err == nil {
			// Table has been auto-incremented; mark INTEGER PK columns
			for _, col := range t.Columns {
				if col.IsPK && strings.EqualFold(col.SourceType, "integer") {
					col.IsAutoInc = true
					// Re-map to bigserial via cast engine with auto_increment flag
					result := s.castEngine.Apply(col.SourceType, col.SourceType, "auto_increment")
					col.TypeName = result.TargetType
					col.Transform = result.Transform
				}
			}
		}
	}

	// Method 2: Parse CREATE TABLE for AUTOINCREMENT keyword
	var createSQL sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, tableName).Scan(&createSQL); err == nil && createSQL.Valid {
		upper := strings.ToUpper(createSQL.String)
		if strings.Contains(upper, "AUTOINCREMENT") {
			for _, col := range t.Columns {
				if col.IsPK && strings.EqualFold(col.SourceType, "integer") {
					col.IsAutoInc = true
					result := s.castEngine.Apply(col.SourceType, col.SourceType, "auto_increment")
					col.TypeName = result.TargetType
					col.Transform = result.Transform
				}
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// DbSource interface methods
// ---------------------------------------------------------------------------

// PrepareTarget creates or prepares the target PostgreSQL tables.
func (s *SQLiteSource) PrepareTarget(ctx context.Context, opts source.PrepareOptions) error {
	if s.pool == nil || s.schema_ == nil {
		return nil
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire target connection: %w", err)
	}
	defer conn.Release()

	if opts.CreateSchemas && s.schema != "" && !strings.EqualFold(s.schema, "public") {
		if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", catalog.QuoteIdent(s.schema))); err != nil {
			return fmt.Errorf("create schema %s: %w", s.schema, err)
		}
	}

	for _, t := range s.schema_.Tables {
		if opts.IncludeDrop {
			if _, err := conn.Exec(ctx, t.DropTableSQL()); err != nil {
				return fmt.Errorf("drop table %s: %w", t.Name, err)
			}
		}
		if opts.CreateTables {
			sql := t.CreateTableSQL()
			if _, err := conn.Exec(ctx, sql); err != nil {
				return fmt.Errorf("create table %s: %w\nSQL: %s", t.Name, err, sql)
			}
		}
		if opts.Truncate {
			if _, err := conn.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s", t.QualifiedName())); err != nil {
				return fmt.Errorf("truncate %s: %w", t.Name, err)
			}
		}
	}

	return nil
}

// CompleteTarget creates indexes, foreign keys, and resets sequences.
func (s *SQLiteSource) CompleteTarget(ctx context.Context, opts source.CompleteOptions) error {
	if s.pool == nil || s.schema_ == nil {
		return nil
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire target connection: %w", err)
	}
	defer conn.Release()

	if opts.CreateIndexes {
		for _, t := range s.schema_.Tables {
			for _, idx := range t.Indexes {
				sql := idx.CreateIndexSQL()
				if _, err := conn.Exec(ctx, sql); err != nil {
					return fmt.Errorf("create index %s: %w\nSQL: %s", idx.Name, err, sql)
				}
			}
		}
	}

	if opts.ForeignKeys {
		for _, t := range s.schema_.Tables {
			for _, fk := range t.ForeignKeys {
				sql := fk.CreateFKeySQL()
				if _, err := conn.Exec(ctx, sql); err != nil {
					return fmt.Errorf("create fk %s: %w\nSQL: %s", fk.Name, err, sql)
				}
			}
		}
	}

	if opts.ResetSequences {
		for _, t := range s.schema_.Tables {
			if err := s.resetSequences(ctx, conn, t); err != nil {
				return err
			}
		}
	}

	return nil
}

// resetSequences sets PostgreSQL sequences for auto-increment columns.
func (s *SQLiteSource) resetSequences(ctx context.Context, conn *pgxpool.Conn, t *catalog.Table) error {
	for _, col := range t.Columns {
		if col.IsAutoInc {
			seqName := fmt.Sprintf("%s_%s_seq", t.Name, col.Name)
			sql := fmt.Sprintf("SELECT setval('%s', COALESCE((SELECT MAX(%s) FROM %s), 1))",
				seqName, col.Name, t.QualifiedName())
			if _, err := conn.Exec(ctx, sql); err != nil {
				continue
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func escapeIdent(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func isTextSourceType(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "text", "varchar", "nvarchar", "char", "nchar", "clob",
		"date", "datetime", "timestamp", "time":
		return true
	default:
		return false
	}
}

// Table name pattern matching (INCLUDING/EXCLUDING).
// Duplicated from mysql/metadata.go -- shared package would be preferable
// but following existing pattern for now.

func tableMatches(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, p := range patterns {
		re, err := compileTablePattern(p)
		if err != nil {
			return false, fmt.Errorf("invalid table pattern %q: %w", p, err)
		}
		if re.MatchString(name) {
			return true, nil
		}
	}
	return false, nil
}

func compileTablePattern(pattern string) (*regexp.Regexp, error) {
	p := pattern
	if strings.HasPrefix(p, "~/") && strings.HasSuffix(p, "/") {
		p = p[2 : len(p)-1]
		return regexp.Compile("(?i)" + p)
	}
	if len(p) >= 2 && p[0] == '\'' && p[len(p)-1] == '\'' {
		p = p[1 : len(p)-1]
	}
	if strings.ContainsAny(p, "%_") {
		escaped := regexp.QuoteMeta(p)
		escaped = strings.ReplaceAll(escaped, "%", ".*")
		escaped = strings.ReplaceAll(escaped, "_", ".")
		return regexp.Compile("^(?i)" + escaped + "$")
	}
	return regexp.Compile("^(?i)" + regexp.QuoteMeta(p) + "$")
}

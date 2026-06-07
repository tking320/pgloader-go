package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// Table discovery
// ---------------------------------------------------------------------------

// discoverTables returns all non-system table names in the database.
func (s *MSSQLSource) discoverTables(ctx context.Context) ([]string, error) {
	query := `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES
WHERE TABLE_TYPE = 'BASE TABLE' AND TABLE_CATALOG = @p1
ORDER BY TABLE_NAME`

	rows, err := s.db.QueryContext(ctx, query, sql.Named("p1", s.dbName))
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
		// Apply INCLUDING ONLY TABLE NAMES MATCHING filter
		if len(s.includingOnly) > 0 {
			match, err := tableMatches(name, s.includingOnly)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
		}
		// Apply EXCLUDING TABLE NAMES MATCHING filter
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
// Table metadata
// ---------------------------------------------------------------------------

// fetchTableMetadata reads the full metadata for a single table.
func (s *MSSQLSource) fetchTableMetadata(ctx context.Context, tableName string) (*catalog.Table, error) {
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

	// Fetch indexes (including primary key)
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

	return t, nil
}

// ---------------------------------------------------------------------------
// Columns
// ---------------------------------------------------------------------------

type mssqlColumnRow struct {
	Name            string
	DataType        string
	Default         sql.NullString
	IsNullable      string
	IsIdentity      sql.NullInt64
	CharMaxLen      sql.NullInt64
	NumericPrec     sql.NullInt64
	NumericScale    sql.NullInt64
	DatetimePrec    sql.NullInt64
	CharSetName     sql.NullString
	CollationName   sql.NullString
}

func (s *MSSQLSource) fetchColumns(ctx context.Context, tableName string) ([]*catalog.Column, error) {
	query := `SELECT
    c.COLUMN_NAME,
    c.DATA_TYPE,
    c.COLUMN_DEFAULT,
    c.IS_NULLABLE,
    COLUMNPROPERTY(object_id(c.TABLE_NAME), c.COLUMN_NAME, 'IsIdentity'),
    c.CHARACTER_MAXIMUM_LENGTH,
    c.NUMERIC_PRECISION,
    c.NUMERIC_SCALE,
    c.DATETIME_PRECISION,
    c.CHARACTER_SET_NAME,
    c.COLLATION_NAME
FROM INFORMATION_SCHEMA.COLUMNS c
JOIN INFORMATION_SCHEMA.TABLES t
    ON c.TABLE_SCHEMA = t.TABLE_SCHEMA AND c.TABLE_NAME = t.TABLE_NAME
WHERE c.TABLE_CATALOG = @p1 AND c.TABLE_NAME = @p2 AND t.TABLE_TYPE = 'BASE TABLE'
ORDER BY c.ORDINAL_POSITION`

	rows, err := s.db.QueryContext(ctx, query, sql.Named("p1", s.dbName), sql.Named("p2", tableName))
	if err != nil {
		return nil, fmt.Errorf("fetch columns for %s: %w", tableName, err)
	}
	defer rows.Close()

	var cols []*catalog.Column
	for rows.Next() {
		var r mssqlColumnRow
		if err := rows.Scan(&r.Name, &r.DataType, &r.Default, &r.IsNullable,
			&r.IsIdentity, &r.CharMaxLen, &r.NumericPrec, &r.NumericScale,
			&r.DatetimePrec, &r.CharSetName, &r.CollationName); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}

		// Build the column type string for CAST matching
		columnType := cast.GetMSSQLColumnType(r.DataType,
			int64OrZero(r.NumericPrec),
			int64OrZero(r.NumericScale),
			int64OrZero(r.CharMaxLen),
			int64OrZero(r.DatetimePrec))

		// Extra for auto-increment matching
		extra := ""
		if r.IsIdentity.Int64 == 1 {
			extra = "auto_increment"
		}

		// Apply CAST rules
		result := s.castEngine.Apply(r.DataType, columnType, extra)

		targetType := result.TargetType
		if !result.DropTypemod {
			mod := cast.ParseTypemod(columnType)
			targetType = cast.SubstituteTypemod(targetType, mod)
		}

		col := &catalog.Column{
			Name:       r.Name,
			TypeName:   targetType,
			Nullable:   r.IsNullable == "YES",
			Transform:  result.Transform,
			SourceType: r.DataType,
			IsAutoInc:  r.IsIdentity.Int64 == 1,
		}

		// Handle default value
		if r.Default.Valid && r.Default.String != "" {
			col.Default = normalizeMSSQLDefault(r.Default.String, targetType)
		}

		cols = append(cols, col)
	}

	return cols, rows.Err()
}

// normalizeMSSQLDefault normalizes an MSSQL column default for PostgreSQL DDL.
// Handles N'...' prefix, CURRENT_TIMESTAMP, newid(), NULL, etc.
func normalizeMSSQLDefault(raw, targetType string) string {
	// Strip outer parentheses: MSSQL stores defaults as ((value)) or (value)
	for strings.HasPrefix(raw, "((") && strings.HasSuffix(raw, "))") {
		raw = raw[1 : len(raw)-1]
	}
	if strings.HasPrefix(raw, "(") && strings.HasSuffix(raw, ")") {
		raw = raw[1 : len(raw)-1]
	}

	// Handle NULL
	if strings.EqualFold(raw, "NULL") {
		return ""
	}

	// Handle N'...' prefix (N'value') → 'value'
	if strings.HasPrefix(raw, "N'") && strings.HasSuffix(raw, "'") {
		raw = "'" + raw[2:]
	}

	// Handle boolean defaults (MSSQL BIT -> PG boolean)
	if targetType == "boolean" {
		if raw == "1" {
			return "TRUE"
		}
		if raw == "0" {
			return "FALSE"
		}
	}

	// Handle CURRENT_TIMESTAMP(n) variations
	upper := strings.ToUpper(raw)
	if strings.HasPrefix(upper, "CURRENT_TIMESTAMP") || strings.EqualFold(raw, "CURRENT TIMESTAMP") {
		return "CURRENT_TIMESTAMP"
	}

	// Handle newid() / newsequentialid()
	if strings.EqualFold(raw, "newid()") || strings.EqualFold(raw, "newsequentialid()") {
		return "" // let PG generate UUIDs via DEFAULT gen_random_uuid()
	}

	// Handle sysdatetimeoffset()
	if strings.EqualFold(raw, "sysdatetimeoffset()") {
		return "CURRENT_TIMESTAMP"
	}

	// Handle getdate()
	if strings.EqualFold(raw, "getdate()") {
		return "CURRENT_TIMESTAMP"
	}

	// Handle convert(...) variations
	if strings.HasPrefix(upper, "CONVERT(") && strings.Contains(raw, "getdate()") {
		return "CURRENT_TIMESTAMP"
	}

	return raw
}

// ---------------------------------------------------------------------------
// Indexes
// ---------------------------------------------------------------------------

type mssqlIndexRow struct {
	SchemaName  string
	TableName   string
	IndexName   string
	ColumnName  string
	IsUnique    bool
	IsPK        bool
	FilterDef   sql.NullString
}

func (s *MSSQLSource) fetchIndexes(ctx context.Context, tableName string) ([]*catalog.Index, error) {
	query := `SELECT
    schema_name(schema_id) as SchemaName,
    o.name as TableName,
    REPLACE(i.name, '.', '_') as IndexName,
    co.[name] as ColumnName,
    i.is_unique,
    i.is_primary_key,
    i.filter_definition
FROM sys.indexes i
JOIN sys.objects o ON i.object_id = o.object_id
JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
JOIN sys.columns co ON co.object_id = i.object_id AND co.column_id = ic.column_id
WHERE o.name = @p1
ORDER BY i.name, ic.key_ordinal`

	rows, err := s.db.QueryContext(ctx, query, sql.Named("p1", tableName))
	if err != nil {
		return nil, fmt.Errorf("fetch indexes for %s: %w", tableName, err)
	}
	defer rows.Close()

	// Group rows by index name
	type indexGroup struct {
		Name       string
		Type       string
		Primary    bool
		Unique     bool
		FilterDef  string
		ColumnList []string
	}
	indexMap := make(map[string]*indexGroup)
	var order []string

	for rows.Next() {
		var r mssqlIndexRow
		if err := rows.Scan(&r.SchemaName, &r.TableName, &r.IndexName,
			&r.ColumnName, &r.IsUnique, &r.IsPK, &r.FilterDef); err != nil {
			return nil, err
		}

		g, ok := indexMap[r.IndexName]
		if !ok {
			idxType := ""
			if r.IsPK {
				idxType = "btree"
			}
			g = &indexGroup{
				Name:     r.IndexName,
				Type:     idxType,
				Primary:  r.IsPK,
				Unique:   r.IsUnique,
				FilterDef: r.FilterDef.String,
			}
			indexMap[r.IndexName] = g
			order = append(order, r.IndexName)
		}
		g.ColumnList = append(g.ColumnList, r.ColumnName)
	}

	var indexes []*catalog.Index
	for _, name := range order {
		g := indexMap[name]
		idx := &catalog.Index{
			Name:    g.Name,
			Type:    g.Type,
			Schema:  s.schema,
			Table:   tableName,
			Primary: g.Primary,
			Unique:  g.Unique,
			Columns: g.ColumnList,
		}
		indexes = append(indexes, idx)

		// Set IsPK on columns
		if g.Primary {
			for _, col := range g.ColumnList {
				s.setPKFlag(tableName, col)
			}
		}
	}

	return indexes, rows.Err()
}

// setPKFlag marks a column as primary key in the table's column list.
func (s *MSSQLSource) setPKFlag(tableName, colName string) {
	for _, t := range s.schema_.Tables {
		if t.Name == tableName {
			for _, c := range t.Columns {
				if c.Name == colName {
					c.IsPK = true
					return
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Foreign keys
// ---------------------------------------------------------------------------

type mssqlFKRow struct {
	ConstraintName string
	SchemaName     string
	TableName      string
	ColumnName     string
	RefSchemaName  string
	RefTableName   string
	RefColumnName  string
	UpdateRule     string
	DeleteRule     string
}

func (s *MSSQLSource) fetchForeignKeys(ctx context.Context, tableName string) ([]*catalog.ForeignKey, error) {
	query := `SELECT
    REPLACE(KCU1.CONSTRAINT_NAME, '.', '_'),
    KCU1.TABLE_SCHEMA,
    KCU1.TABLE_NAME,
    KCU1.COLUMN_NAME,
    KCU2.TABLE_SCHEMA,
    KCU2.TABLE_NAME,
    KCU2.COLUMN_NAME,
    RC.UPDATE_RULE,
    RC.DELETE_RULE
FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS RC
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE KCU1
    ON KCU1.CONSTRAINT_CATALOG = RC.CONSTRAINT_CATALOG
    AND KCU1.CONSTRAINT_SCHEMA = RC.CONSTRAINT_SCHEMA
    AND KCU1.CONSTRAINT_NAME = RC.CONSTRAINT_NAME
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE KCU2
    ON KCU2.CONSTRAINT_CATALOG = RC.UNIQUE_CONSTRAINT_CATALOG
    AND KCU2.CONSTRAINT_SCHEMA = RC.UNIQUE_CONSTRAINT_SCHEMA
    AND KCU2.CONSTRAINT_NAME = RC.UNIQUE_CONSTRAINT_NAME
WHERE KCU1.ORDINAL_POSITION = KCU2.ORDINAL_POSITION
    AND KCU1.TABLE_CATALOG = @p1
    AND KCU1.TABLE_NAME = @p2
ORDER BY KCU1.CONSTRAINT_NAME, KCU1.ORDINAL_POSITION`

	rows, err := s.db.QueryContext(ctx, query, sql.Named("p1", s.dbName), sql.Named("p2", tableName))
	if err != nil {
		return nil, fmt.Errorf("fetch foreign keys for %s: %w", tableName, err)
	}
	defer rows.Close()

	type fkGroup struct {
		Name          string
		Schema        string
		Table         string
		RefSchema     string
		RefTable      string
		UpdateRule    string
		DeleteRule    string
		Columns       []string
		RefColumns    []string
	}
	fkMap := make(map[string]*fkGroup)
	var order []string

	for rows.Next() {
		var r mssqlFKRow
		if err := rows.Scan(&r.ConstraintName, &r.SchemaName, &r.TableName,
			&r.ColumnName, &r.RefSchemaName, &r.RefTableName,
			&r.RefColumnName, &r.UpdateRule, &r.DeleteRule); err != nil {
			return nil, err
		}

		g, ok := fkMap[r.ConstraintName]
		if !ok {
			g = &fkGroup{
				Name:       r.ConstraintName,
				Schema:     r.SchemaName,
				Table:      r.TableName,
				RefSchema:  r.RefSchemaName,
				RefTable:   r.RefTableName,
				UpdateRule: r.UpdateRule,
				DeleteRule: r.DeleteRule,
			}
			fkMap[r.ConstraintName] = g
			order = append(order, r.ConstraintName)
		}
		g.Columns = append(g.Columns, r.ColumnName)
		g.RefColumns = append(g.RefColumns, r.RefColumnName)
	}

	var fkeys []*catalog.ForeignKey
	for _, name := range order {
		g := fkMap[name]
		fk := &catalog.ForeignKey{
			Name:           g.Name,
			TableName:      s.schema + "." + g.Table,
			Columns:        g.Columns,
			ForeignTable:   s.schema + "." + g.RefTable,
			ForeignColumns: g.RefColumns,
			UpdateRule:     g.UpdateRule,
			DeleteRule:     g.DeleteRule,
		}
		fkeys = append(fkeys, fk)
	}

	return fkeys, rows.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func int64OrZero(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// ---------------------------------------------------------------------------
// Table name pattern matching (INCLUDING/EXCLUDING)
// ---------------------------------------------------------------------------

// tableMatches checks if a table name matches any of the given patterns.
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

// compileTablePattern converts a pgloader table name pattern to a compiled regexp.
// Pattern formats:
//
//	~/regex/     → case-insensitive regex match
//	'pattern'    → case-insensitive exact match, with LIKE wildcard conversion
//	pattern      → same as quoted (case-insensitive, LIKE wildcard conversion)
func compileTablePattern(pattern string) (*regexp.Regexp, error) {
	p := pattern

	// Regex pattern: ~/.../
	if strings.HasPrefix(p, "~/") && strings.HasSuffix(p, "/") {
		p = p[2 : len(p)-1]
		return regexp.Compile("(?i)" + p)
	}

	// Strip surrounding quotes
	if len(p) >= 2 && p[0] == '\'' && p[len(p)-1] == '\'' {
		p = p[1 : len(p)-1]
	}

	// Check for SQL LIKE wildcards and convert to regex
	if strings.ContainsAny(p, "%_") {
		escaped := regexp.QuoteMeta(p)
		escaped = strings.ReplaceAll(escaped, "%", ".*")
		escaped = strings.ReplaceAll(escaped, "_", ".")
		return regexp.Compile("^(?i)" + escaped + "$")
	}

	// Plain text: case-insensitive exact match
	return regexp.Compile("^(?i)" + regexp.QuoteMeta(p) + "$")
}

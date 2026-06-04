package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// Table discovery
// ---------------------------------------------------------------------------

// discoverTables returns all non-system table names in the database.
func (s *MySQLSource) discoverTables(ctx context.Context) ([]string, error) {
	query := `SELECT table_name FROM information_schema.tables
WHERE table_schema = ? AND table_type = 'BASE TABLE'
ORDER BY table_name`

	rows, err := s.db.QueryContext(ctx, query, s.dbName)
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
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// ---------------------------------------------------------------------------
// Table metadata
// ---------------------------------------------------------------------------

// fetchTableMetadata reads the full metadata for a single table.
func (s *MySQLSource) fetchTableMetadata(ctx context.Context, tableName string) (*catalog.Table, error) {
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

type mysqlColumnRow struct {
	Name         string
	DataType     string
	ColumnType   string
	Nullable     string
	Default      sql.NullString
	Extra        string
	Comment      string
	CharMaxLen   sql.NullInt64
	NumericPrec  sql.NullInt64
	NumericScale sql.NullInt64
}

func (s *MySQLSource) fetchColumns(ctx context.Context, tableName string) ([]*catalog.Column, error) {
	query := `SELECT
    c.column_name,
    c.data_type,
    c.column_type,
    c.is_nullable,
    c.column_default,
    c.extra,
    c.column_comment,
    c.character_maximum_length,
    c.numeric_precision,
    c.numeric_scale
FROM information_schema.columns c
WHERE c.table_schema = ? AND c.table_name = ?
ORDER BY c.ordinal_position`

	rows, err := s.db.QueryContext(ctx, query, s.dbName, tableName)
	if err != nil {
		return nil, fmt.Errorf("fetch columns for %s: %w", tableName, err)
	}
	defer rows.Close()

	var cols []*catalog.Column
	for rows.Next() {
		var r mysqlColumnRow
		if err := rows.Scan(&r.Name, &r.DataType, &r.ColumnType, &r.Nullable,
			&r.Default, &r.Extra, &r.Comment, &r.CharMaxLen, &r.NumericPrec, &r.NumericScale); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}

		// Apply CAST rules
		result := s.castEngine.Apply(r.DataType, r.ColumnType, r.Extra)

		targetType := result.TargetType
		if !result.DropTypemod {
			mod := cast.ParseTypemod(r.ColumnType)
			targetType = cast.SubstituteTypemod(targetType, mod)
		}

		col := &catalog.Column{
			Name:       r.Name,
			TypeName:   targetType,
			Nullable:   r.Nullable == "YES",
			Comment:    r.Comment,
			Extra:      r.Extra,
			Transform:  result.Transform,
			SourceType: r.DataType,
			IsAutoInc:  strings.Contains(r.Extra, "auto_increment"),
		}

		// Handle default value
		if r.Default.Valid && r.Default.String != "" {
			if r.Default.String == "NULL" {
				col.Default = "NULL"
			} else if r.Default.String == "CURRENT_TIMESTAMP" ||
				r.Default.String == "current_timestamp()" {
				col.Default = "CURRENT_TIMESTAMP"
			} else if isTextType(r.DataType) {
				col.Default = fmt.Sprintf("'%s'", escapeDefault(r.Default.String))
			} else {
				col.Default = r.Default.String
			}
		
			// Adjust default for boolean-typed columns
			if targetType == "boolean" {
				if col.Default == "0" {
					col.Default = "false"
				} else if col.Default == "1" {
					col.Default = "true"
				}
			}

			// Remove invalid zero-date defaults for date/timestamptz columns
			if (targetType == "date" || targetType == "timestamptz") &&
				(col.Default == "'0000-00-00'" || col.Default == "'0000-00-00 00:00:00'") {
				col.Default = ""
			}
		}

		cols = append(cols, col)
	}

	return cols, rows.Err()
}

// ---------------------------------------------------------------------------
// Indexes
// ---------------------------------------------------------------------------

type mysqlIndexRow struct {
	TableName    string
	IndexName    string
	IndexType    string
	NonUnique    int64
	ColumnNames  string
}

func (s *MySQLSource) fetchIndexes(ctx context.Context, tableName string) ([]*catalog.Index, error) {
	query := `SELECT
    table_name, index_name, index_type,
    SUM(non_unique),
    CAST(GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',') AS CHAR)
FROM information_schema.statistics
WHERE table_schema = ? AND table_name = ?
GROUP BY table_name, index_name, index_type`

	rows, err := s.db.QueryContext(ctx, query, s.dbName, tableName)
	if err != nil {
		return nil, fmt.Errorf("fetch indexes for %s: %w", tableName, err)
	}
	defer rows.Close()

	var indexes []*catalog.Index
	for rows.Next() {
		var r mysqlIndexRow
		if err := rows.Scan(&r.TableName, &r.IndexName, &r.IndexType,
			&r.NonUnique, &r.ColumnNames); err != nil {
			return nil, err
		}

		cols := splitCSV(r.ColumnNames)
		isPK := r.IndexName == "PRIMARY"
		isUnique := r.NonUnique == 0

		idx := &catalog.Index{
			Name:    r.IndexName,
			Type:    r.IndexType,
			Schema:  s.schema,
			Table:   tableName,
			Primary: isPK,
			Unique:  isUnique,
			Columns: cols,
		}
		indexes = append(indexes, idx)

		// Set IsPK on columns
		if isPK {
			for _, col := range cols {
				s.setPKFlag(tableName, col)
			}
		}
	}

	return indexes, rows.Err()
}

// setPKFlag marks a column as primary key in the table's column list.
func (s *MySQLSource) setPKFlag(tableName, colName string) {
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

type mysqlFKRow struct {
	TableName      string
	ConstraintName string
	RefTableName   string
	ColumnNames    string
	RefColumnNames string
	UpdateRule     string
	DeleteRule     string
}

func (s *MySQLSource) fetchForeignKeys(ctx context.Context, tableName string) ([]*catalog.ForeignKey, error) {
	query := `SELECT
    tc.table_name,
    tc.constraint_name,
    k.REFERENCED_TABLE_NAME,
    GROUP_CONCAT(k.column_name ORDER BY k.ordinal_position SEPARATOR ',') AS cols,
    GROUP_CONCAT(k.REFERENCED_COLUMN_NAME ORDER BY k.position_in_unique_constraint SEPARATOR ',') AS fcols,
    rc.update_rule,
    rc.delete_rule
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage k
    ON k.table_schema = tc.table_schema
    AND k.table_name = tc.table_name
    AND k.constraint_name = tc.constraint_name
JOIN information_schema.referential_constraints rc
    ON rc.constraint_schema = tc.table_schema
    AND rc.constraint_name = tc.constraint_name
WHERE tc.table_schema = ? AND tc.table_name = ?
    AND tc.constraint_type = 'FOREIGN KEY'
    AND k.REFERENCED_TABLE_NAME IS NOT NULL
GROUP BY tc.table_name, tc.constraint_name, k.REFERENCED_TABLE_NAME,
    rc.update_rule, rc.delete_rule`

	rows, err := s.db.QueryContext(ctx, query, s.dbName, tableName)
	if err != nil {
		return nil, fmt.Errorf("fetch foreign keys for %s: %w", tableName, err)
	}
	defer rows.Close()

	var fkeys []*catalog.ForeignKey
	for rows.Next() {
		var r mysqlFKRow
		if err := rows.Scan(&r.TableName, &r.ConstraintName, &r.RefTableName,
			&r.ColumnNames, &r.RefColumnNames, &r.UpdateRule, &r.DeleteRule); err != nil {
			return nil, err
		}

		fk := &catalog.ForeignKey{
			Name:          r.ConstraintName,
			TableName:     r.TableName,
			Columns:       splitCSV(r.ColumnNames),
			ForeignTable:  r.RefTableName,
			ForeignColumns: splitCSV(r.RefColumnNames),
			UpdateRule:    r.UpdateRule,
			DeleteRule:    r.DeleteRule,
		}
		fkeys = append(fkeys, fk)
	}

	return fkeys, rows.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, len(parts))
	for i, p := range parts {
		result[i] = strings.TrimSpace(p)
	}
	return result
}

func escapeDefault(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s)
}

func isTextType(dataType string) bool {
	switch strings.ToLower(dataType) {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext",
		"enum", "set", "date", "datetime", "timestamp", "time":
		return true
	default:
		return false
	}
}

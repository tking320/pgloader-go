// Package mssql implements Microsoft SQL Server source support for pgloader.
// It connects to an MSSQL database, introspects the schema, reads data,
// and provides it to the pipeline for COPY into PostgreSQL.
package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/catalog"
	"github.com/tking320/pgloader-go/internal/source"
)

// MSSQLSource implements source.Source and source.DbSource for MSSQL databases.
type MSSQLSource struct {
	// MSSQL connection
	host     string
	port     int
	user     string
	password string
	dbName   string
	db       *sql.DB

	// Target PostgreSQL connection (for PrepareTarget/CompleteTarget)
	pool *pgxpool.Pool

	// Target schema/table
	schema string
	table  string

	// Schema catalog (populated by FetchMetadata)
	catalog *catalog.Catalog
	schema_ *catalog.Schema

	// CAST engine
	castEngine *cast.Engine

	// Concurrency sharding
	whereClause string // WHERE clause for sharded reads
	activeTable int    // index into schema_.Tables for MapRows

	// Table name filtering (INCLUDING/EXCLUDING)
	includingOnly []string
	excluding     []string
}

// New creates an MSSQLSource.
func New(host string, port int, user, password, dbName, schema, table string, pool *pgxpool.Pool, castEngine *cast.Engine) *MSSQLSource {
	return &MSSQLSource{
		host:       host,
		port:       port,
		user:       user,
		password:   password,
		dbName:     dbName,
		schema:     schema,
		table:      table,
		pool:       pool,
		castEngine: castEngine,
	}
}

// Connect opens a connection to MSSQL.
func (s *MSSQLSource) Connect(ctx context.Context) error {
	dsn := fmt.Sprintf("sqlserver://%s:%s@%s:%d?database=%s&encrypt=disable",
		s.user, s.password, s.host, s.port, s.dbName)

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return fmt.Errorf("mssql connect: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("mssql ping: %w", err)
	}

	s.db = db
	return nil
}

// Close closes the MSSQL connection.
func (s *MSSQLSource) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SetTableFilters configures INCLUDING/EXCLUDING table name patterns.
func (s *MSSQLSource) SetTableFilters(including, excluding []string) {
	s.includingOnly = including
	s.excluding = excluding
}

// ---------------------------------------------------------------------------
// Source interface
// ---------------------------------------------------------------------------

func (s *MSSQLSource) TableName() string { return s.table }

func (s *MSSQLSource) SetActiveTable(name string) error {
	if s.schema_ == nil {
		return fmt.Errorf("no catalog: call FetchMetadata first")
	}
	for i, t := range s.schema_.Tables {
		if t.Name == name {
			s.activeTable = i
			return nil
		}
	}
	return fmt.Errorf("table %q not found in catalog", name)
}

func (s *MSSQLSource) ActiveTable() *catalog.Table {
	if s.schema_ == nil || len(s.schema_.Tables) <= s.activeTable {
		return nil
	}
	return s.schema_.Tables[s.activeTable]
}

func (s *MSSQLSource) TableNames() []string {
	if s.schema_ == nil {
		return nil
	}
	names := make([]string, len(s.schema_.Tables))
	for i, t := range s.schema_.Tables {
		names[i] = t.Name
	}
	return names
}

func (s *MSSQLSource) Encoding() string { return "UTF8" }

func (s *MSSQLSource) DataIsPreformatted() bool { return false }

func (s *MSSQLSource) Clone() source.Source {
	clone := *s
	clone.db = nil // each clone gets its own connection
	return &clone
}

// CopyColumnList returns the column list for COPY command.
func (s *MSSQLSource) CopyColumnList() []string {
	if s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return nil
	}
	t := s.ActiveTable()
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = c.Name
	}
	return cols
}

// ConcurrencySupport returns sharded sources for parallel loading.
// Uses primary key range sharding (same pattern as MySQL).
func (s *MSSQLSource) ConcurrencySupport(ctx context.Context, concurrency int) ([]source.Source, error) {
	if concurrency <= 1 || s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return nil, nil
	}

	t := s.ActiveTable()

	// Find integer primary key
	var pkName string
	for _, col := range t.Columns {
		if col.IsPK {
			pkName = col.Name
			break
		}
	}
	if pkName == "" {
		return nil, nil // no PK, can't shard
	}

	// Get min/max
	query := fmt.Sprintf("SELECT MIN(%s), MAX(%s) FROM [%s]", quoteIdent(pkName), quoteIdent(pkName), t.Name)
	var min, max sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query).Scan(&min, &max); err != nil {
		return nil, fmt.Errorf("get pk range: %w", err)
	}
	if !min.Valid || !max.Valid {
		return nil, nil // empty table
	}

	rangeSize := (max.Int64 - min.Int64) / int64(concurrency)
	if rangeSize < 1 {
		rangeSize = 1
	}

	var sources []source.Source
	for i := 0; i < concurrency; i++ {
		lo := min.Int64 + int64(i)*rangeSize
		hi := lo + rangeSize
		if i == concurrency-1 {
			hi = max.Int64 + 1
		}
		clone := s.Clone().(*MSSQLSource)
		clone.whereClause = fmt.Sprintf("%s >= %d AND %s < %d", quoteIdent(pkName), lo, quoteIdent(pkName), hi)
		if err := clone.Connect(ctx); err != nil {
			return nil, fmt.Errorf("clone connect: %w", err)
		}
		sources = append(sources, clone)
	}

	return sources, nil
}

// MapRows reads all rows from the MSSQL table and calls processRow for each.
func (s *MSSQLSource) MapRows(ctx context.Context, processRow func(source.Row) error) error {
	if s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return fmt.Errorf("no table metadata: call FetchMetadata first")
	}

	t := s.ActiveTable()

	// Build SELECT with proper SQL expressions per column type
	// (mirroring get-column-sql-expression from the reference)
	colExprs := make([]string, len(t.Columns))
	for i, col := range t.Columns {
		colExprs[i] = getColumnSQLExpression(col.Name, col.SourceType)
	}

	query := fmt.Sprintf("SELECT %s FROM [%s]", strings.Join(colExprs, ", "), t.Name)
	if s.whereClause != "" {
		query += " WHERE " + s.whereClause
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mssql query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("mssql columns: %w", err)
	}
	values := make([]interface{}, len(cols))

	for rows.Next() {
		scanTargets := make([]interface{}, len(cols))
		for i := range values {
			scanTargets[i] = &values[i]
		}

		if err := rows.Scan(scanTargets...); err != nil {
			return fmt.Errorf("mssql scan: %w", err)
		}

		// Convert to source.Row
		row := make(source.Row, len(cols))
		for i, v := range values {
			row[i] = convertMSSQLValue(v)
		}

		// Apply transforms
		for i, col := range t.Columns {
			if col.Transform != "" {
				xform := cast.GetTransform(col.Transform)
				if xform != nil {
					transformed, err := xform(row[i])
					if err != nil {
						return fmt.Errorf("transform %s on column %s: %w", col.Transform, col.Name, err)
					}
					row[i] = transformed
				}
			}
		}

		if err := processRow(row); err != nil {
			return err
		}
	}

	return rows.Err()
}

// getColumnSQLExpression returns the SQL expression for reading a column.
// Mirrors get-column-sql-expression from the reference implementation:
// date/time types use CONVERT with specific style codes for unambiguous format.
func getColumnSQLExpression(name, typeName string) string {
	quoted := quoteIdent(name)
	switch strings.ToLower(typeName) {
	case "time":
		return fmt.Sprintf("convert(varchar(30), %s, 114)", quoted)
	case "datetime", "datetime2":
		return fmt.Sprintf("convert(varchar(30), %s, 126)", quoted)
	case "datetimeoffset":
		return fmt.Sprintf("convert(varchar(35), %s, 127)", quoted)
		case "smalldatetime", "date":
			return fmt.Sprintf("convert(varchar(30), %s, 126)", quoted)
		case "uniqueidentifier":
			return fmt.Sprintf("convert(varchar(36), %s)", quoted)
		default:
			return quoted
		}
	}

// convertMSSQLValue converts a database/sql driver value to a standard Go value.
func convertMSSQLValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case int64:
		return val
	case float64:
		return val
	case bool:
		if val {
			return int64(1)
		}
		return int64(0)
	case string:
		return val
	case []byte:
		return string(val)
	case time.Time:
		return val.Format("2006-01-02 15:04:05.999999-07")
	default:
		return fmt.Sprintf("%v", val)
	}
}

// quoteIdent brackets an identifier for MSSQL quoting.
func quoteIdent(name string) string {
	if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
		return name
	}
	// Escape any embedded brackets
	name = strings.ReplaceAll(name, "]", "]]")
	return "[" + name + "]"
}

// ---------------------------------------------------------------------------
// DbSource interface
// ---------------------------------------------------------------------------

// FetchMetadata reads the MSSQL schema into the catalog.
func (s *MSSQLSource) FetchMetadata(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("not connected: call Connect first")
	}

	// Default to "dbo" (MSSQL's default schema) when not overridden,
	// matching the original pgloader behavior of preserving MSSQL schema names.
	if s.schema == "" {
		s.schema = "dbo"
	}

	s.catalog = &catalog.Catalog{}
	s.schema_ = &catalog.Schema{Name: s.schema, Catalog: s.catalog}
	s.catalog.Schemas = append(s.catalog.Schemas, s.schema_)

	if s.table != "" {
		// Single table mode
		table, err := s.fetchTableMetadata(ctx, s.table)
		if err != nil {
			return err
		}
		s.schema_.Tables = append(s.schema_.Tables, table)
	} else {
		// Full database mode — discover all tables
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

// PrepareTarget creates or prepares the target PostgreSQL tables.
func (s *MSSQLSource) PrepareTarget(ctx context.Context, opts source.PrepareOptions) error {
	if s.pool == nil {
		return fmt.Errorf("no target pool configured")
	}
	if s.schema_ == nil {
		return nil
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire target connection: %w", err)
	}
	defer conn.Release()

	// Create target schema if needed and not public
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
func (s *MSSQLSource) CompleteTarget(ctx context.Context, opts source.CompleteOptions) error {
	if s.pool == nil || s.schema_ == nil {
		return nil
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire target connection: %w", err)
	}
	defer conn.Release()

	// First pass: create all indexes
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

	// Second pass: create all foreign keys
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

	// Third pass: reset sequences
	if opts.ResetSequences {
		for _, t := range s.schema_.Tables {
			if err := s.resetSequences(ctx, conn, t); err != nil {
				return err
			}
		}
	}

	// Fourth pass: comments
	if opts.Comments {
		for _, t := range s.schema_.Tables {
			if sql := t.TableCommentSQL(); sql != "" {
				if _, err := conn.Exec(ctx, sql); err != nil {
					return fmt.Errorf("comment on table %s: %w\nSQL: %s", t.Name, err, sql)
				}
			}
			for _, col := range t.Columns {
				if sql := col.ColumnCommentSQL(t); sql != "" {
					if _, err := conn.Exec(ctx, sql); err != nil {
						return fmt.Errorf("comment on column %s.%s: %w\nSQL: %s", t.Name, col.Name, err, sql)
					}
				}
			}
		}
	}

	return nil
}

// resetSequences sets PostgreSQL sequences to MAX(pk) for identity columns.
func (s *MSSQLSource) resetSequences(ctx context.Context, conn *pgxpool.Conn, t *catalog.Table) error {
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

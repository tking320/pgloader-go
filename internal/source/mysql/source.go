// Package mysql implements MySQL source support for pgloader.
// It connects to a MySQL database, introspects the schema, reads data,
// and provides it to the pipeline for COPY into PostgreSQL.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/catalog"
	"github.com/tking320/pgloader-go/internal/source"
)

// MySQLSource implements source.Source and source.DbSource for MySQL databases.
type MySQLSource struct {
	// MySQL connection
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
	includingOnly []string // patterns from INCLUDING ONLY TABLE NAMES MATCHING
	excluding     []string // patterns from EXCLUDING TABLE NAMES MATCHING
}

// New creates a MySQLSource.
func New(host string, port int, user, password, dbName, schema, table string, pool *pgxpool.Pool, castEngine *cast.Engine) *MySQLSource {
	return &MySQLSource{
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

// Connect opens a connection to MySQL.
func (s *MySQLSource) Connect(ctx context.Context) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local",
		s.user, s.password, s.host, s.port, s.dbName)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("mysql connect: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("mysql ping: %w", err)
	}

	s.db = db
	return nil
}

// Close closes the MySQL connection.
func (s *MySQLSource) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SetTableFilters configures INCLUDING/EXCLUDING table name patterns.
func (s *MySQLSource) SetTableFilters(including, excluding []string) {
	s.includingOnly = including
	s.excluding = excluding
}

// ---------------------------------------------------------------------------
// Source interface
// ---------------------------------------------------------------------------

func (s *MySQLSource) TableName() string { return s.table }
func (s *MySQLSource) SetActiveTable(name string) error {
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

func (s *MySQLSource) ActiveTable() *catalog.Table {
	if s.schema_ == nil || len(s.schema_.Tables) <= s.activeTable {
		return nil
	}
	return s.schema_.Tables[s.activeTable]
}

func (s *MySQLSource) TableNames() []string {
	if s.schema_ == nil {
		return nil
	}
	names := make([]string, len(s.schema_.Tables))
	for i, t := range s.schema_.Tables {
		names[i] = t.Name
	}
	return names
}

func (s *MySQLSource) Encoding() string { return "UTF8" }

func (s *MySQLSource) DataIsPreformatted() bool { return false }

func (s *MySQLSource) Clone() source.Source {
	clone := *s
	clone.db = nil // each clone gets its own connection
	// Keep catalog and schema_ shared (read-only) so shards can MapRows
	return &clone
}

// CopyColumnList returns the column list for COPY command.
func (s *MySQLSource) CopyColumnList() []string {
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
// For MySQL, this uses primary key range sharding.
func (s *MySQLSource) ConcurrencySupport(ctx context.Context, concurrency int) ([]source.Source, error) {
	if concurrency <= 1 || s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return nil, nil
	}

	t := s.ActiveTable()

	// Find integer primary key
	var pkCol *catalog.Column
	var pkName string
	for _, col := range t.Columns {
		if col.IsPK {
			pkName = col.Name
			pkCol = col
			break
		}
	}
	if pkCol == nil {
		return nil, nil // no PK, can't shard
	}

	// Get min/max
	query := fmt.Sprintf("SELECT MIN(`%s`), MAX(`%s`) FROM `%s`", pkName, pkName, t.Name)
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
			hi = max.Int64 + 1 // include the last row
		}
		clone := s.Clone().(*MySQLSource)
		clone.whereClause = fmt.Sprintf("`%s` >= %d AND `%s` < %d", pkName, lo, pkName, hi)
		if err := clone.Connect(ctx); err != nil {
			return nil, fmt.Errorf("clone connect: %w", err)
		}
		sources = append(sources, clone)
	}

	return sources, nil
}

// MapRows reads all rows from the MySQL table and calls processRow for each.
func (s *MySQLSource) MapRows(ctx context.Context, processRow func(source.Row) error) error {
	if s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return fmt.Errorf("no table metadata: call FetchMetadata first")
	}

	t := s.ActiveTable()

	// Build SELECT with proper quoting
	colNames := make([]string, len(t.Columns))
	colTypes := make([]string, len(t.Columns))
	for i, col := range t.Columns {
		if cast.MustUseSTAsText(col.SourceType) {
			colNames[i] = fmt.Sprintf("ST_AsText(`%s`) AS `%s`", col.Name, col.Name)
		} else {
			colNames[i] = fmt.Sprintf("`%s`", col.Name)
		}
		colTypes[i] = col.SourceType
	}

	query := fmt.Sprintf("SELECT %s FROM `%s`", strings.Join(colNames, ", "), t.Name)
	if s.whereClause != "" {
		query += " WHERE " + s.whereClause
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mysql query: %w", err)
	}
	defer rows.Close()

	// Build column value slice for scanning
	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("mysql columns: %w", err)
	}
	values := make([]interface{}, len(cols))

	for rows.Next() {
		// Create scanners for each column
		scanTargets := make([]interface{}, len(cols))
		for i := range values {
			scanTargets[i] = &values[i]
		}

		if err := rows.Scan(scanTargets...); err != nil {
			return fmt.Errorf("mysql scan: %w", err)
		}

		// Convert to source.Row
		row := make(source.Row, len(cols))
		for i, v := range values {
			row[i] = convertMySQLValue(v, colTypes[i])
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

// convertMySQLValue converts a MySQL driver value to a standard Go value
// suitable for COPY formatting.
func convertMySQLValue(v interface{}, colType string) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case []byte:
		// MySQL driver returns strings as []byte for most types
		// Return as string for text types, keep as []byte for binary
		if isBinaryType(colType) {
			return val
		}
		return string(val)
	case int32:
		return int64(val)
	case int16:
		return int64(val)
	case int8:
		return int64(val)
	case uint64:
		return int64(val) // safe for COPY text format
	case float32:
		return float64(val)
	case time.Time:
		return val.Format("2006-01-02 15:04:05-07")
	default:
		return val
	}
}

func isBinaryType(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// DbSource interface
// ---------------------------------------------------------------------------

// FetchMetadata reads the MySQL schema into the catalog.
func (s *MySQLSource) FetchMetadata(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("not connected: call Connect first")
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
func (s *MySQLSource) PrepareTarget(ctx context.Context, opts source.PrepareOptions) error {
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
func (s *MySQLSource) CompleteTarget(ctx context.Context, opts source.CompleteOptions) error {
	if s.pool == nil || s.schema_ == nil {
		return nil
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire target connection: %w", err)
	}
	defer conn.Release()

	// Ensure pg_trgm is available for MySQL FULLTEXT → PG GIN indexes
	if opts.CreateIndexes {
		if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
			return fmt.Errorf("create extension pg_trgm: %w", err)
		}
	}

	for _, t := range s.schema_.Tables {
		if opts.CreateIndexes {
			for _, idx := range t.Indexes {
				sql := idx.CreateIndexSQL()
				if _, err := conn.Exec(ctx, sql); err != nil {
					return fmt.Errorf("create index %s: %w\nSQL: %s", idx.Name, err, sql)
				}
			}
		}

		if opts.ForeignKeys {
			for _, fk := range t.ForeignKeys {
				sql := fk.CreateFKeySQL()
				if _, err := conn.Exec(ctx, sql); err != nil {
					return fmt.Errorf("create fk %s: %w\nSQL: %s", fk.Name, err, sql)
				}
			}
		}

		if opts.ResetSequences {
			if err := s.resetSequences(ctx, conn, t); err != nil {
				return err
			}
		}

		if opts.Comments {
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

// resetSequences sets PostgreSQL sequences to MAX(pk) for auto_increment columns.
func (s *MySQLSource) resetSequences(ctx context.Context, conn *pgxpool.Conn, t *catalog.Table) error {
	for _, col := range t.Columns {
		if col.IsAutoInc {
			seqName := fmt.Sprintf("%s_%s_seq", t.Name, col.Name)
			sql := fmt.Sprintf("SELECT setval('%s', COALESCE((SELECT MAX(%s) FROM %s), 1))",
				seqName, col.Name, t.QualifiedName())
			if _, err := conn.Exec(ctx, sql); err != nil {
				// Sequence might not exist for serial/bigserial columns,
				// or might have a different naming convention
				continue
			}
		}
	}
	return nil
}

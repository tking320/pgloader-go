// Package sqlite implements SQLite source support for pgloader.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/catalog"
	"github.com/tking320/pgloader-go/internal/source"
)

// SQLiteSource implements source.Source and source.DbSource for SQLite databases.
type SQLiteSource struct {
	// SQLite connection
	filename string // path to .sqlite file, or ":memory:"
	db       *sql.DB

	// Target PostgreSQL connection
	pool *pgxpool.Pool

	// Target schema/table
	schema string
	table  string

	// Schema catalog (populated by FetchMetadata)
	catalog  *catalog.Catalog
	schema_  *catalog.Schema
	seqFound bool // true if sqlite_sequence table exists

	// CAST engine
	castEngine *cast.Engine

	// Concurrency
	whereClause string
	activeTable int

	// Table filtering
	includingOnly []string
	excluding     []string
}

// New creates a SQLiteSource.
func New(filename, schema, table string, pool *pgxpool.Pool, castEngine *cast.Engine) *SQLiteSource {
	return &SQLiteSource{
		filename:    filename,
		schema:      schema,
		table:       table,
		pool:        pool,
		castEngine:  castEngine,
	}
}

// Connect opens a connection to the SQLite database.
func (s *SQLiteSource) Connect(ctx context.Context) error {
	db, err := sql.Open("sqlite3", s.filename)
	if err != nil {
		return fmt.Errorf("sqlite open: %w", err)
	}
	// SQLite does not support concurrent writes well;
	// single connection is sufficient.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite ping: %w", err)
	}

	// Enable foreign key introspection
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("sqlite pragma foreign_keys: %w", err)
	}

	s.db = db
	return nil
}

// Close closes the SQLite connection.
func (s *SQLiteSource) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SetTableFilters configures INCLUDING/EXCLUDING table name patterns.
func (s *SQLiteSource) SetTableFilters(including, excluding []string) {
	s.includingOnly = including
	s.excluding = excluding
}

// ---------------------------------------------------------------------------
// Source interface
// ---------------------------------------------------------------------------

func (s *SQLiteSource) TableName() string {
	if t := s.ActiveTable(); t != nil {
		return t.Name
	}
	return s.table
}

func (s *SQLiteSource) SchemaName() string { return s.schema }

func (s *SQLiteSource) SetActiveTable(name string) error {
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

func (s *SQLiteSource) ActiveTable() *catalog.Table {
	if s.schema_ == nil || len(s.schema_.Tables) <= s.activeTable {
		return nil
	}
	return s.schema_.Tables[s.activeTable]
}

func (s *SQLiteSource) TableNames() []string {
	if s.schema_ == nil {
		return nil
	}
	names := make([]string, len(s.schema_.Tables))
	for i, t := range s.schema_.Tables {
		names[i] = t.Name
	}
	return names
}

func (s *SQLiteSource) Encoding() string { return "UTF8" }
func (s *SQLiteSource) DataIsPreformatted() bool { return false }

func (s *SQLiteSource) Clone() source.Source {
	clone := *s
	clone.db = nil
	return &clone
}

func (s *SQLiteSource) CopyColumnList() []string {
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

// ConcurrencySupport returns nil — SQLite does not support concurrent reads.
func (s *SQLiteSource) ConcurrencySupport(ctx context.Context, concurrency int) ([]source.Source, error) {
	return nil, nil
}

// MapRows reads all rows from the active SQLite table and calls processRow for each.
func (s *SQLiteSource) MapRows(ctx context.Context, processRow func(source.Row) error) error {
	if s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return fmt.Errorf("no table metadata: call FetchMetadata first")
	}

	t := s.ActiveTable()

	// Build SELECT with proper quoting
	colNames := make([]string, len(t.Columns))
	for i, col := range t.Columns {
		colNames[i] = quoteIdent(col.Name)
	}

	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(colNames, ", "), quoteIdent(t.Name))
	if s.whereClause != "" {
		query += " WHERE " + s.whereClause
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("sqlite query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("sqlite columns: %w", err)
	}
	values := make([]interface{}, len(cols))

	for rows.Next() {
		scanTargets := make([]interface{}, len(cols))
		for i := range values {
			scanTargets[i] = &values[i]
		}

		if err := rows.Scan(scanTargets...); err != nil {
			return fmt.Errorf("sqlite scan: %w", err)
		}

		row := make(source.Row, len(cols))
		for i, v := range values {
			row[i] = convertSQLiteValue(v)
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

// quoteIdent quotes a SQL identifier, doubling any embedded double-quotes.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// convertSQLiteValue normalizes sqlite3 driver return values to standard Go types.
func convertSQLiteValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case int64:
		return val
	case float64:
		return val
	case string:
		return val
	case []byte:
		return string(val) // SQLite TEXT/BLOB: return as string for COPY text format
	case time.Time:
		return val.Format("2006-01-02 15:04:05.999999-07")
	case bool:
		if val {
			return int64(1)
		}
		return int64(0)
	default:
		return val
	}
}

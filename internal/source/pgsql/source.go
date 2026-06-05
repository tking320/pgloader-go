// Package pgsql implements PostgreSQL source support for pgloader.
// It connects to a source PostgreSQL database, introspects the schema,
// reads data via COPY ... TO STDOUT, and streams it to the target
// PostgreSQL database for COPY ... FROM STDIN.
package pgsql

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/catalog"
	"github.com/tking320/pgloader-go/internal/source"
)

// PgSQLSource implements source.Source and source.DbSource for PostgreSQL sources.
type PgSQLSource struct {
	// Source PostgreSQL connection
	srcURL  string
	srcPool *pgxpool.Pool

	// Target PostgreSQL connection (for PrepareTarget/CompleteTarget)
	tgtPool *pgxpool.Pool

	// Source identification
	srcSchema string // source schema name (defaults to "public")
	table     string // target table name

	// Schema catalog (populated by FetchMetadata)
	catalog *catalog.Catalog
	schema_ *catalog.Schema

	// CAST engine
	castEngine *cast.Engine

	// Concurrency sharding
	whereClause string // WHERE clause for sharded reads
	activeTable int    // index into schema_.Tables for MapRows
}

// New creates a PgSQLSource.
func New(srcURL, srcSchema, table string, tgtPool *pgxpool.Pool, castEngine *cast.Engine) *PgSQLSource {
	return &PgSQLSource{
		srcURL:     srcURL,
		srcSchema:  srcSchema,
		table:      table,
		tgtPool:    tgtPool,
		castEngine: castEngine,
	}
}

// Connect opens a connection to the source PostgreSQL database.
func (s *PgSQLSource) Connect(ctx context.Context) error {
	pool, err := pgxpool.New(ctx, s.srcURL)
	if err != nil {
		return fmt.Errorf("pgsql source connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("pgsql source ping: %w", err)
	}
	s.srcPool = pool
	return nil
}

// Close closes the source PostgreSQL connection pool.
func (s *PgSQLSource) Close() error {
	if s.srcPool != nil {
		s.srcPool.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Source interface
// ---------------------------------------------------------------------------

func (s *PgSQLSource) TableName() string { return s.table }

func (s *PgSQLSource) SetActiveTable(name string) error {
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

func (s *PgSQLSource) ActiveTable() *catalog.Table {
	if s.schema_ == nil || len(s.schema_.Tables) <= s.activeTable {
		return nil
	}
	return s.schema_.Tables[s.activeTable]
}

func (s *PgSQLSource) TableNames() []string {
	if s.schema_ == nil {
		return nil
	}
	names := make([]string, len(s.schema_.Tables))
	for i, t := range s.schema_.Tables {
		names[i] = t.Name
	}
	return names
}

func (s *PgSQLSource) Encoding() string { return "UTF8" }

// DataIsPreformatted returns true — PG-to-PG data is already in COPY format
// since we read via COPY ... TO STDOUT and write via COPY ... FROM STDIN.
func (s *PgSQLSource) DataIsPreformatted() bool { return true }

func (s *PgSQLSource) Clone() source.Source {
	clone := *s
	clone.srcPool = nil // each clone gets its own connection
	// Keep catalog and schema_ shared (read-only) so shards can MapRows
	return &clone
}

// CopyColumnList returns the column list for COPY command.
func (s *PgSQLSource) CopyColumnList() []string {
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

// ConcurrencySupport implements PK range sharding for PostgreSQL sources.
func (s *PgSQLSource) ConcurrencySupport(ctx context.Context, concurrency int) ([]source.Source, error) {
	if concurrency <= 1 || s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return nil, nil
	}

	t := s.ActiveTable()

	// Find integer primary key
	var pkName string
	var pkCol *catalog.Column
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

	// Get min/max via source connection
	conn, err := s.srcPool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire source conn: %w", err)
	}
	defer conn.Release()

	query := fmt.Sprintf("SELECT min(%s), max(%s) FROM %s",
		quoteIdent(pkName), quoteIdent(pkName), t.QualifiedName())
	var min, max *int64
	if err := conn.QueryRow(ctx, query).Scan(&min, &max); err != nil {
		return nil, fmt.Errorf("get pk range: %w", err)
	}
	if min == nil || max == nil {
		return nil, nil // empty table
	}

	rangeSize := (*max - *min) / int64(concurrency)
	if rangeSize < 1 {
		rangeSize = 1
	}

	var sources []source.Source
	for i := 0; i < concurrency; i++ {
		lo := *min + int64(i)*rangeSize
		hi := lo + rangeSize
		if i == concurrency-1 {
			hi = *max + 1 // include the last row
		}
		clone := s.Clone().(*PgSQLSource)
		clone.whereClause = fmt.Sprintf("%s >= %d AND %s < %d",
			quoteIdent(pkName), lo, quoteIdent(pkName), hi)
		if err := clone.Connect(ctx); err != nil {
			return nil, fmt.Errorf("clone connect: %w", err)
		}
		sources = append(sources, clone)
	}

	return sources, nil
}

// ---------------------------------------------------------------------------
// DbSource: PrepareTarget
// ---------------------------------------------------------------------------

// PrepareTarget creates or prepares the target PostgreSQL tables.
func (s *PgSQLSource) PrepareTarget(ctx context.Context, opts source.PrepareOptions) error {
	if s.tgtPool == nil || s.schema_ == nil {
		return nil
	}

	conn, err := s.tgtPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire target connection: %w", err)
	}
	defer conn.Release()

	// Create schemas
	if opts.CreateSchemas {
		for _, sch := range s.catalog.Schemas {
			if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdent(sch.Name))); err != nil {
				return fmt.Errorf("create schema %s: %w", sch.Name, err)
			}
		}
	}

	// Create extensions
	for _, ext := range s.schema_.Extensions {
		sql := fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %s", quoteIdent(ext.Name))
		if ext.Schema != "" {
			sql += fmt.Sprintf(" SCHEMA %s", quoteIdent(ext.Schema))
		}
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("create extension %s: %w", ext.Name, err)
		}
	}

	// Drop custom types when IncludeDrop is set
	if opts.IncludeDrop {
		for _, typ := range s.schema_.Types {
			conn.Exec(ctx, fmt.Sprintf("DROP TYPE IF EXISTS %s.%s CASCADE", quoteIdent(typ.Schema), quoteIdent(typ.Name)))
		}
	}

	// Create custom types (enums, domains, composites)
	for _, typ := range s.schema_.Types {
		sql := typeCreateSQL(typ)
		if sql != "" {
			if _, err := conn.Exec(ctx, sql); err != nil {
				return fmt.Errorf("create type %s: %w\nSQL: %s", typ.Name, err, sql)
			}
		}
	}

	// Create user-defined functions (before triggers and tables that use them)
	for _, fn := range s.schema_.Functions {
		if _, err := conn.Exec(ctx, fn.Definition); err != nil {
			return fmt.Errorf("create function %s: %w\nSQL: %s", fn.Name, err, fn.Definition)
		}
	}

	// Create sequences referenced by table defaults (before tables that use them)
	// Skip identity columns — PostgreSQL auto-creates those sequences.
	seqNames := make(map[string]bool)
	for _, t := range s.schema_.Tables {
		for _, c := range t.Columns {
			if c.SequenceName != "" && c.ExtraDDL == "" {
				seqNames[c.SequenceName] = true
			}
		}
	}
	for seqName := range seqNames {
		sql := fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s.%s", quoteIdent(s.schema_.Name), quoteIdent(seqName))
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("create sequence %s: %w\nSQL: %s", seqName, err, sql)
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
			// Add PARTITION BY clause for partitioned tables
			if t.PartitionInfo != nil {
				sql = addPartitionClause(sql, t.PartitionInfo)
			}
			// Add INHERITS clause for table inheritance
			if t.ParentTable != "" {
				sql = strings.TrimRight(sql, ")")
				sql += fmt.Sprintf(") INHERITS (%s)", quoteIdent(t.ParentTable))
			}
			if _, err := conn.Exec(ctx, sql); err != nil {
				return fmt.Errorf("create table %s: %w\nSQL: %s", t.Name, err, sql)
			}
			// Create child partitions for partitioned tables
			if t.PartitionInfo != nil && len(t.PartitionInfo.Partitions) > 0 {
				for _, child := range t.PartitionInfo.Partitions {
					childSQL := fmt.Sprintf("CREATE TABLE %s PARTITION OF %s %s",
						quoteIdent(child.Name), t.QualifiedName(), child.Bound)
					if _, err := conn.Exec(ctx, childSQL); err != nil {
						return fmt.Errorf("create partition %s: %w\nSQL: %s", child.Name, err, childSQL)
					}
				}
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

// addPartitionClause appends PARTITION BY to a CREATE TABLE statement.
func addPartitionClause(createSQL string, pi *catalog.PartitionInfo) string {
	// Remove trailing semicolon if present
	createSQL = strings.TrimRight(createSQL, ";")

	// Build partition key
	var keys []string
	for i, col := range pi.KeyColumns {
		if col != "" {
			keys = append(keys, quoteIdent(col))
		} else if i < len(pi.KeyExpressions) && pi.KeyExpressions[i] != "" {
			keys = append(keys, pi.KeyExpressions[i])
		}
	}
	if len(keys) == 0 {
		return createSQL
	}

	return fmt.Sprintf("%s PARTITION BY %s (%s)", createSQL, pi.Strategy, strings.Join(keys, ", "))
}

// ---------------------------------------------------------------------------
// DbSource: CompleteTarget
// ---------------------------------------------------------------------------

// CompleteTarget creates indexes, foreign keys, and resets sequences.
func (s *PgSQLSource) CompleteTarget(ctx context.Context, opts source.CompleteOptions) error {
	if s.tgtPool == nil || s.schema_ == nil {
		return nil
	}

	conn, err := s.tgtPool.Acquire(ctx)
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

	// Second pass: create all foreign keys (after indexes so PKs exist)
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

	// Third pass: reset sequences and create triggers
	for _, t := range s.schema_.Tables {
		if opts.ResetSequences {
			if err := s.resetSequences(ctx, conn, t); err != nil {
				return err
			}
		}

		if opts.CreateTriggers {
			for _, trig := range t.Triggers {
				sql := trigCreateSQL(trig, t)
				if sql != "" {
					if _, err := conn.Exec(ctx, sql); err != nil {
						return fmt.Errorf("create trigger %s: %w\nSQL: %s", trig.Name, err, sql)
					}
				}
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

// resetSequences sets PostgreSQL sequences to MAX(pk) for serial/bigserial columns.
func (s *PgSQLSource) resetSequences(ctx context.Context, conn *pgxpool.Conn, t *catalog.Table) error {
	for _, col := range t.Columns {
		if col.IsAutoInc && col.SequenceName != "" {
			sql := fmt.Sprintf("SELECT setval('%s', COALESCE((SELECT MAX(%s) FROM %s), 1))",
				col.SequenceName, quoteIdent(col.Name), t.QualifiedName())
			if _, err := conn.Exec(ctx, sql); err != nil {
				// Sequence might not exist; skip
				continue
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Identifier quoting
// ---------------------------------------------------------------------------

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

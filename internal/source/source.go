// Package source defines the core interfaces for all data sources.
// This corresponds to the generic source API in pgloader's src/sources/common/api.lisp.
package source

import (
	"context"
)

// Row represents a single row of data as an ordered slice of values.
type Row []any

// TransformFn is a function that transforms a single column value.
type TransformFn func(ctx context.Context, val any) (any, error)

// Field describes a source field and its mapping to a target column.
type Field struct {
	Name      string
	SourceCol int // column index in source data
	Cast      CastRule
	Transform TransformFn
}

// CastRule describes how to convert a source type to a PostgreSQL type.
type CastRule struct {
	SourceType  string
	TargetType  string
	DropTypemod bool
	DropDefault bool
	DropNotNull bool
	KeepDefault bool
	KeepNotNull bool
	Transform   string // named transform function
	Default     string // default value expression
}

// ---------------------------------------------------------------------------
// Source interface and hierarchy
// ---------------------------------------------------------------------------

// Source is the common interface implemented by all data sources.
// It corresponds to the `copy` class in pgloader.
type Source interface {
	// TableName returns the target table name.
	TableName() string

	// SchemaName returns the target schema for the active table.
	// For single-schema sources this is the configured schema.
	// For multi-schema sources (MSSQL), this returns the active table's schema.
	SchemaName() string

	// MapRows reads all rows from the source and calls processRow for each.
	// This is the primary data reading method.
	MapRows(ctx context.Context, processRow func(Row) error) error

	// CopyColumnList returns the list of column names for the COPY command.
	CopyColumnList() []string

	// DataIsPreformatted returns true if data is already in COPY format.
	DataIsPreformatted() bool

	// ConcurrencySupport returns sharded copies for parallel loading,
	// or nil if the source doesn't support concurrency.
	ConcurrencySupport(ctx context.Context, concurrency int) ([]Source, error)

	// Clone creates an independent copy of this source (used for concurrency).
	Clone() Source

	// Encoding returns the source encoding (empty = assume UTF-8).
	Encoding() string
}

// FileSource is the base for file-based sources (CSV, fixed-width, COPY format, DBF, IXF).
// It corresponds to the `md-copy` class in pgloader.
type FileSource struct {
	FilePath   string
	TargetName string
	Fields     []Field
	SkipLines  int
	HasHeader  bool
	Enc        string
}

func (fs *FileSource) TableName() string        { return fs.TargetName }
func (fs *FileSource) SchemaName() string       { return "" }
func (fs *FileSource) Encoding() string         { return fs.Enc }
func (fs *FileSource) DataIsPreformatted() bool { return false }

// DbSource is the interface for database-backed sources (MySQL, SQLite, MSSQL, PG-to-PG).
// It corresponds to the `db-copy` class in pgloader.
type DbSource interface {
	Source

	// FetchMetadata reads the full schema from the source database.
	FetchMetadata(ctx context.Context) error

	// PrepareTarget creates/drops/truncates tables in the target database.
	PrepareTarget(ctx context.Context, opts PrepareOptions) error

	// CompleteTarget creates indexes, foreign keys, triggers, etc.
	CompleteTarget(ctx context.Context, opts CompleteOptions) error

	// TableNames returns all table names discovered during FetchMetadata.
	TableNames() []string

	// SetActiveTable switches the active table for MapRows/CopyColumnList.
	SetActiveTable(name string) error
}

// PrepareOptions controls schema preparation behavior.
type PrepareOptions struct {
	Truncate         bool
	CreateTables     bool
	CreateSchemas    bool
	DropIndexes      bool
	IncludeDrop      bool
	MaterializeViews []string
}

// CompleteOptions controls schema finalization behavior.
type CompleteOptions struct {
	ForeignKeys    bool
	CreateIndexes  bool
	CreateTriggers bool
	ResetSequences bool
	Comments       bool
}

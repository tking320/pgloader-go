// Package config provides global configuration parameters for pgloader.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Config holds all configurable parameters for pgload operations.
type Config struct {
	// Batch settings
	BatchSize  int   // max rows per batch (default 50000)
	BatchBytes int64 // max bytes per batch (default 32MB)

	// Concurrency
	Concurrency      int   // number of concurrent writer goroutines (default 1)
	RowsPerRange     int64 // rows per range for DB source sharding (default 50000)
	PrefetchRows     int64 // rows to prefetch from source (default 10000)
	MaxParallelIndex int   // max parallel CREATE INDEX (default 4)

	// Behavior
	OnErrorStop bool // stop on first error instead of continuing
	DryRun      bool // only check connections, don't load
	Truncate    bool // truncate target tables before load
	SchemaOnly  bool // only migrate schema, skip data
	DataOnly    bool // only load data, skip schema migration

	// Schema migration control
	CreateTables   bool // create tables on target (default true)
	CreateIndexes  bool // create indexes after data load (default true)
	ForeignKeys    bool // create foreign keys after data load (default true)
	IncludeDrop    bool // DROP TABLE IF EXISTS before CREATE
	ResetSequences bool // reset sequences to MAX(pk) after load (default true)
	CreateTriggers bool // create triggers on target (default true)
	Comments       bool // copy comments to target (default false)

	// Logging
	LogMinMessages    string // log level for log file (default "notice")
	ClientMinMessages string // log level for console (default "warning")
	LogFile           string // path to log file
	RootDir           string // root directory for output files (default "/tmp/pgload")
	SummaryFile       string // path to summary output

	// SSL
	NoSSLCertVerification bool // skip SSL certificate verification

	// CSV-specific defaults
	DefaultDelimiter rune // default: ','
	DefaultQuote     rune // default: '"'
	DefaultEscape    rune // default: '"'

	// GUCs are PostgreSQL GUC settings to apply on the target session.
	GUCs []string // e.g. "work_mem = '32 MB'", "maintenance_work_mem = '128 MB'"

	// BeforeFile is a SQL script to run before loading data.
	BeforeFile string
	// AfterFile is a SQL script to run after loading data.
	AfterFile string

	// CastRules is an optional string specifying user-defined cast rules.
	CastRules string
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		BatchSize:         50000,
		BatchBytes:        32 * 1024 * 1024, // 32 MB
		Concurrency:       1,
		RowsPerRange:      50000,
		PrefetchRows:      10000,
		MaxParallelIndex:  4,
		CreateTables:      true,
		CreateIndexes:     true,
		ForeignKeys:       true,
		ResetSequences:    true,
		CreateTriggers:    true,
		LogMinMessages:    "notice",
		ClientMinMessages: "warning",
		RootDir:           "/tmp/pgload",
		DefaultDelimiter:  ',',
		DefaultQuote:      '"',
		DefaultEscape:     '"',
	}
}

// String returns a human-readable summary of the config.
func (c *Config) String() string {
	return fmt.Sprintf(
		"batch=%d/%d concurrency=%d on_error_stop=%v dry_run=%v",
		c.BatchSize, c.BatchBytes, c.Concurrency, c.OnErrorStop, c.DryRun,
	)
}

// ApplyWithOption parses a pgloader WITH option string and applies it to Config.
// Supports the common pgloader WITH options:
//
//	create tables / no create tables
//	create indexes / no create indexes
//	include drop / no include drop
//	foreign keys / no foreign keys
//	truncate / no truncate
//	drop indexes / no drop indexes
//	reset sequences / no reset sequences
//	triggers / no triggers
//	schema only / data only
//	batch size = N
//	batch concurrency = N
//	workers = N
//	prefetch rows = N
//	concurrency = N
//	batch rows per range = N
func (c *Config) ApplyWithOption(opt string) error {
	opt = strings.TrimSpace(opt)
	if opt == "" {
		return nil
	}

	// Key = value style options
	if parts := strings.SplitN(opt, "=", 2); len(parts) == 2 {
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid WITH option %q: %q is not a number", opt, val)
		}
		switch key {
		case "batch size":
			c.BatchSize = n
		case "batch concurrency", "workers":
			c.Concurrency = n
		case "prefetch rows":
			c.PrefetchRows = int64(n)
		case "concurrency":
			c.Concurrency = n
		case "batch rows per range":
			c.RowsPerRange = int64(n)
		default:
			return fmt.Errorf("unknown WITH option: %q", opt)
		}
		return nil
	}

	// Boolean toggle options
	switch opt {
	case "create tables":
		c.CreateTables = true
	case "no create tables":
		c.CreateTables = false
	case "create indexes":
		c.CreateIndexes = true
	case "no create indexes":
		c.CreateIndexes = false
	case "include drop":
		c.IncludeDrop = true
	case "no include drop":
		c.IncludeDrop = false
	case "foreign keys":
		c.ForeignKeys = true
	case "no foreign keys":
		c.ForeignKeys = false
	case "truncate":
		c.Truncate = true
	case "no truncate":
		c.Truncate = false
	case "drop indexes":
		c.IncludeDrop = true
	case "no drop indexes":
		c.IncludeDrop = false
	case "reset sequences":
		c.ResetSequences = true
	case "no reset sequences":
		c.ResetSequences = false
	case "triggers":
		c.CreateTriggers = true
	case "no triggers":
		c.CreateTriggers = false
	case "schema only":
		c.SchemaOnly = true
		c.DataOnly = false
	case "data only":
		c.DataOnly = true
		c.SchemaOnly = false
	case "on error stop":
		c.OnErrorStop = true
	case "no on error stop":
		c.OnErrorStop = false
	case "comments":
		c.Comments = true
	case "no comments":
		c.Comments = false
	default:
		return fmt.Errorf("unknown WITH option: %q", opt)
	}
	return nil
}

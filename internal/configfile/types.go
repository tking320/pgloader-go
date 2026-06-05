// Package configfile parses pgloader .load configuration files and executes
// the data loading commands they define.
package configfile

// SourceType identifies the source database type.
type SourceType int

const (
	SourceMySQL      SourceType = iota // MySQL -> PostgreSQL
	SourcePostgreSQL                   // PostgreSQL -> PostgreSQL
	SourceCSV                          // CSV file -> PostgreSQL
	SourceSQLite                       // SQLite -> PostgreSQL
)

// LoadCommand represents a single LOAD command parsed from a .load file.
type LoadCommand struct {
	// Source type
	LoadType SourceType

	// SourceURI is the FROM URI (e.g. "mysql://user:pass@host/dbname").
	SourceURI string

	// TargetURI is the INTO PostgreSQL URI.
	TargetURI string

	// File source fields (for CSV and other file sources).
	FilePath     string // path to the source file
	TargetTable  string // TARGET TABLE name
	TargetSchema string // target schema (defaults to "public")

	// SourceColumns is the column list from the FROM clause (for CSV).
	SourceColumns []string

	// WITH options (e.g. "include drop", "create tables", "batch size = 10000").
	WITH []string

	// SET GUC settings (e.g. "maintenance_work_mem to '128MB'").
	SET []string

	// CAST rules as raw strings.
	CastRules []string

	// BEFORE LOAD DO SQL statements.
	BeforeLoad []string

	// AFTER LOAD DO SQL statements.
	AfterLoad []string

	// Table filtering.
	IncludingOnly    []string // INCLUDING ONLY TABLE NAMES MATCHING patterns
	Excluding        []string // EXCLUDING TABLE NAMES MATCHING patterns
	MaterializeViews []string // MATERIALIZE VIEWS
}

// ConfigFile represents a parsed .load file containing one or more commands.
type ConfigFile struct {
	Commands []*LoadCommand
}

// CLIOptions holds CLI flag overrides for config file execution.
// These are applied after config file settings, with CLI values winning.
type CLIOptions struct {
	Debug  bool     // --debug: verbose output + SQL tracing
	Quiet  bool     // --quiet: suppress progress output
	DryRun bool     // --dry-run: validate connections only
	With   []string // --with: applied after config file WITH options
	Set    []string // --set: applied after config file SET options
}

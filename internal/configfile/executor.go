package configfile

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/config"
	"github.com/tking320/pgloader-go/internal/monitor"
	"github.com/tking320/pgloader-go/internal/orchestrator"
	"github.com/tking320/pgloader-go/internal/pipeline"
	"github.com/tking320/pgloader-go/internal/source/csv"
	"github.com/tking320/pgloader-go/internal/source/mysql"
	"github.com/tking320/pgloader-go/internal/source/pgsql"
	"github.com/tking320/pgloader-go/internal/source/sqlite"
)

// ExecuteConfigFile parses and executes a .load configuration file.
// CLI options are applied on top of config file settings, CLI wins.
func ExecuteConfigFile(ctx context.Context, path string, cli CLIOptions) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	cf, err := ParseFile(string(data))
	if err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	for i, cmd := range cf.Commands {
		if err := ExecuteCommand(ctx, cmd, cli); err != nil {
			return fmt.Errorf("command %d: %w", i+1, err)
		}
	}

	return nil
}

// ExecuteCommand executes a single parsed LOAD command.
// CLI options override the command's settings.
func ExecuteCommand(ctx context.Context, cmd *LoadCommand, cli CLIOptions) error {
	cfg := config.DefaultConfig()

	// Apply WITH options from config file
	for _, opt := range cmd.WITH {
		if err := cfg.ApplyWithOption(opt); err != nil {
			return fmt.Errorf("apply WITH option %q: %w", opt, err)
		}
	}

	// Apply CLI WITH options on top (CLI wins)
	for _, opt := range cli.With {
		if err := cfg.ApplyWithOption(opt); err != nil {
			return fmt.Errorf("apply CLI --with %q: %w", opt, err)
		}
	}

	if cli.DryRun {
		return fmt.Errorf("dry-run mode not supported for config files")
	}

	// Create monitor: quiet mode uses stderr, debug enables verbose
	monOut := os.Stdout
	if cli.Quiet {
		monOut = os.Stderr
	}
	mon := monitor.NewMonitor(monOut, cli.Debug)

	// Connect to target PostgreSQL
	if cmd.TargetURI == "" {
		return fmt.Errorf("target database URI required in INTO clause")
	}
	var pool *pgxpool.Pool
	var err error
	if cli.Debug {
		poolCfg, parseErr := pgxpool.ParseConfig(cmd.TargetURI)
		if parseErr != nil {
			return fmt.Errorf("parse target URL: %w", parseErr)
		}
		poolCfg.ConnConfig.Tracer = &DebugTracer{}
		pool, err = pgxpool.NewWithConfig(ctx, poolCfg)
	} else {
		pool, err = pgxpool.New(ctx, cmd.TargetURI)
	}
	if err != nil {
		return fmt.Errorf("connect to target: %w", err)
	}
	defer pool.Close()

	// Apply SET GUC settings: config file first, then CLI overrides
	allSet := append(cmd.SET, cli.Set...)
	if len(allSet) > 0 {
		gucConn, err := pool.Acquire(ctx)
		if err != nil {
			return fmt.Errorf("acquire for GUC: %w", err)
		}
		for _, guc := range allSet {
			if _, err := gucConn.Exec(ctx, "SET SESSION "+guc); err != nil {
				gucConn.Release()
				return fmt.Errorf("set session %s: %w", guc, err)
			}
		}
		gucConn.Release()
	}

	mon.Start()

	// Execute BEFORE LOAD SQL statements
	if len(cmd.BeforeLoad) > 0 {
		mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: "before load"}
		if beforeErr := execSQL(ctx, pool, cmd.BeforeLoad); beforeErr != nil {
			mon.Stop()
			return fmt.Errorf("before load: %w", beforeErr)
		}
		mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "before load"}
	}

	schema := cmd.TargetSchema

	// Run the appropriate source loader
	switch cmd.LoadType {
	case SourceMySQL:
		err = execMySQL(ctx, cfg, mon, pool, cmd, schema)
	case SourcePostgreSQL:
		err = execPgsql(ctx, cfg, mon, pool, cmd, schema)
	case SourceCSV:
		err = execCSV(ctx, cfg, mon, pool, cmd, schema)
	case SourceSQLite:
		err = execSQLite(ctx, cfg, mon, pool, cmd, schema)
	default:
		return fmt.Errorf("unsupported load type: %v", cmd.LoadType)
	}

	// Execute AFTER LOAD SQL statements
	if len(cmd.AfterLoad) > 0 {
		mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: "after load"}
		afterErr := execSQL(ctx, pool, cmd.AfterLoad)
		mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "after load"}
		if afterErr != nil {
			mon.Stop()
			mon.WriteSummary()
			return fmt.Errorf("after load: %w", afterErr)
		}
	}

	mon.Stop()

	if err != nil {
		return err
	}

	mon.WriteSummary()

	return nil
}

// execMySQL runs a MySQL-to-PostgreSQL migration from a parsed command.
func execMySQL(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, cmd *LoadCommand, schema string) error {

	u, err := url.Parse(cmd.SourceURI)
	if err != nil {
		return fmt.Errorf("parse mysql URI: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := 3306
	portStr := u.Port()
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("invalid mysql port: %s", portStr)
		}
	}

	if u.User == nil {
		return fmt.Errorf("mysql URI must include a username")
	}
	user := u.User.Username()
	password, _ := u.User.Password()
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return fmt.Errorf("mysql database name required in URI path")
	}

	// Original pgloader behavior: when no TARGET SCHEMA specified,
	// use the MySQL database name as the PostgreSQL schema name.
	if schema == "" {
		schema = dbName
	}

	castEngine := cast.NewEngine(cast.MySQLDefaultRules())
	src := mysql.New(host, port, user, password, dbName, schema, "", pool, castEngine)

	if err := src.Connect(ctx); err != nil {
		return fmt.Errorf("mysql connect: %w", err)
	}
	defer src.Close()

	// Apply INCLUDING/EXCLUDING table name filters
	src.SetTableFilters(cmd.IncludingOnly, cmd.Excluding)

	mig := orchestrator.NewMigration(cfg, src, pool, mon, schema)
	return mig.Run(ctx)
}

// execPgsql runs a PostgreSQL-to-PostgreSQL migration from a parsed command.
func execPgsql(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, cmd *LoadCommand, schema string) error {

	castEngine := cast.NewEngine(cast.PgDefaultRules())
	src := pgsql.New(cmd.SourceURI, schema, "", pool, castEngine)

	if err := src.Connect(ctx); err != nil {
		return fmt.Errorf("pgsql connect: %w", err)
	}
	defer src.Close()

	mig := orchestrator.NewMigration(cfg, src, pool, mon, schema)
	return mig.Run(ctx)
}

// execCSV runs a CSV-to-PostgreSQL load from a parsed command.
func execCSV(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, cmd *LoadCommand, schema string) error {

	delimiter, hasHeader, skipLines := parseCSVWithOptions(cmd.WITH)

	opts := []csv.Option{
		csv.WithDelimiter(delimiter),
		csv.WithHeader(hasHeader),
		csv.WithSkipLines(skipLines),
	}

	if len(cmd.SourceColumns) > 0 {
		opts = append(opts, csv.WithColumns(cmd.SourceColumns))
	}

	src := csv.NewCSVSource(cmd.FilePath, cmd.TargetTable, opts...)

	pipe := pipeline.New(cfg, src, pool, mon, schema, cmd.TargetTable)
	return pipe.Run(ctx)
}

// execSQLite runs a SQLite-to-PostgreSQL migration from a parsed command.
func execSQLite(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, cmd *LoadCommand, schema string) error {

	// URI format: sqlite:///path/to/file.db
	// Remove the "sqlite://" prefix (3 slashes = absolute path, 2 = relative)
	path := strings.TrimPrefix(cmd.SourceURI, "sqlite://")
	if path == ":memory:" {
		// In-memory database (mostly for testing)
	}
	if path == "" {
		return fmt.Errorf("sqlite filename required in URI: %s", cmd.SourceURI)
	}

	if schema == "" {
		schema = "public"
	}

	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	src := sqlite.New(path, schema, "", pool, castEngine)

	if err := src.Connect(ctx); err != nil {
		return fmt.Errorf("sqlite connect: %w", err)
	}
	defer src.Close()

	// Apply INCLUDING/EXCLUDING table name filters
	src.SetTableFilters(cmd.IncludingOnly, cmd.Excluding)

	mig := orchestrator.NewMigration(cfg, src, pool, mon, schema)
	return mig.Run(ctx)
}

// parseCSVWithOptions extracts CSV-specific settings from WITH options.
func parseCSVWithOptions(withOpts []string) (delimiter rune, hasHeader bool, skipLines int) {
	delimiter = ','
	hasHeader = false
	skipLines = 0

	for _, opt := range withOpts {
		opt = strings.TrimSpace(opt)

		switch {
		case strings.HasPrefix(opt, "fields terminated by"):
			delimiter = extractQuotedChar(opt)
		case strings.HasPrefix(opt, "skip header"):
			skipLines = extractSkipCount(opt)
		case opt == "header":
			hasHeader = true
		}
	}

	return
}

// extractQuotedChar extracts a single character from a quoted string
// in an option like "fields terminated by ','".
func extractQuotedChar(opt string) rune {
	idx := strings.IndexByte(opt, '\'')
	if idx < 0 {
		return ','
	}
	rest := opt[idx+1:]
	if len(rest) == 0 || rest[0] == '\'' {
		return ','
	}
	// Handle escape sequences
	if len(rest) >= 2 && rest[0] == '\\' {
		switch rest[1] {
		case 't':
			return '\t'
		case 'n':
			return '\n'
		case 'r':
			return '\r'
		}
	}
	return rune(rest[0])
}

// extractSkipCount extracts the number from a "skip header = N" option.
func extractSkipCount(opt string) int {
	idx := strings.IndexByte(opt, '=')
	if idx < 0 {
		return 1 // default to 1 if just "skip header"
	}
	val := strings.TrimSpace(opt[idx+1:])
	n, err := strconv.Atoi(val)
	if err != nil {
		return 1
	}
	return n
}

// ---------------------------------------------------------------------------
// DebugTracer — pgx tracer for SQL logging in debug mode
// ---------------------------------------------------------------------------

// DebugTracer implements pgx tracer interfaces to log SQL in debug mode.
type DebugTracer struct{}

func (d *DebugTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	fmt.Fprintf(os.Stderr, "DEBUG SQL: %s\n", data.SQL)
	return ctx
}

func (d *DebugTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err != nil {
		fmt.Fprintf(os.Stderr, "DEBUG SQL ERROR: %v\n", data.Err)
	}
}

func (d *DebugTracer) TraceCopyFromStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	fmt.Fprintf(os.Stderr, "DEBUG SQL: COPY %s FROM STDIN\n", data.TableName)
	return ctx
}

func (d *DebugTracer) TraceCopyFromEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromEndData) {
	if data.Err != nil {
		fmt.Fprintf(os.Stderr, "DEBUG SQL ERROR: %v\n", data.Err)
	}
}

func (d *DebugTracer) TraceBatchStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (d *DebugTracer) TraceBatchQuery(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchQueryData) {
	fmt.Fprintf(os.Stderr, "DEBUG SQL: %s\n", data.SQL)
	if data.Err != nil {
		fmt.Fprintf(os.Stderr, "DEBUG SQL ERROR: %v\n", data.Err)
	}
}

func (d *DebugTracer) TraceBatchEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchEndData) {
}

func (d *DebugTracer) TracePrepareStart(ctx context.Context, conn *pgx.Conn, data pgx.TracePrepareStartData) context.Context {
	fmt.Fprintf(os.Stderr, "DEBUG SQL: PREPARE %s AS %s\n", data.Name, data.SQL)
	return ctx
}

func (d *DebugTracer) TracePrepareEnd(ctx context.Context, conn *pgx.Conn, data pgx.TracePrepareEndData) {
	if data.Err != nil {
		fmt.Fprintf(os.Stderr, "DEBUG SQL ERROR: %v\n", data.Err)
	}
}

func (d *DebugTracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	fmt.Fprintf(os.Stderr, "DEBUG SQL: connecting to %s\n", data.ConnConfig.ConnString())
	return ctx
}

func (d *DebugTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	if data.Err != nil {
		fmt.Fprintf(os.Stderr, "DEBUG SQL ERROR: %v\n", data.Err)
	}
}

// ---------------------------------------------------------------------------
// SQL execution helpers
// ---------------------------------------------------------------------------

// execSQL executes a list of SQL statements on the target pool.
func execSQL(ctx context.Context, pool *pgxpool.Pool, statements []string) error {
	if len(statements) == 0 {
		return nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	for _, stmt := range statements {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			if _, rbErr := conn.Exec(ctx, "ROLLBACK"); rbErr != nil {
				fmt.Fprintf(os.Stderr, "ROLLBACK failed: %v (original error: %v)\n", rbErr, err)
			}
			return fmt.Errorf("execute %q: %w", truncate(stmt, 80), err)
		}
	}
	if _, err := conn.Exec(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

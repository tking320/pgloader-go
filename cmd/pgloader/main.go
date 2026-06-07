// pgloader — A PostgreSQL data loading tool (Go port of pgloader).
//
// Usage:
//
//	pgloader [ option ... ] SOURCE TARGET
//	pgloader [ option ... ] command-file...
//
// Examples:
//
//	pgloader data.csv postgresql://localhost/mydb --table mytable
//	pgloader postgresql://host/source postgresql://host/target
//	pgloader mysql://user@host/dbname postgresql://localhost/target
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/tking320/pgloader-go/internal/cast"
	"github.com/tking320/pgloader-go/internal/config"
	"github.com/tking320/pgloader-go/internal/configfile"
	"github.com/tking320/pgloader-go/internal/monitor"
	"github.com/tking320/pgloader-go/internal/orchestrator"
	"github.com/tking320/pgloader-go/internal/pipeline"
	"github.com/tking320/pgloader-go/internal/source/csv"
	"github.com/tking320/pgloader-go/internal/source/mssql"
	"github.com/tking320/pgloader-go/internal/source/mysql"
	"github.com/tking320/pgloader-go/internal/source/pgsql"
	"github.com/tking320/pgloader-go/internal/source/sqlite"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := execute(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context) error {
	cfg := config.DefaultConfig()
	mon := monitor.NewMonitor(os.Stdout, false)

	var (
		sourceArg, dbURL, table, delimiter, logfile, enc, beforeFile, afterFile, castRules, sourceType string
		hasHeader, guess, quiet, debug                                                                 bool
		skipLines                                                                                      int
		colsFlag                                                                                       []string
		withOpts                                                                                       []string
		setOpts                                                                                        []string
		fkeys, includeDrop, dryRun                                                                     bool
	)

	rootCmd := &cobra.Command{
		Use:           "pgloader [ option ... ] SOURCE TARGET",
		Short:         "pgloader — PostgreSQL data loading tool",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long:          `pgloader loads data from various sources into PostgreSQL.`,
		Example: `  pgloader data.csv postgresql://localhost/mydb --table mytable
  pgloader postgresql://host/source postgresql://host/target
  pgloader mysql://user@host/dbname postgresql://localhost/target`,
		RunE: func(cmd *cobra.Command, args []string) error {
			totalStartTime := time.Now()
			if len(args) < 1 {
				return fmt.Errorf("source required: pgloader SOURCE TARGET")
			}
			sourceArg = args[0]

			// If the first argument is a .load config file, delegate to
			// the config file executor instead of using CLI arguments.
			// CLI flags still apply as overrides on top of config file settings.
			if strings.HasSuffix(sourceArg, ".load") {
				if len(args) > 1 {
					return fmt.Errorf("only one .load file supported per invocation")
				}
				// Read CLI flags for override
				loadDebug, _ := cmd.Flags().GetBool("debug")
				loadQuiet, _ := cmd.Flags().GetBool("quiet")
				loadDryRun, _ := cmd.Flags().GetBool("dry-run")
				loadWith, _ := cmd.Flags().GetStringSlice("with")
				loadSet, _ := cmd.Flags().GetStringSlice("set")
				loadFkeys, _ := cmd.Flags().GetBool("foreign-keys")
				loadIncludeDrop, _ := cmd.Flags().GetBool("include-drop")

				// Convert --foreign-keys and --include-drop to WITH strings
				if !loadFkeys {
					loadWith = append(loadWith, "no foreign keys")
				}
				if loadIncludeDrop {
					loadWith = append(loadWith, "include drop")
				}

				cli := configfile.CLIOptions{
					Debug:  loadDebug,
					Quiet:  loadQuiet,
					DryRun: loadDryRun,
					With:   loadWith,
					Set:    loadSet,
				}
				return configfile.ExecuteConfigFile(ctx, sourceArg, cli)
			}

			if guess && len(args) == 1 && !isURISource(sourceArg) {
				guessed, err := csv.GuessParams(sourceArg)
				if err != nil {
					return fmt.Errorf("guess failed: %w", err)
				}
				fmt.Printf("Delimiter: %q\n", guessed.Delimiter)
				fmt.Printf("Header:    %v\n", guessed.HasHeader)
				fmt.Printf("Skip:      %d\n", guessed.SkipLines)
				fmt.Printf("Columns:   %d\n", guessed.NumCols)
				return nil
			}

			if len(args) < 2 {
				return fmt.Errorf("target database required: pgloader SOURCE TARGET")
			}
			dbURL = args[1]

			table, _ = cmd.Flags().GetString("table")
			delimiter, _ = cmd.Flags().GetString("delimiter")
			hasHeader, _ = cmd.Flags().GetBool("header")
			skipLines, _ = cmd.Flags().GetInt("skip-lines")
			guess, _ = cmd.Flags().GetBool("guess")
			colsFlag, _ = cmd.Flags().GetStringSlice("columns")
			quiet, _ = cmd.Flags().GetBool("quiet")
			fkeys, _ = cmd.Flags().GetBool("foreign-keys")
			includeDrop, _ = cmd.Flags().GetBool("include-drop")
			debug, _ = cmd.Flags().GetBool("debug")
			dryRun, _ = cmd.Flags().GetBool("dry-run")
			logfile, _ = cmd.Flags().GetString("logfile")
			withOpts, _ = cmd.Flags().GetStringSlice("with")
			setOpts, _ = cmd.Flags().GetStringSlice("set")
			enc, _ = cmd.Flags().GetString("encoding")
			beforeFile, _ = cmd.Flags().GetString("before")
			afterFile, _ = cmd.Flags().GetString("after")
			castRules, _ = cmd.Flags().GetString("cast")
			sourceType, _ = cmd.Flags().GetString("type")

			// Connect to target database
			var (
				pool *pgxpool.Pool
				err  error
			)
			if debug {
				var poolCfg *pgxpool.Config
				poolCfg, err = pgxpool.ParseConfig(dbURL)
				if err != nil {
					return fmt.Errorf("parse target URL: %w", err)
				}
				poolCfg.ConnConfig.Tracer = &configfile.DebugTracer{}
				pool, err = pgxpool.NewWithConfig(ctx, poolCfg)
			} else {
				pool, err = pgxpool.New(ctx, dbURL)
			}
			if err != nil {
				return fmt.Errorf("connect to target: %w", err)
			}
			defer pool.Close()

			cfg.GUCs = setOpts

			// Apply PostgreSQL GUC settings on target
			if len(cfg.GUCs) > 0 {
				gucConn, err := pool.Acquire(ctx)
				if err != nil {
					return fmt.Errorf("acquire for GUC: %w", err)
				}
				for _, guc := range cfg.GUCs {
					if _, err := gucConn.Exec(ctx, "SET SESSION "+guc); err != nil {
						gucConn.Release()
						return fmt.Errorf("set %s: %w", guc, err)
					}
				}
				gucConn.Release()
			}

			cfg.DryRun = dryRun
			cfg.LogFile = logfile
			cfg.BeforeFile = beforeFile
			cfg.AfterFile = afterFile
			cfg.CastRules = castRules

			// Dry-run mode: verify connections and exit
			if dryRun {
				if err := pool.Ping(ctx); err != nil {
					return fmt.Errorf("dry-run: target unreachable: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Dry run: connections ok, not loading anything.\n")
				return nil
			}

			schema := "public"

			// Apply WITH options from --with flags
			for _, opt := range withOpts {
				if err := cfg.ApplyWithOption(opt); err != nil {
					return err
				}
			}

			// Apply CLI overrides to config
			cfg.ForeignKeys = fkeys
			cfg.IncludeDrop = includeDrop

			// Suppress summary output when quiet
			if quiet {
				mon = monitor.NewMonitor(os.Stderr, false)
			}

			// Enable debug output
			if debug {
				cfg.ClientMinMessages = "debug"
				mon = monitor.NewMonitor(os.Stdout, true)
			}

			mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: "before load"}

			// Execute before-load SQL script
			if err := execSQLFile(ctx, pool, cfg.BeforeFile); err != nil {
				return fmt.Errorf("before script: %w", err)
			}
			mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "before load"}

			mon.Start()

			// Run the appropriate source loader
			var loadErr error
			switch {
			case sourceType == "postgresql" || sourceType == "pg" ||
				(sourceType == "" && (strings.HasPrefix(sourceArg, "postgresql://") || strings.HasPrefix(sourceArg, "postgres://"))):
				loadErr = runPgsql(ctx, cfg, mon, pool, sourceArg, schema, table)
			case sourceType == "mysql" ||
				(sourceType == "" && strings.HasPrefix(sourceArg, "mysql://")):
				loadErr = runMySQL(ctx, cfg, mon, pool, sourceArg, schema, table, guess)
			case sourceType == "sqlite" ||
				(sourceType == "" && strings.HasPrefix(sourceArg, "sqlite://")):
				loadErr = runSQLite(ctx, cfg, mon, pool, sourceArg, schema, table)
			case sourceType == "mssql" || sourceType == "sqlserver" ||
				(sourceType == "" && (strings.HasPrefix(sourceArg, "mssql://") || strings.HasPrefix(sourceArg, "sqlserver://"))):
				loadErr = runMSSQL(ctx, cfg, mon, pool, sourceArg, schema, table)
			case sourceType == "csv" || (sourceType == "" && !isURISource(sourceArg)):
				// File source (CSV)
				if table == "" {
					return fmt.Errorf("target table required: use --table")
				}
				loadErr = runCSV(ctx, cfg, mon, pool, sourceArg, schema, table, delimiter, hasHeader, skipLines, guess, enc, colsFlag)
			default:
				return fmt.Errorf("unsupported source type: %s", sourceType)
			}

			if loadErr != nil {
				mon.Stop()
				return loadErr
			}

			mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: "after load"}
			// Execute after-load SQL script
			if err := execSQLFile(ctx, pool, cfg.AfterFile); err != nil {
				return fmt.Errorf("after script: %w", err)
			}
			mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "after load"}

			mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "all tables", Secs: time.Since(totalStartTime).Seconds()}

			mon.Stop()
			mon.WriteSummary()

			return nil
		},
	}

	// Custom help template: show examples after flags
	rootCmd.SetHelpTemplate(`{{with or .Long .Short }}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UseLine}}{{end}}{{if .HasAvailableFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .Example}}

Examples:
{{.Example}}{{end}}
`)

	// Define CLI flags
	rootCmd.Flags().String("table", "", "target table name")
	rootCmd.Flags().String("delimiter", ",", "CSV delimiter character")
	rootCmd.Flags().Bool("header", false, "CSV file has header row")
	rootCmd.Flags().Int("skip-lines", 0, "number of lines to skip at start of file")
	rootCmd.Flags().Bool("guess", false, "guess CSV parameters from sample")
	rootCmd.Flags().StringSlice("columns", nil, "column names for the table")
	rootCmd.Flags().Bool("quiet", false, "disable progress messages")
	rootCmd.Flags().Bool("debug", false, "enable debug SQL logging")
	rootCmd.Flags().Bool("dry-run", false, "validate connections and exit")
	rootCmd.Flags().String("logfile", "", "write log to file")
	rootCmd.Flags().StringSlice("with", nil, "WITH options")
	rootCmd.Flags().StringSlice("set", nil, "GUC settings")
	rootCmd.Flags().String("encoding", "", "source file encoding")
	rootCmd.Flags().String("before", "", "SQL file to run before load")
	rootCmd.Flags().String("after", "", "SQL file to run after load")
	rootCmd.Flags().String("cast", "", "cast rules file")
	rootCmd.Flags().String("type", "", "source type (csv, mysql, postgresql, pg, sqlite, mssql, sqlserver)")
	rootCmd.Flags().Bool("foreign-keys", true, "create foreign keys after data load")
	rootCmd.Flags().MarkHidden("foreign-keys")
	rootCmd.Flags().Bool("include-drop", false, "DROP TABLE IF EXISTS before CREATE TABLE")

	if err := rootCmd.Execute(); err != nil {
		return err
	}
	return nil
}

// execSQLFile reads and executes a SQL script file.
func execSQLFile(ctx context.Context, pool *pgxpool.Pool, path string) error {
	if path == "" {
		return nil
	}
	sql, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("execute %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Source type detection
// ---------------------------------------------------------------------------

// isURISource returns true if the source is a URI (not a file path).
func isURISource(s string) bool {
	for i := 0; i < len(s)-2; i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// MySQL source runner
// ---------------------------------------------------------------------------

func runMySQL(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, sourceArg, schema, table string, guess bool) error {

	// Parse mysql:// URI
	u, err := url.Parse(sourceArg)
	if err != nil {
		return fmt.Errorf("parse mysql URI: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	portStr := u.Port()
	port := 3306
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("invalid mysql port: %s", portStr)
		}
	}

	user := u.User.Username()
	password, _ := u.User.Password()
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return fmt.Errorf("mysql database name required in URI path")
	}

	// Create MySQL source
	castEngine := cast.NewEngine(cast.MySQLDefaultRules())
	src := mysql.New(host, port, user, password, dbName, schema, table, pool, castEngine)

	if err := src.Connect(ctx); err != nil {
		return fmt.Errorf("mysql connect: %w", err)
	}
	defer src.Close()

	mig := orchestrator.NewMigration(cfg, src, pool, mon, schema)
	return mig.Run(ctx)
}

// ---------------------------------------------------------------------------
// CSV source runner
// ---------------------------------------------------------------------------

func runCSV(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, sourceArg, schema, table, delimiter string,
	hasHeader bool, skipLines int, guess bool, enc string, colsFlag []string) error {

	opts := []csv.Option{
		csv.WithDelimiter(rune(delimiter[0])),
		csv.WithHeader(hasHeader),
		csv.WithSkipLines(skipLines),
	}

	if guess {
		guessed, err := csv.GuessParams(sourceArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not guess CSV params: %v\n", err)
		} else {
			opts = []csv.Option{
				csv.WithDelimiter(guessed.Delimiter),
				csv.WithHeader(guessed.HasHeader),
				csv.WithSkipLines(guessed.SkipLines),
			}
			fmt.Fprintf(os.Stderr, "Guessed: delimiter=%q header=%v skip=%d cols=%d\n",
				guessed.Delimiter, guessed.HasHeader, guessed.SkipLines, guessed.NumCols)
		}
	}

	if len(colsFlag) > 0 {
		opts = append(opts, csv.WithColumns(colsFlag))
	}
	if enc != "" {
		opts = append(opts, csv.WithEncoding(enc))
	}

	src := csv.NewCSVSource(sourceArg, table, opts...)

	pipe := pipeline.New(cfg, src, pool, mon, schema, table)
	return pipe.Run(ctx)
}

// ---------------------------------------------------------------------------
// PostgreSQL source runner
// ---------------------------------------------------------------------------

func runPgsql(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, sourceArg, schema, table string) error {

	castEngine := cast.NewEngine(cast.PgDefaultRules())
	src := pgsql.New(sourceArg, schema, table, pool, castEngine)

	if err := src.Connect(ctx); err != nil {
		return fmt.Errorf("pgsql connect: %w", err)
	}
	defer src.Close()

	mig := orchestrator.NewMigration(cfg, src, pool, mon, schema)
	return mig.Run(ctx)
}

// ---------------------------------------------------------------------------
// MSSQL source runner
// ---------------------------------------------------------------------------

func runMSSQL(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, sourceArg, schema, table string) error {

	u, err := url.Parse(sourceArg)
	if err != nil {
		return fmt.Errorf("parse mssql URI: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	portStr := u.Port()
	port := 1433
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("invalid mssql port: %s", portStr)
		}
	}

	user := u.User.Username()
	password, _ := u.User.Password()
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		dbName = u.Query().Get("database")
	}
	if dbName == "" {
		return fmt.Errorf("mssql database name required in URI path or ?database= parameter")
	}

	// Preserve MSSQL schema names (default to "dbo") matching original pgloader behavior.
	if schema == "" || schema == "public" {
		schema = "dbo"
	}

	castEngine := cast.NewEngine(cast.MSSQLDefaultRules())
	src := mssql.New(host, port, user, password, dbName, schema, table, pool, castEngine)

	if err := src.Connect(ctx); err != nil {
		return fmt.Errorf("mssql connect: %w", err)
	}
	defer src.Close()

	mig := orchestrator.NewMigration(cfg, src, pool, mon, schema)
	return mig.Run(ctx)
}

// ---------------------------------------------------------------------------
// SQLite source runner
// ---------------------------------------------------------------------------

func runSQLite(ctx context.Context, cfg *config.Config, mon *monitor.Monitor,
	pool *pgxpool.Pool, sourceArg, schema, table string) error {

	path := strings.TrimPrefix(sourceArg, "sqlite://")
	if path == "" {
		return fmt.Errorf("sqlite filename required in URI: %s", sourceArg)
	}

	if schema == "" || schema == "public" {
		schema = "public"
	}

	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	src := sqlite.New(path, schema, table, pool, castEngine)

	if err := src.Connect(ctx); err != nil {
		return fmt.Errorf("sqlite connect: %w", err)
	}
	defer src.Close()

	mig := orchestrator.NewMigration(cfg, src, pool, mon, schema)
	return mig.Run(ctx)
}

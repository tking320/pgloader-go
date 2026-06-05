# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
make build          # go build -o build/bin/pgloader ./cmd/pgloader
make test           # go test ./internal/... -v -count=1
make test-short     # go test ./internal/... -short -count=1
make lint           # go vet ./...
make fmt            # go fmt ./...
make clean          # rm -rf build/
go test ./internal/configfile/... -v -count=1 -run TestName   # single test
```

Integration tests (require Docker):
```bash
make check-pg-pg       # PostgreSQL -> PostgreSQL
make check-mysql-pg    # MySQL -> PostgreSQL
make check-sqlite-pg   # SQLite -> PostgreSQL
make check-integration # all three
```

## Run

```bash
# Direct source/target from CLI:
./build/bin/pgloader sqlite:///path/to/db.sqlite postgresql://localhost/targetdb
./build/bin/pgloader mysql://user@host/db postgresql://localhost/target
./build/bin/pgloader data.csv postgresql://localhost/db --table mytable

# Config file mode (.load file):
./build/bin/pgloader my_migration.load
```

## Project Architecture

**Concept:** Go port of pgloader (Common Lisp) — migrates databases to PostgreSQL.

### Entry Point
`cmd/pgloader/main.go` — cobra CLI. Detects source from URI scheme or `.load` file extension, dispatches to source-specific runner.

### Two Execution Paths
1. **CLI args** → source runner in main.go
2. **`.load` config file** → `configfile.ExecuteConfigFile()` → parses state-machine grammar (LOAD DATABASE/CSV, FROM, INTO, WITH, SET, CAST, BEFORE/AFTER LOAD DO)

### Four-Phase DB Migration Lifecycle (orchestrator)
```
FetchMetadata → PrepareTarget → copyAllTables → CompleteTarget
```
- **FetchMetadata:** Introspect source schema into `catalog.Catalog` (schemas → tables → columns, indexes, FKs, triggers)
- **PrepareTarget:** `CREATE TABLE`, extensions, types, schemas on target PG
- **copyAllTables:** Per-table pipeline: `MapRows → batch → COPY FROM STDIN` (with PK-range sharding for concurrent tables)
- **CompleteTarget:** `CREATE INDEX`, `ADD FOREIGN KEY`, `RESET SEQUENCE`, comments

### Key Interfaces (`internal/source/source.go`)
- **`Source`** — data reading: `TableName()`, `MapRows()`, `CopyColumnList()`, `ConcurrencySupport()`
- **`DbSource`** — extends Source with schema lifecycle: `FetchMetadata()`, `PrepareTarget()`, `CompleteTarget()`

### Source Implementations
| Package | Data Method | Concurrency | Schema Source |
|---------|-------------|-------------|---------------|
| `source/sqlite` | SELECT | No | sqlite_master + PRAGMA |
| `source/mysql` | SELECT (paginated) | PK-range sharding | information_schema |
| `source/pgsql` | COPY TO STDOUT | PK-range sharding | pg_catalog |
| `source/csv` | File reader | No | N/A |

### Package Map
- **`catalog/`** — Schema data model + DDL generation (`CreateTableSQL()`, `CreateIndexSQL()`, etc.)
- **`cast/`** — Type-matching rule engine with default rulesets for each source -> PG. Transform fns: `zero-dates-to-null`, `tinyint-to-bool`, `bit-to-bool`, `money-to-numeric`, etc.
- **`copy/`** — Batch accumulation, `FormatRowToCopyText()`, `COPY FROM STDIN` flush, binary-search retry on bad rows
- **`pipeline/`** — Per-table goroutine pipeline: MapRows callback → batch → flush
- **`monitor/`** — Event-driven stats collection + formatted summary
- **`config/`** — Config struct + WITH option parser
- **`configfile/`** — .load file state-machine parser + executor

### Config Precedence (lowest→highest)
1. `config.DefaultConfig()`
2. .load file WITH options
3. .load file SET options
4. CLI `--with` / `--set` flags
5. Explicit CLI flags (`--foreign-keys`, `--include-drop`, etc.)

### CAST Rules
Type mapping rules are defined per-source in `internal/cast/{mysql,pgsql,sqlite}_rules.go`. Adding a new source type requires: source implementation (DbSource), CAST ruleset, and CLI runner in main.go.

### Key Dependencies
- `pgx/v5` — PG driver + COPY protocol
- `go-sqlite3` — SQLite (requires CGO)
- `cobra` — CLI framework
- `go-sql-driver/mysql` — MySQL driver

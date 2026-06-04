# pgloader-go

Go port of [pgloader](https://pgloader.io) — a high-performance PostgreSQL data loading tool. Migrate data from MySQL, PostgreSQL, and CSV files into PostgreSQL with automatic schema migration, parallel COPY pipelines, and per-row error handling.

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                       CLI (cobra)                         │
│  cmd/pgloader/main.go                                    │
└─────┬──────────────────────┬──────────────────────┬───────┘
      │ CSV                  │ MySQL/PG              │
      ▼                      ▼                       ▼
┌──────────────┐   ┌──────────────────┐   ┌──────────────────┐
│  Pipeline     │   │  Orchestrator     │   │  Orchestrator     │
│  pipeline/    │   │  orchestrator/    │   │  orchestrator/    │
│              │   │                  │   │                  │
│  1. MapRows  │   │  1. FetchMetadata│   │  1. FetchMetadata│
│  2. Batch    │   │  2. PrepareTarget│   │  2. PrepareTarget│
│  3. COPY     │   │  3. copyAllTables│   │  3. copyAllTables│
│              │   │  4. CompleteTarg │   │  4. CompleteTarg │
└──────┬───────┘   └────────┬─────────┘   └────────┬─────────┘
       │                    │                      │
       └────────────────────┼──────────────────────┘
                            ▼
              ┌─────────────────────────┐
              │      copy package        │
              │  FormatRowToCopyText     │
              │  Batch / RetryBatch      │
              │  CopyWriter.FlushBatch   │
              │  binarySearchBadRow      │
              └────────────┬────────────┘
                           ▼
              ┌─────────────────────────┐
              │    PostgreSQL COPY      │
              │    FROM STDIN           │
              └─────────────────────────┘
```

### Core packages

| Package | Responsibility |
|---------|---------------|
| [`cmd/pgloader`](cmd/pgloader) | CLI entry point, flag parsing, source dispatch |
| [`internal/config`](internal/config) | Global configuration, WITH option parsing |
| [`internal/catalog`](internal/catalog) | Data model for schemas, tables, columns, indexes, FKs; DDL generation |
| [`internal/source`](internal/source) | `Source` and `DbSource` interfaces |
| [`internal/source/csv`](internal/source/csv) | CSV file reader with delimiter guessing |
| [`internal/source/mysql`](internal/source/mysql) | MySQL schema introspection + data reader |
| [`internal/source/pgsql`](internal/source/pgsql) | PostgreSQL schema introspection + COPY-based reader |
| [`internal/cast`](internal/cast) | CAST rule engine — MySQL/PG type mapping + transform functions |
| [`internal/copy`](internal/copy) | COPY text format encoding, batch management, binary-search retry |
| [`internal/pipeline`](internal/pipeline) | Goroutine pipeline: reader → batch → COPY writer |
| [`internal/orchestrator`](internal/orchestrator) | Full migration lifecycle orchestration |
| [`internal/monitor`](internal/monitor) | Event-driven statistics collection + summary report |

## Data flow

### File source (CSV)

```
CSV file → MapRows(row by row) → FormatRowToCopyText → batch
  → batch full → FlushBatch → COPY FROM STDIN
  → error → RetryBatch (binary search bad rows) → retry good rows
```

### Database migration (MySQL / PostgreSQL)

```
FetchMetadata (introspect source schema)
  → Apply CAST rules (type mapping)
  → PrepareTarget (CREATE TABLE, extensions, types)
  → For each table: MapRows → COPY pipeline
  → CompleteTarget (CREATE INDEX, ADD FOREIGN KEY, RESET SEQUENCE)
```

## Usage

```bash
# CSV import
pgloader data.csv postgresql://localhost/mydb --table mytable --header

# MySQL to PostgreSQL
pgloader mysql://user@host/dbname postgresql://localhost/target

# PostgreSQL to PostgreSQL
pgloader postgresql://source-host/dbname postgresql://target-host/targetdb

# Schema-only migration
pgloader mysql://user@host/dbname postgresql://localhost/target --with "schema only"

# Dry run (validate connections only)
pgloader mysql://user@host/dbname postgresql://localhost/target --dry-run
```

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--table` | — | Target table name (required for CSV) |
| `--type` | auto | Source type: `csv`, `mysql`, `postgresql`, `pg` |
| `--delimiter` | `,` | CSV delimiter character |
| `--header` | `false` | CSV file has header row |
| `--skip-lines` | `0` | Lines to skip at start of file |
| `--encoding` | — | Source file encoding |
| `--columns` | — | Column names |
| `--with` | — | WITH options (see below) |
| `--set` | — | GUC settings |
| `--before` | — | SQL file to run before load |
| `--after` | — | SQL file to run after load |
| `--cast` | — | Cast rules file |
| `--foreign-keys` | `true` | Create foreign keys after data load |
| `--include-drop` | `false` | DROP TABLE IF EXISTS before CREATE |
| `--debug` | `false` | Enable debug SQL logging |
| `--dry-run` | `false` | Validate connections and exit |
| `--quiet` | `false` | Suppress progress messages |
| `--logfile` | — | Write log to file |

### WITH options

| Option | Description |
|--------|-------------|
| `create tables` / `no create tables` | Enable/disable table creation |
| `create indexes` / `no create indexes` | Enable/disable index creation |
| `foreign keys` / `no foreign keys` | Enable/disable FK creation |
| `include drop` / `no include drop` | DROP before CREATE |
| `truncate` / `no truncate` | Truncate target before load |
| `schema only` / `data only` | Migration scope |
| `batch size = N` | Rows per batch (default 50000) |
| `batch concurrency = N` | Writer goroutines |
| `prefetch rows = N` | Rows to prefetch (default 10000) |
| `batch rows per range = N` | DB source shard size |

## Output

```
             table name     errors       rows      bytes            time
-----------------------  ---------  ---------  ---------  --------------
            before load          0          0                     0.000s
                  fetch          0          0                     0.083s
   create, drop, truncate          0          0                     0.054s
-----------------------  ---------  ---------  ---------  --------------
  "public"."accounts"              0        100     1.4 kB          0.003s
  "public"."measurements"          0          3       57 B          0.002s
-----------------------  ---------  ---------  ---------  --------------
    create indexes, fkeys          0          0                     0.030s
               after load          0          0                     0.000s
-----------------------  ---------  ---------  ---------  --------------
        Total import time          0        114     1.9 kB          0.204s
```

## CAST type mappings

### MySQL → PostgreSQL

| MySQL type | PostgreSQL type | Transform |
|------------|----------------|-----------|
| `tinyint(1)` | `boolean` | `tinyint-to-bool` |
| `int auto_increment` | `serial` | — |
| `bigint unsigned` | `numeric(20)` | — |
| `datetime` / `timestamp` | `timestamptz` | `zero-dates-to-null` |
| `json` | `jsonb` | — |
| `enum` / `set` | `text` | — |
| `geometry` / `point` | `geometry` / `point` | `wkt-to-geometry` |
| `bit(1)` | `boolean` | `bit-to-bool` |

### PostgreSQL → PostgreSQL

| PG type | Target type | Transform |
|---------|-------------|-----------|
| `money` | `numeric` | `money-to-numeric` |
| `xid` | `bigint` | — |
| `txid_snapshot` | `text` | — |
| `pg_lsn` | `text` | — |

## Build

```bash
git clone git@github.com:tking320/pgloader-go.git
cd pgloader-go
make build
./build/bin/pgloader --help
```

### Requirements

- Go 1.20+
- PostgreSQL target
- MySQL source (optional, for MySQL migrations)

## Roadmap

- [x] CSV source with delimiter guessing
- [x] MySQL source with schema migration
- [x] PostgreSQL source with schema migration
- [x] CAST rule engine
- [x] Batch error retry (binary search)
- [x] Summary report matching native pgloader format
- [ ] SQLite source
- [ ] MSSQL source
- [ ] Fixed-width / DBF / IXF sources
- [ ] `.load` command file parser
- [ ] Citus distribution support

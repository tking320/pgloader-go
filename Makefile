APP_NAME   = pgloader
BUILDDIR   = build
GO         = go

.PHONY: all build test test-short lint fmt clean check check-pg-pg check-mysql-pg check-sqlite-pg check-mssql-pg check-mssql-pg-cli check-mssql-pg-load check-integration _run-mssql-migration

all: build

build:
	$(GO) build -o $(BUILDDIR)/bin/$(APP_NAME) ./cmd/$(APP_NAME)

test:
	$(GO) test ./internal/... -v -count=1

test-short:
	$(GO) test ./internal/... -short -count=1

lint:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: lint build test check-pg-pg check-mysql-pg check-sqlite-pg check-mssql-pg
	$(GO) build -race ./...

# ---------------------------------------------------------------------------
# Integration tests (require database containers)
# ---------------------------------------------------------------------------

PG_SRC  ?= postgresql://test:test@localhost:5434/sourcedb
PG_TGT  ?= postgresql://test:test@localhost:5433/targetdb
MYSQL_URI ?= mysql://root:test@127.0.0.1:3306/sourcedb
MSSQL_SA_PASSWORD ?= YourStr0ngP@ssword
# URL-encoded password (%40 = @) for use with Go's url.Parse in pgloader
MSSQL_URI ?= sqlserver://sa:YourStr0ngP%40ssword@localhost:1433?database=sourcedb

check-integration: check-pg-pg check-mysql-pg check-sqlite-pg check-mssql-pg check-mssql-pg-load

check-pg-pg: build
	@echo "=== PG -> PG integration test ==="
	@echo "  Cleaning target database..."
	@psql "$(PG_TGT)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" -q >/dev/null 2>&1 || true
	@ok=1; \
	command -v psql >/dev/null 2>&1 || { echo "  SKIP: psql not installed"; ok=0; }; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Loading test data into source..."; \
	  psql "$(PG_SRC)" -f test/pgsql_migration_test_data.sql -q >/dev/null 2>&1 || { echo "  SKIP: source PG unreachable"; ok=0; }; \
	fi; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Running migration..."; \
	  ./build/bin/pgloader "$(PG_SRC)" "$(PG_TGT)" --with "foreign keys"; \
	  echo "  Verifying migration..."; \
	  psql "$(PG_TGT)" -f test/pgsql_migration_verify.sql -t -A; \
	else \
	  echo "  SKIPPED"; \
	fi

check-mysql-pg: build
	@echo "=== MySQL -> PG integration test ==="
	@echo "  Cleaning target database..."
	@psql "$(PG_TGT)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" -q >/dev/null 2>&1 || true
	@ok=1; \
	command -v mysql >/dev/null 2>&1 || { echo "  SKIP: mysql client not installed"; ok=0; }; \
	command -v psql >/dev/null 2>&1 || { echo "  SKIP: psql not installed"; ok=0; }; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Loading test data into source..."; \
	  mysql -h 127.0.0.1 -u root -ptest sourcedb < test/mysql_migration_test_data.sql 2>/dev/null || { echo "  SKIP: MySQL unreachable"; ok=0; }; \
	fi; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Running migration..."; \
	  ./build/bin/pgloader "$(MYSQL_URI)" "$(PG_TGT)" --with "foreign keys"; \
	  echo "  Verifying migration..."; \
	  psql "$(PG_TGT)" -f test/mysql_migration_verify.sql -t -A; \
	else \
	  echo "  SKIPPED"; \
	fi

# ---------------------------------------------------------------------------
# SQLite -> PG integration test
# ---------------------------------------------------------------------------

SQLITE_TEST_DB  ?= /tmp/pgloader_sqlite_test.db

check-sqlite-pg: build
	@echo "=== SQLite -> PG integration test ==="
	@echo "  Cleaning target database..."
	@psql "$(PG_TGT)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" -q >/dev/null 2>&1 || true
	@ok=1; \
	command -v sqlite3 >/dev/null 2>&1 || { echo "  SKIP: sqlite3 not installed"; ok=0; }; \
	command -v psql >/dev/null 2>&1 || { echo "  SKIP: psql not installed"; ok=0; }; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Creating test SQLite database..."; \
	  rm -f "$(SQLITE_TEST_DB)"; \
	  sqlite3 "$(SQLITE_TEST_DB)" < test/sqlite_migration_test_data.sql 2>/dev/null; \
	  echo "  Running migration via .load config..."; \
	  ./build/bin/pgloader test/sqlite.load; \
	  echo "  Verifying migration..."; \
	  psql "$(PG_TGT)" -f test/sqlite_migration_verify.sql -t -A; \
	  echo "  Cleaning up..."; \
	  rm -f "$(SQLITE_TEST_DB)"; \
	else \
	  echo "  SKIPPED"; \
	fi

# ---------------------------------------------------------------------------
# MSSQL -> PG integration test
# ---------------------------------------------------------------------------

check-mssql-pg check-mssql-pg-cli: build
	@echo "=== MSSQL -> PG integration test (CLI) ==="
	@psql "$(PG_TGT)" -c "DROP SCHEMA IF EXISTS dbo CASCADE; CREATE SCHEMA dbo;" -q >/dev/null 2>&1 || true
	$(MAKE) _run-mssql-migration MIGRATION_CMD="./build/bin/pgloader \"$(MSSQL_URI)\" \"$(PG_TGT)\" --with \"foreign keys\""

check-mssql-pg-load: build
	@echo "=== MSSQL -> PG integration test (.load file) ==="
	@psql "$(PG_TGT)" -c "DROP SCHEMA IF EXISTS dbo CASCADE; CREATE SCHEMA dbo;" -q >/dev/null 2>&1 || true
	$(MAKE) _run-mssql-migration MIGRATION_CMD="./build/bin/pgloader test/mssql.load"

_run-mssql-migration:
	@ok=1; input_path="test/mssql_migration_test_data.sql"; \
	if command -v sqlcmd >/dev/null 2>&1; then \
	  sqlcmd() { sqlcmd "$$@"; }; \
	elif docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^pgloader-mssql-src$$'; then \
	  echo "  Using docker exec for sqlcmd..."; \
	  docker cp "$$input_path" pgloader-mssql-src:/tmp/mssql_test_data.sql 2>/dev/null; \
	  sqlcmd() { docker exec pgloader-mssql-src /opt/mssql-tools/bin/sqlcmd -C "$$@"; }; \
	  input_path="/tmp/mssql_test_data.sql"; \
	else \
	  echo "  SKIP: sqlcmd not found (install: brew install mssql-tools, or run MSSQL Docker container)"; ok=0; \
	fi; \
	command -v psql >/dev/null 2>&1 || { echo "  SKIP: psql not installed"; ok=0; }; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Creating sourcedb database..."; \
	  sqlcmd -S localhost,1433 -U sa -P "$(MSSQL_SA_PASSWORD)" -Q "DROP DATABASE IF EXISTS sourcedb; CREATE DATABASE sourcedb" 2>/dev/null || { echo "  SKIP: cannot create sourcedb"; ok=0; }; \
	fi; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Loading test data into source..."; \
	  sqlcmd -S localhost,1433 -U sa -P "$(MSSQL_SA_PASSWORD)" -d sourcedb -i "$$input_path" 2>/dev/null || { echo "  SKIP: cannot load test data"; ok=0; }; \
	fi; \
	if [ "$$ok" -eq 1 ]; then \
	  echo "  Running migration..."; \
	  $(MIGRATION_CMD); \
	  echo "  Verifying migration..."; \
	  psql "$(PG_TGT)" -f test/mssql_migration_verify.sql -t -A; \
	else \
	  echo "  SKIPPED"; \
	fi

clean:
	rm -rf $(BUILDDIR)
	rm -f $(APP_NAME)

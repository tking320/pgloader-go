APP_NAME   = pgloader
BUILDDIR   = build
GO         = go

.PHONY: all build test test-short lint fmt clean check check-pg-pg check-mysql-pg check-integration

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

check: lint build test check-pg-pg check-mysql-pg check-sqlite-pg
	$(GO) build -race ./...

# ---------------------------------------------------------------------------
# Integration tests (require database containers)
# ---------------------------------------------------------------------------

PG_SRC  ?= postgresql://test:test@localhost:5434/sourcedb
PG_TGT  ?= postgresql://test:test@localhost:5433/targetdb
MYSQL_URI ?= mysql://root:test@127.0.0.1:3306/sourcedb

check-integration: check-pg-pg check-mysql-pg check-sqlite-pg

check-pg-pg: build
	@echo "=== PG -> PG integration test ==="
	@echo "  Cleaning target database..."
	@psql "$(PG_TGT)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" -q >/dev/null 2>&1 || true
	@command -v psql >/dev/null 2>&1 || { echo "SKIP: psql not installed"; exit 0; }
	@echo "  Loading test data into source..."
	@psql "$(PG_SRC)" -f test/pgsql_migration_test_data.sql -q >/dev/null 2>&1 || { echo "SKIP: source PG unreachable at $(PG_SRC)"; exit 0; }
	@echo "  Running migration..."
	./build/bin/pgloader "$(PG_SRC)" "$(PG_TGT)" --with "foreign keys"
	@echo "  Verifying migration..."
	psql "$(PG_TGT)" -f test/pgsql_migration_verify.sql -t -A

check-mysql-pg: build
	@echo "=== MySQL -> PG integration test ==="
	@echo "  Cleaning target database..."
	@psql "$(PG_TGT)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" -q >/dev/null 2>&1 || true
	@command -v mysql >/dev/null 2>&1 || { echo "SKIP: mysql client not installed"; exit 0; }
	@command -v psql >/dev/null 2>&1 || { echo "SKIP: psql not installed"; exit 0; }
	@echo "  Loading test data into source..."
	@mysql -h 127.0.0.1 -u root -ptest sourcedb < test/mysql_migration_test_data.sql 2>/dev/null || { echo "SKIP: MySQL unreachable at 127.0.0.1:3306"; exit 0; }
	@echo "  Running migration..."
	./build/bin/pgloader "$(MYSQL_URI)" "$(PG_TGT)" --with "foreign keys"
	@echo "  Verifying migration..."
	psql "$(PG_TGT)" -f test/mysql_migration_verify.sql -t -A

# ---------------------------------------------------------------------------
# SQLite -> PG integration test
# ---------------------------------------------------------------------------

SQLITE_TEST_DB  ?= /tmp/pgloader_sqlite_test.db

check-sqlite-pg: build
	@echo "=== SQLite -> PG integration test ==="
	@echo "  Cleaning target database..."
	@psql "$(PG_TGT)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" -q >/dev/null 2>&1 || true
	@command -v sqlite3 >/dev/null 2>&1 || { echo "SKIP: sqlite3 not installed"; exit 0; }
	@command -v psql >/dev/null 2>&1 || { echo "SKIP: psql not installed"; exit 0; }
	@echo "  Creating test SQLite database..."
	@rm -f "$(SQLITE_TEST_DB)"
	@sqlite3 "$(SQLITE_TEST_DB)" < test/sqlite_migration_test_data.sql 2>/dev/null
	@echo "  Running migration via .load config..."
	./build/bin/pgloader test/sqlite.load
	@echo "  Verifying migration..."
	psql "$(PG_TGT)" -f test/sqlite_migration_verify.sql -t -A
	@echo "  Cleaning up..."
	@rm -f "$(SQLITE_TEST_DB)"

clean:
	rm -rf $(BUILDDIR)
	rm -f $(APP_NAME)

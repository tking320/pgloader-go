# SQLite Source — pgloader-go 数据源扩展设计

**日期**: 2026-06-05
**状态**: 设计稿
**项目**: pgloader-go

## 1. 概述

为 pgloader-go 增加 SQLite 作为数据源，支持将 SQLite 数据库迁移到 PostgreSQL。
功能范围对标现有 MySQL 源和原生 pgloader 的 SQLite 实现。

## 2. 目标

- 支持 `.load` 配置文件和 CLI 命令行两种方式执行 SQLite→PG 迁移
- 完整元数据自省（表、列、索引、外键、自增序列检测）
- SQLite→PostgreSQL 类型映射（CAST 规则）
- 数据读取和批量 COPY 写入
- 单元测试和集成测试

## 3. 非目标

- 不实现 SQLite 触发器迁移
- 不实现 SQLite 视图迁移
- 不实现 SQLite 自定义函数迁移
- 不实现 SQLite 分区表（SQLite 无原生分区支持）

## 4. 架构

### 4.1 模块结构

新增文件全部位于 pgloader-go 代码库中：

```
internal/source/sqlite/
├── source.go             -- SQLiteSource 结构体、Connect/Close、Source 接口实现
├── source_test.go        -- 单元测试
├── metadata.go           -- FetchMetadata: 表/列/索引/FK 自省
└── metadata_test.go      -- 元数据测试

internal/cast/sqlite_rules.go  -- SQLiteDefaultRules() 类型映射
```

### 4.2 受影响文件

| 文件 | 改动 |
|------|------|
| `internal/configfile/types.go` | 增加 `SourceSQLite SourceType = "sqlite"` |
| `internal/configfile/parser.go` | `parseCommand()` 增加 `sqlite://` URI 识别 |
| `internal/configfile/executor.go` | 增加 `case SourceSQLite` 和 `execSQLite()` 函数 |
| `cmd/pgloader/main.go` | `--type` 增加 `sqlite`，source switch 增加 case，增加 `runSQLite()` |
| `go.mod` | 添加 `github.com/mattn/go-sqlite3` 依赖 |
| `Makefile` | 增加 `check-sqlite-pg` 集成测试目标 |

## 5. 详细设计

### 5.1 SQLiteSource 结构体

参照 `MySQLSource` 模式：

```go
type SQLiteSource struct {
    // 连接
    db     *sql.DB
    pool   *pgxpool.Pool

    // URI 参数
    filename string   // SQLite 文件路径，":memory:" 表示内存数据库
    dbname   string   // 目标 PG 数据库名

    // 元数据
    catalog  *catalog.Catalog
    schema   *catalog.Schema
    cast     *cast.Engine

    // 过滤
    includingOnly []string
    excluding     []string
    whereClause   map[string]string

    // 运行时
    activeTable string
    tableNames  []string
}
```

### 5.2 URI 解析

URI 格式：`sqlite:///path/to/database.sqlite`

- `sqlite:///path/to/file.db` → 文件路径
- `sqlite:///:memory:` → 内存数据库（特殊处理）
- 通过 `strings.TrimPrefix(uri, "sqlite://")` 提取路径

### 5.3 元数据自省

使用 PRAGMA 查询（参考原生 pgloader 实践）：

**表列表**:
```sql
SELECT name FROM sqlite_master
WHERE type='table' AND name != 'sqlite_sequence'
ORDER BY name
```

**列信息** (`PRAGMA table_info`):
```sql
PRAGMA table_info('tablename')
-- 返回: cid, name, type, notnull, dflt_value, pk
```

**索引列表** (`PRAGMA index_list`):
```sql
PRAGMA index_list('tablename')
-- 返回: seq, name, unique, origin, partial
```

**索引列** (`PRAGMA index_info`):
```sql
PRAGMA index_info('indexname')
-- 返回: seqno, cid, name
```

**外键** (`PRAGMA foreign_key_list`):
```sql
PRAGMA foreign_key_list('tablename')
-- 返回: id, seq, table, from, to, on_update, on_delete, match
```

**自增序列**:
```sql
SELECT seq FROM sqlite_sequence WHERE name = 'tablename'
```

**AUTOINCREMENT 检测**:
```sql
SELECT sql FROM sqlite_master WHERE name = 'tablename'
```
解析 CREATE TABLE SQL 中是否包含 `AUTOINCREMENT` 关键字。

### 5.4 类型映射

定义 `SQLiteDefaultRules()` 返回默认 CAST 规则：

| SQLite 类型 | PostgreSQL 类型 |
|-------------|----------------|
| `INTEGER PRIMARY KEY` + 自增 | `bigserial` |
| `INTEGER` | `bigint` |
| `TINYINT` | `smallint` |
| `BIGINT` | `bigint` |
| `REAL` | `real` |
| `FLOAT` | `float` |
| `DOUBLE` / `DOUBLE PRECISION` | `double precision` |
| `NUMERIC`(保留 typmod) | `numeric` |
| `DECIMAL`(保留 typmod) | `decimal` |
| `TEXT`, `VARCHAR`, `NVARCHAR`, `CHAR`, `CLOB` | `text` |
| `BLOB` | `bytea` |
| `BOOLEAN` | `boolean` |
| `DATETIME` | `timestamptz` |
| `TIMESTAMP` | `timestamp` |
| `DATE` | `date` |
| `TIME` | `time` |

### 5.5 数据读取 (MapRows)

```go
func (s *SQLiteSource) MapRows(ctx context.Context, tableName string,
    processRow processRowFn, batch *copy.Batch) error
```

流程：
1. 构建 `SELECT "col1", "col2", ... FROM "tableName"` 查询
   - 如存在 `whereClause` 则追加
2. `s.db.QueryContext(ctx, query)` 执行查询
3. 遍历 `*sql.Rows`
   - 读取各列值（`interface{}` 扫描）
   - 根据目标 PG 类型进行转换：
     - `int64` → `int64-to-string`
     - `float64` → `float-to-string`
     - `[]byte` (BLOB) → `bytea` 编码
     - `string` → 按列目标类型转换
     - `nil` → `\N` (NULL)
   - 调用 `processRow` 将格式化后的行写入 COPY batch
4. 返回 `batch.Done()` 结果

### 5.6 并发控制

SQLite 不支持并发读取，因此：

```go
func (s *SQLiteSource) ConcurrencySupport() bool { return false }
```

与原生 pgloader 行为一致。

### 5.7 目标端准备 (PrepareTarget)

复用现有 `catalog.Table.CreateTableSQL()` 生成 CREATE TABLE 语句：
- 转换 schema 为 PG schema
- 自增 PK→bigserial
- 创建表后，再创建索引和外键

### 5.8 目标端完成 (CompleteTarget)

参照 MySQLCompleteTarget：
- 创建索引（`CREATE INDEX ... ON ...`）
- 添加外键约束（`ALTER TABLE ... ADD FOREIGN KEY ...`）
- 重置序列（`SELECT setval(...)`）

## 6. 集成点

### 6.1 .load 配置文件解析

在 `internal/configfile/parser.go` 中增加：

```go
// parseCommand() 中增加 URI 识别
if strings.HasPrefix(uri, "sqlite://") {
    return SourceSQLite, nil
}
```

### 6.2 执行器

在 `internal/configfile/executor.go` 中增加：

```go
case SourceSQLite:
    return execSQLite(ctx, cmd, pool)
```

`execSQLite()` 函数：
1. 解析 URI 提取文件路径
2. 创建 SQLiteSource 实例
3. 连接 SQLite
4. FetchMetadata
5. PrepareTarget
6. 逐表 copyAllTables
7. CompleteTarget

### 6.3 CLI 入口

在 `cmd/pgloader/main.go` 中增加：
- `--type` flag 增加 `"sqlite"` 选项
- source switch 增加 `case "sqlite"`
- `runSQLite()` 函数（与 `runMySQL()` 模式一致）

## 7. 测试策略

### 7.1 单元测试

- `internal/source/sqlite/metadata_test.go`: 使用预定义 PRAGMA 结果测试元数据解析
- `internal/cast/sqlite_rules_test.go`: 测试类型映射规则
- `internal/configfile/parser_test.go`: 增加 SQLite URI 和 .load 文件解析测试

### 7.2 集成测试 — .load 文件方式

与现有 MySQL/PG 集成测试不同，SQLite 测试使用 `.load` 配置文件方式执行迁移。

**测试数据** (`test/sqlite_migration_test_data.sql`):
```sql
-- SQLite 测试数据（通过 sqlite3 CLI 注入）
CREATE TABLE basic_types (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    age INTEGER,
    salary REAL,
    bio BLOB,
    is_active INTEGER DEFAULT 1,
    birthday TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO basic_types (name, age, salary, bio, is_active, birthday)
VALUES ('Alice', 30, 75000.50, x'48656c6c6f', 1, '1994-03-15'),
       ('Bob', 25, 65000.00, NULL, 0, '1999-07-22');

CREATE TABLE orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    customer_name TEXT NOT NULL,
    amount REAL NOT NULL,
    order_date TEXT DEFAULT (datetime('now'))
);

INSERT INTO orders (customer_name, amount) VALUES ('Alice', 99.99), ('Bob', 150.00);

-- 测试特殊表名
CREATE TABLE "Test Tbl" (id INTEGER PRIMARY KEY, val TEXT);
INSERT INTO "Test Tbl" (val) VALUES ('space test');

CREATE TABLE "Test""Tbl" (id INTEGER PRIMARY KEY, val TEXT);
INSERT INTO "Test""Tbl" (val) VALUES ('quote test');
```

**配置文件** (`test/sqlite.load`):
```text
LOAD DATABASE
     FROM sqlite:///tmp/pgloader_sqlite_test.db
     INTO postgresql://test:test@localhost:5433/targetdb

     WITH include drop, create tables, create indexes, reset sequences,
          foreign keys, batch size = 1000, batch concurrency = 1

     SET client_encoding to 'UTF8'

     CAST type datetime to timestamptz drop default drop not null using zero-dates-to-null,
          type tinyint to smallint

     BEFORE LOAD DO
     $$ create schema if not exists public; $$

     AFTER LOAD DO
     $$ analyze basic_types; $$;
```

**验证脚本** (`test/sqlite_migration_verify.sql`):
```sql
-- 验证基础类型表
SELECT 'PASS' AS basic_types_row_count
FROM basic_types WHERE id = 1 AND name = 'Alice' AND age = 30;

-- 验证自增序列
SELECT 'PASS' AS auto_increment
FROM basic_types WHERE id = 2;

-- 验证特殊表名
SELECT 'PASS' AS space_table
FROM "Test Tbl" WHERE val = 'space test';

SELECT 'PASS' AS quote_table
FROM "Test""Tbl" WHERE val = 'quote test';
```

### 7.3 Makefile 目标

```makefile
# SQLite 测试相关的变量
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

check-integration: check-pg-pg check-mysql-pg check-sqlite-pg
```

## 8. 限制与注意事项

1. SQLite 的 `:memory:` 数据库在多进程/多连接场景下需谨慎处理
2. SQLite 无原生 `BOOLEAN` 类型（存储为 `INTEGER 0/1`），CAST 规则需特殊处理
3. SQLite 的 `ROWID` 隐藏列不迁移（仅迁移显式定义的表列）
4. 外键约束顺序可能影响 CompleteTarget 阶段的创建顺序
5. 大文件 SQLite 数据库迁移性能受限于单线程读取

## 9. 实现顺序

1. 添加 `mattn/go-sqlite3` 依赖
2. 创建 `internal/source/sqlite/` 包（source.go, metadata.go）
3. 实现 CAST 规则 `internal/cast/sqlite_rules.go`
4. 集成到配置解析和 CLI
5. 添加测试
6. 更新文档

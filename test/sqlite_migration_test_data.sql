-- SQLite test data for pgloader integration tests
-- Execute: sqlite3 /tmp/pgloader_sqlite_test.db < test/sqlite_migration_test_data.sql

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
VALUES ('Alice', 30, 75000.50, x'48656c6c6f', 1, '1994-03-15');

INSERT INTO basic_types (name, age, salary, bio, is_active, birthday)
VALUES ('Bob', 25, 65000.00, NULL, 0, '1999-07-22');

CREATE TABLE orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    customer_name TEXT NOT NULL,
    amount REAL NOT NULL,
    order_date TEXT DEFAULT (datetime('now'))
);

INSERT INTO orders (customer_name, amount) VALUES ('Alice', 99.99);
INSERT INTO orders (customer_name, amount) VALUES ('Bob', 150.00);

-- Test table with space in name
CREATE TABLE "Test Tbl" (
    id INTEGER PRIMARY KEY,
    val TEXT
);
INSERT INTO "Test Tbl" (val) VALUES ('space test');

-- Test table with quote in name
CREATE TABLE "Test""Tbl" (
    id INTEGER PRIMARY KEY,
    val TEXT
);
INSERT INTO "Test""Tbl" (val) VALUES ('quote test');

-- Table with indexes and FK
CREATE TABLE parent (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL
);
INSERT INTO parent VALUES (1, 'parent1');
INSERT INTO parent VALUES (2, 'parent2');

CREATE TABLE child (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id INTEGER NOT NULL,
    label TEXT,
    FOREIGN KEY (parent_id) REFERENCES parent(id) ON DELETE CASCADE
);
CREATE INDEX idx_child_parent ON child(parent_id);
INSERT INTO child (parent_id, label) VALUES (1, 'child1');
INSERT INTO child (parent_id, label) VALUES (1, 'child2');

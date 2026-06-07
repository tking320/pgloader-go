-- MSSQL test data for pgloader integration tests
-- Execute: sqlcmd -S localhost,1433 -U sa -P password -d sourcedb -i test/mssql_migration_test_data.sql

CREATE TABLE basic_types (
    id INT IDENTITY(1,1) PRIMARY KEY,
    name NVARCHAR(255) NOT NULL,
    age INT,
    salary FLOAT,
    bio VARBINARY(100),
    is_active BIT DEFAULT 1,
    birthday DATE,
    created_at DATETIME2 DEFAULT GETDATE()
);

INSERT INTO basic_types (name, age, salary, bio, is_active, birthday)
VALUES (N'Alice', 30, 75000.50, 0x48656C6C6F, 1, '1994-03-15');

INSERT INTO basic_types (name, age, salary, bio, is_active, birthday)
VALUES (N'Bob', 25, 65000.00, NULL, 0, '1999-07-22');

CREATE TABLE orders (
    id INT IDENTITY(1,1) PRIMARY KEY,
    customer_name NVARCHAR(255) NOT NULL,
    amount NUMERIC(10,2) NOT NULL,
    order_date DATETIME2 DEFAULT GETDATE()
);

INSERT INTO orders (customer_name, amount) VALUES (N'Alice', 99.99);
INSERT INTO orders (customer_name, amount) VALUES (N'Bob', 150.00);

-- Test table with space in name
CREATE TABLE [Test Tbl] (
    id INT IDENTITY(1,1) PRIMARY KEY,
    val NVARCHAR(100)
);
INSERT INTO [Test Tbl] (val) VALUES (N'space test');

-- Test table with uniqueidentifier
CREATE TABLE uuid_test (
    id INT IDENTITY(1,1) PRIMARY KEY,
    guid UNIQUEIDENTIFIER DEFAULT NEWID(),
    label NVARCHAR(100)
);
INSERT INTO uuid_test (label) VALUES (N'entry1');
INSERT INTO uuid_test (label) VALUES (N'entry2');

-- Table with indexes and FK
CREATE TABLE parent (
    id INT PRIMARY KEY,
    name NVARCHAR(255) NOT NULL
);
INSERT INTO parent VALUES (1, N'parent1');
INSERT INTO parent VALUES (2, N'parent2');

CREATE TABLE child (
    id INT IDENTITY(1,1) PRIMARY KEY,
    parent_id INT NOT NULL,
    label NVARCHAR(100),
    CONSTRAINT FK_child_parent FOREIGN KEY (parent_id) REFERENCES parent(id) ON DELETE CASCADE
);
CREATE INDEX idx_child_parent ON child(parent_id);
INSERT INTO child (parent_id, label) VALUES (1, N'child1');
INSERT INTO child (parent_id, label) VALUES (1, N'child2');

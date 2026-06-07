-- Verify basic_types table
SELECT 'PASS' AS basic_types_row_count
FROM basic_types WHERE id = 1 AND name = 'Alice' AND age = 30 AND salary = 75000.5;

SELECT 'PASS' AS basic_types_row_count2
FROM basic_types WHERE id = 2 AND name = 'Bob' AND age = 25;

-- Verify auto-increment (id=2 with AUTOINCREMENT)
SELECT 'PASS' AS basic_types_auto_inc
FROM basic_types WHERE id = 2;

-- Verify orders table
SELECT 'PASS' AS orders_row_count
FROM orders WHERE customer_name = 'Alice' AND amount = 99.99;

-- Verify users table and index
SELECT 'PASS' AS users_row_count
FROM users WHERE email = 'alice@example.com' AND name = 'Alice';

SELECT 'PASS' AS users_index_exists
FROM pg_indexes WHERE tablename = 'users' AND indexname = 'idx_users_email';

-- Verify special table names
SELECT 'PASS' AS space_table_name
FROM "Test Tbl" WHERE val = 'space test';

SELECT 'PASS' AS quote_table_name
FROM "Test""Tbl" WHERE val = 'quote test';

-- Verify foreign keys
SELECT 'PASS' AS fk_child_count
FROM child c JOIN parent p ON c.parent_id = p.id
WHERE p.name = 'parent1' AND c.label = 'child1';

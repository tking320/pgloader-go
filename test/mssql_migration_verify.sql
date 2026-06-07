SET search_path TO dbo;

-- Verify basic_types table
SELECT 'PASS' AS basic_types_row_count
FROM dbo.basic_types WHERE name = 'Alice' AND age = 30;

SELECT 'PASS' AS basic_types_salary
FROM dbo.basic_types WHERE name = 'Alice' AND salary = 75000.5;

SELECT 'PASS' AS basic_types_row_count2
FROM dbo.basic_types WHERE name = 'Bob' AND age = 25;

-- Verify auto-increment
SELECT 'PASS' AS basic_types_auto_inc
FROM dbo.basic_types WHERE id = 2;

-- Verify bit conversion
SELECT 'PASS' AS basic_types_bit
FROM dbo.basic_types WHERE is_active = true AND name = 'Alice';

SELECT 'PASS' AS basic_types_bit_false
FROM dbo.basic_types WHERE is_active = false AND name = 'Bob';

-- Verify orders table
SELECT 'PASS' AS orders_row_count
FROM dbo.orders WHERE customer_name = 'Alice' AND amount = 99.99;

-- Verify special table names
SELECT 'PASS' AS space_table_name
FROM "Test Tbl" WHERE val = 'space test';

-- Verify UUID conversion
SELECT 'PASS' AS uuid_table_count
FROM dbo.uuid_test WHERE label = 'entry1';

-- Verify foreign keys
SELECT 'PASS' AS fk_child_count
FROM dbo.child c JOIN dbo.parent p ON c.parent_id = p.id
WHERE p.name = 'parent1' AND c.label = 'child1';

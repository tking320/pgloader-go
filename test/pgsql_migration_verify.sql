-- Verify PG-to-PG migration results
-- Returns row counts for each table; all should match source

SELECT 'basic_types' AS table_name, COUNT(*) AS row_count FROM basic_types
UNION ALL
SELECT 'json_data', COUNT(*) FROM json_data
UNION ALL
SELECT 'enum_test', COUNT(*) FROM enum_test
UNION ALL
SELECT 'orders', COUNT(*) FROM orders
UNION ALL
SELECT 'logs', COUNT(*) FROM logs
UNION ALL
SELECT 'payments', COUNT(*) FROM payments
ORDER BY table_name;

-- Verify enum type exists
SELECT 'enum_exists' AS check_name, EXISTS (
    SELECT 1 FROM pg_type WHERE typname = 'mood'
    UNION ALL
    SELECT 1 FROM pg_type WHERE typname = 'mood'
) AS passed;

-- Verify indexes exist
SELECT 'index_check' AS check_name, COUNT(*) >= 3 AS passed
FROM pg_indexes
WHERE tablename = 'logs' AND indexname IN ('idx_logs_severity', 'idx_logs_logged_at', 'logs_pkey');

-- Verify foreign key exists
SELECT 'fk_check' AS check_name, COUNT(*) > 0 AS passed
FROM information_schema.table_constraints
WHERE table_name = 'orders' AND constraint_type = 'FOREIGN KEY';

-- Verify data integrity: orders reference valid products
SELECT 'fk_integrity' AS check_name, COUNT(*) = 0 AS passed
FROM orders o
LEFT JOIN basic_types p ON o.product_id = p.id
WHERE p.id IS NULL;

-- Verify money type was migrated
SELECT 'money_check' AS check_name, COUNT(*) = 3 AS passed FROM payments;

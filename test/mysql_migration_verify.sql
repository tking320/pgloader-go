-- Verify MySQL to PostgreSQL migration results

-- Check row counts
SELECT 'basic_types' AS table_name, COUNT(*)::text AS row_count FROM basic_types
UNION ALL
SELECT 'json_data', COUNT(*)::text FROM json_data
UNION ALL
SELECT 'enum_test', COUNT(*)::text FROM enum_test
UNION ALL
SELECT 'big_serial_test', COUNT(*)::text FROM big_serial_test
UNION ALL
SELECT 'date_time_test', COUNT(*)::text FROM date_time_test
UNION ALL
SELECT 'unsigned_test', COUNT(*)::text FROM unsigned_test
UNION ALL
SELECT 'parent', COUNT(*)::text FROM parent
UNION ALL
SELECT 'child', COUNT(*)::text FROM child
UNION ALL
SELECT 'bit_test', COUNT(*)::text FROM bit_test
UNION ALL
SELECT 'Test"Tbl', COUNT(*)::text FROM "Test""Tbl"
UNION ALL
SELECT 'test Tbl', COUNT(*)::text FROM "test Tbl"
ORDER BY table_name;

-- Check CAST results: tinyint(1) → boolean
SELECT 'tinyint_to_bool' AS check_name,
    CASE WHEN is_active = true AND name = 'Widget A' THEN 'PASS' ELSE 'FAIL' END AS result
FROM basic_types WHERE name = 'Widget A';

-- Check CAST results: date preserved
SELECT 'date_preserved' AS check_name,
    CASE WHEN date_col = '2024-01-15'::date THEN 'PASS' ELSE 'FAIL' END AS result
FROM date_time_test WHERE id = 1;

-- Check enum → text type
SELECT 'enum_as_text' AS check_name,
    CASE WHEN current_mood::text = 'happy' THEN 'PASS' ELSE 'FAIL' END AS result
FROM enum_test WHERE id = 1;

-- Check unsigned bigint → numeric(20)
SELECT 'unsigned_bigint' AS check_name,
    CASE WHEN big_unsigned = 4294967295::numeric(20) THEN 'PASS' ELSE 'FAIL' END AS result
FROM unsigned_test WHERE id = 2;

-- Check auto_increment → serial (id should be auto-generated)
SELECT 'auto_inc_serial' AS check_name,
    CASE WHEN count = 3 THEN 'PASS' ELSE 'FAIL' END AS result
FROM (SELECT COUNT(*) FROM big_serial_test) AS sub(count);

-- Check foreign key exists
SELECT 'fk_exists' AS check_name,
    CASE WHEN COUNT(*) > 0 THEN 'PASS' ELSE 'FAIL' END AS result
FROM information_schema.table_constraints
WHERE table_name = 'child' AND constraint_type = 'FOREIGN KEY';

-- MySQL test data for MySQL-to-PostgreSQL migration CI test
-- Exercises CAST rules and type mappings

CREATE TABLE basic_types (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description TEXT,
    is_active TINYINT(1) DEFAULT 1,
    price DECIMAL(10,2),
    rating FLOAT DEFAULT 0.0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO basic_types (name, description, is_active, price, rating) VALUES
    ('Widget A', 'A basic widget', 1, 19.99, 4.5),
    ('Widget B', 'Another widget', 0, 29.99, 3.8),
    ('Widget C', 'Premium widget', 1, 49.99, 4.9);

-- JSON data
CREATE TABLE json_data (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    data JSON NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO json_data (data) VALUES
    ('{"key": "value", "count": 42}'),
    ('{"items": [1,2,3], "nested": {"a": 1}}');

-- Enum types mapped to text
CREATE TABLE enum_test (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    current_mood ENUM('happy', 'sad', 'neutral') DEFAULT 'happy',
    score TINYINT CHECK (score >= 0 AND score <= 100)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO enum_test (current_mood, score) VALUES
    ('happy', 100),
    ('sad', 42),
    ('neutral', 75);

-- Auto increment bigint
CREATE TABLE big_serial_test (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    data VARCHAR(50)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO big_serial_test (data) VALUES
    ('row1'),
    ('row2'),
    ('row3');

-- Date/time types (zero dates avoided due to parseTime=true in DSN)
CREATE TABLE date_time_test (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    date_col DATE,
    datetime_col DATETIME,
    timestamp_col TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    year_col YEAR
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO date_time_test (date_col, datetime_col, year_col) VALUES
    ('2024-01-15', '2024-01-15 10:30:00', 2024),
    ('2024-06-01', '2024-06-01 00:00:00', 2025),
    ('2025-12-31', '2025-12-31 23:59:59', 2026);

-- Unsigned bigint → numeric(20)
CREATE TABLE unsigned_test (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    big_unsigned BIGINT UNSIGNED
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO unsigned_test (big_unsigned) VALUES
    (1),
    (4294967295),
    (42);

-- Foreign keys
CREATE TABLE parent (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(50) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE child (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    parent_id INTEGER NOT NULL,
    description VARCHAR(100),
    FOREIGN KEY (parent_id) REFERENCES parent(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO parent (name) VALUES ('Group A'), ('Group B'), ('Group C');
INSERT INTO child (parent_id, description) VALUES (1, 'Child of A'), (1, 'Another child of A'), (2, 'Child of B');

-- Bit type
CREATE TABLE bit_test (
    id INTEGER AUTO_INCREMENT PRIMARY KEY,
    flag BIT(1) DEFAULT b'0',
    flags BIT(8) DEFAULT b'10101010'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO bit_test (flag, flags) VALUES
    (b'1', b'11111111'),
    (b'0', b'00000000'),
    (b'1', b'10101010');

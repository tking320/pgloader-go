package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/tking320/pgloader-go/internal/cast"
)

func TestMetadataBasicTypes(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer s.Close()

	_, err := s.db.ExecContext(context.Background(), `
		CREATE TABLE basic_types (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT 'anonymous',
			age INTEGER,
			salary REAL,
			bio BLOB,
			is_active INTEGER DEFAULT 1,
			birthday TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := s.FetchMetadata(context.Background()); err != nil {
		t.Fatalf("FetchMetadata() error = %v", err)
	}

	if len(s.schema_.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(s.schema_.Tables))
	}

	tbl := s.schema_.Tables[0]
	if tbl.Name != "basic_types" {
		t.Errorf("expected basic_types, got %s", tbl.Name)
	}

	// Verify columns
	expectedCols := map[string]struct {
		typeName  string
		nullable  bool
		isPK      bool
		isAutoInc bool
	}{
		"id":         {"bigserial", true, true, true},
		"name":       {"text", false, false, false},
		"age":        {"bigint", true, false, false},
		"salary":     {"real", true, false, false},
		"bio":        {"bytea", true, false, false},
		"is_active":  {"bigint", true, false, false},
		"birthday":   {"text", true, false, false},
		"created_at": {"timestamptz", true, false, false},
	}

	if len(tbl.Columns) != len(expectedCols) {
		t.Errorf("expected %d columns, got %d", len(expectedCols), len(tbl.Columns))
	}

	for _, col := range tbl.Columns {
		exp, ok := expectedCols[col.Name]
		if !ok {
			t.Errorf("unexpected column %s", col.Name)
			continue
		}
		if !strings.EqualFold(col.TypeName, exp.typeName) {
			t.Errorf("column %s: expected type %s, got %s", col.Name, exp.typeName, col.TypeName)
		}
		if col.Nullable != exp.nullable {
			t.Errorf("column %s: expected nullable=%v, got %v", col.Name, exp.nullable, col.Nullable)
		}
		if col.IsPK != exp.isPK {
			t.Errorf("column %s: expected IsPK=%v, got %v", col.Name, exp.isPK, col.IsPK)
		}
	}
}

func TestMetadataIndexes(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer s.Close()

	_, err := s.db.ExecContext(context.Background(), `
		CREATE TABLE indexed_table (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL,
			name TEXT
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	_, err = s.db.ExecContext(context.Background(),
		`CREATE UNIQUE INDEX idx_email ON indexed_table(email)`)
	if err != nil {
		t.Fatalf("create index: %v", err)
	}

	if err := s.FetchMetadata(context.Background()); err != nil {
		t.Fatalf("FetchMetadata() error = %v", err)
	}

	tbl := s.schema_.Tables[0]
	t.Logf("Found %d indexes", len(tbl.Indexes))
	for _, idx := range tbl.Indexes {
		t.Logf("  index: %s (unique=%v, primary=%v, columns=%v)",
			idx.Name, idx.Unique, idx.Primary, idx.Columns)
	}

	if len(tbl.Indexes) < 1 {
		t.Errorf("expected at least 1 index, got %d", len(tbl.Indexes))
	}
}

func TestMetadataForeignKeys(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer s.Close()

	_, err := s.db.ExecContext(context.Background(), `
		CREATE TABLE parent (
			id INTEGER PRIMARY KEY,
			name TEXT
		)
	`)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	_, err = s.db.ExecContext(context.Background(), `
		CREATE TABLE child (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER,
			FOREIGN KEY (parent_id) REFERENCES parent(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	if err := s.FetchMetadata(context.Background()); err != nil {
		t.Fatalf("FetchMetadata() error = %v", err)
	}

	for _, tbl := range s.schema_.Tables {
		if tbl.Name == "child" {
			t.Logf("child table has %d foreign keys", len(tbl.ForeignKeys))
			for _, fk := range tbl.ForeignKeys {
				t.Logf("  FK: %s -> %s(%v)", fk.Columns, fk.ForeignTable, fk.ForeignColumns)
			}
			if len(tbl.ForeignKeys) == 0 {
				t.Error("expected at least 1 FK on child table")
			} else {
				fk := tbl.ForeignKeys[0]
				if fk.ForeignTable != "parent" {
					t.Errorf("expected foreign table 'parent', got '%s'", fk.ForeignTable)
				}
				if fk.DeleteRule != "CASCADE" {
					t.Errorf("expected delete rule CASCADE, got '%s'", fk.DeleteRule)
				}
			}
			break
		}
	}
}

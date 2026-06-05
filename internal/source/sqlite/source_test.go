package sqlite

import (
	"context"
	"testing"

	"github.com/tking320/pgloader-go/internal/cast"
)

func TestNewAndConnectInMemory(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)

	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer s.Close()

	// Create a test table
	_, err := s.db.ExecContext(context.Background(),
		`CREATE TABLE test_table (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, age INTEGER)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Insert test data
	_, err = s.db.ExecContext(context.Background(),
		`INSERT INTO test_table (name, age) VALUES ('Alice', 30), ('Bob', 25)`)
	if err != nil {
		t.Fatalf("insert data: %v", err)
	}

	// Fetch metadata
	if err := s.FetchMetadata(context.Background()); err != nil {
		t.Fatalf("FetchMetadata() error = %v", err)
	}

	names := s.TableNames()
	if len(names) != 1 || names[0] != "test_table" {
		t.Errorf("expected [test_table], got %v", names)
	}

	// Set active table and verify
	if err := s.SetActiveTable("test_table"); err != nil {
		t.Fatalf("SetActiveTable() error = %v", err)
	}
	if s.ActiveTable() == nil {
		t.Fatal("ActiveTable() returned nil")
	}
	if s.ActiveTable().Name != "test_table" {
		t.Errorf("expected test_table, got %s", s.ActiveTable().Name)
	}
}

func TestTableNameFiltering(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)

	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer s.Close()

	// Create tables
	for _, tbl := range []string{"customers", "orders", "tmp_import"} {
		_, err := s.db.ExecContext(context.Background(),
			`CREATE TABLE `+tbl+` (id INTEGER PRIMARY KEY)`)
		if err != nil {
			t.Fatalf("create table %s: %v", tbl, err)
		}
	}

	// Apply INCLUDE filter
	s.SetTableFilters([]string{"'customers'", "'orders'"}, nil)
	if err := s.FetchMetadata(context.Background()); err != nil {
		t.Fatalf("FetchMetadata() error = %v", err)
	}
	names := s.TableNames()
	if len(names) != 2 {
		t.Errorf("expected 2 tables after include filter, got %v", names)
	}
}

func TestConcurrencySupport(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)
	shards, err := s.ConcurrencySupport(context.Background(), 4)
	if err != nil {
		t.Errorf("ConcurrencySupport() error = %v", err)
	}
	if shards != nil {
		t.Errorf("expected nil shards for SQLite, got %d", len(shards))
	}
}

func TestClone(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)
	clone := s.Clone()
	if clone == nil {
		t.Fatal("Clone() returned nil")
	}
	// Verify clone has same filename but nil db
	sqliteClone := clone.(*SQLiteSource)
	if sqliteClone.filename != ":memory:" {
		t.Errorf("expected :memory:, got %s", sqliteClone.filename)
	}
	if sqliteClone.db != nil {
		t.Error("clone should have nil db (needs its own connection)")
	}
}

func TestEncoding(t *testing.T) {
	castEngine := cast.NewEngine(cast.SQLiteDefaultRules())
	s := New(":memory:", "public", "", nil, castEngine)
	if s.Encoding() != "UTF8" {
		t.Errorf("expected UTF8, got %s", s.Encoding())
	}
}

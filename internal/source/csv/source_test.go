package csv

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tking320/pgloader-go/internal/source"
)

func writeTestCSV(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCSVSource_BasicRead(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "test.csv",
		"name,age,city\nAlice,30,NYC\nBob,25,SF\n")

	src := NewCSVSource(csvPath, "mytable", WithHeader(true))

	ctx := context.Background()
	var rows []source.Row
	err := src.MapRows(ctx, func(row source.Row) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("MapRows() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if len(src.Columns) != 3 {
		t.Fatalf("got %d columns, want 3", len(src.Columns))
	}
	if src.Columns[0] != "name" || src.Columns[1] != "age" || src.Columns[2] != "city" {
		t.Errorf("columns = %v, want [name age city]", src.Columns)
	}
}

func TestCSVSource_NoHeader(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "test.csv",
		"1,hello,3.14\n2,world,2.71\n")

	src := NewCSVSource(csvPath, "t")

	ctx := context.Background()
	var rows []source.Row
	err := src.MapRows(ctx, func(row source.Row) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("MapRows() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if len(src.Columns) != 3 {
		t.Fatalf("got %d columns, want 3", len(src.Columns))
	}
	if !strings.HasPrefix(src.Columns[0], "col_") {
		t.Errorf("first column = %q, want 'col_1'", src.Columns[0])
	}
}

func TestCSVSource_WithNullIf(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "test.csv",
		"A,NULL,B\nNULL,,C\n")

	src := NewCSVSource(csvPath, "t",
		WithNullIf([]string{"NULL"}),
		WithHeader(false),
	)

	ctx := context.Background()
	var rows []source.Row
	err := src.MapRows(ctx, func(row source.Row) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("MapRows() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	// Row 0: A, NULL, B → A stays, NULL→nil, B stays
	if rows[0][0] == nil {
		t.Errorf("rows[0][0] = nil, want 'A'")
	}
	if rows[0][1] != nil {
		t.Errorf("rows[0][1] = %v, want nil", rows[0][1])
	}
	if rows[0][2] == nil {
		t.Errorf("rows[0][2] = nil, want 'B'")
	}

	// Row 1: NULL, empty, C → NULL→nil, empty→nil, C stays
	if rows[1][0] != nil {
		t.Errorf("rows[1][0] = %v, want nil", rows[1][0])
	}
	if rows[1][1] != nil {
		t.Errorf("rows[1][1] = %v, want nil", rows[1][1])
	}
	if rows[1][2] == nil {
		t.Errorf("rows[1][2] = nil, want 'C'")
	}
}

func TestCSVSource_EmptyFile(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "empty.csv", "")

	src := NewCSVSource(csvPath, "t")

	ctx := context.Background()
	var rows []source.Row
	err := src.MapRows(ctx, func(row source.Row) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("MapRows() error = %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows, want 0", len(rows))
	}
}

func TestGuessParams(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "test.csv",
		"name|age|city\nAlice|30|NYC\nBob|25|SF\n")

	params, err := GuessParams(csvPath)
	if err != nil {
		t.Fatalf("GuessParams() error = %v", err)
	}
	if params.Delimiter != '|' {
		t.Errorf("Delimiter = %q, want '|'", params.Delimiter)
	}
	if !params.HasHeader {
		t.Error("HasHeader = false, want true")
	}
	if params.NumCols != 3 {
		t.Errorf("NumCols = %d, want 3", params.NumCols)
	}
}

func TestGuessParams_TabDelimited(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "test.tsv",
		"a\tb\tc\n1\t2\t3\n4\t5\t6\n")

	params, err := GuessParams(csvPath)
	if err != nil {
		t.Fatalf("GuessParams() error = %v", err)
	}
	if params.Delimiter != '\t' {
		t.Errorf("Delimiter = %q, want '\\t'", params.Delimiter)
	}
}

func TestCSVSource_WithCustomColumns(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "test.csv",
		"1,2,3\n4,5,6\n")

	src := NewCSVSource(csvPath, "t",
		WithColumns([]string{"a", "b", "c"}),
	)

	ctx := context.Background()
	var rows []source.Row
	err := src.MapRows(ctx, func(row source.Row) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("MapRows() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if len(src.Columns) != 3 {
		t.Fatalf("got %d columns, want 3", len(src.Columns))
	}
	if src.Columns[0] != "a" {
		t.Errorf("columns[0] = %q, want 'a'", src.Columns[0])
	}
}

func TestCSVSource_TabDelimiter(t *testing.T) {
	csvPath := writeTestCSV(t, t.TempDir(), "test.tsv",
		"1\t2\t3\n4\t5\t6\n")

	src := NewCSVSource(csvPath, "t",
		WithDelimiter('\t'),
		WithColumns([]string{"x", "y", "z"}),
	)

	ctx := context.Background()
	var rows []source.Row
	err := src.MapRows(ctx, func(row source.Row) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("MapRows() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// First value of first row should be "1"
	val := rows[0][0].(string)
	n, err := strconv.Atoi(val)
	if err != nil || n != 1 {
		t.Errorf("rows[0][0] = %v, want 1", rows[0][0])
	}
}

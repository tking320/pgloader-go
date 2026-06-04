package copy

import (
	"testing"

	"github.com/tking320/pgloader-go/internal/source"
)

func TestFormatRowToCopyText_BasicTypes(t *testing.T) {
	tests := []struct {
		name string
		row  source.Row
		want string
	}{
		{
			name: "strings and ints",
			row:  source.Row{"hello", int64(42), 3.14},
			want: "hello\t42\t3.14\n",
		},
		{
			name: "nil values",
			row:  source.Row{nil, "text", nil},
			want: `\N` + "\t" + "text" + "\t" + `\N` + "\n",
		},
		{
			name: "booleans",
			row:  source.Row{true, false},
			want: "t\tf\n",
		},
		{
			name: "escape tab",
			row:  source.Row{"tab\there"},
			want: "tab\\there\n",
		},
		{
			name: "escape newline",
			row:  source.Row{"new\nline"},
			want: "new\\nline\n",
		},
		{
			name: "escape backslash",
			row:  source.Row{"back\\slash"},
			want: "back\\\\slash\n",
		},
		{
			name: "bytes",
			row:  source.Row{[]byte("binary")},
			want: "\\\\x62696e617279\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatRowToCopyText(tt.row)
			if err != nil {
				t.Fatalf("FormatRowToCopyText() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("FormatRowToCopyText() = %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestBatch_AddRow(t *testing.T) {
	batch := NewBatch(3, 1000)
	row := source.Row{"a", int64(1)}

	err := batch.AddRow(row, FormatRowToCopyText)
	if err != nil {
		t.Fatalf("AddRow() error = %v", err)
	}
	if batch.RowCount != 1 {
		t.Errorf("RowCount = %d, want 1", batch.RowCount)
	}

	// Fill batch to capacity
	batch.AddRow(row, FormatRowToCopyText)
	batch.AddRow(row, FormatRowToCopyText)
	err = batch.AddRow(row, FormatRowToCopyText)
	if err != ErrBatchFull {
		t.Errorf("AddRow() error = %v, want ErrBatchFull", err)
	}
}

func TestBatch_Reset(t *testing.T) {
	batch := NewBatch(100, 10000)
	for i := 0; i < 5; i++ {
		batch.AddRow(source.Row{int64(i)}, FormatRowToCopyText)
	}
	batch.Reset()
	if batch.RowCount != 0 || batch.ByteCount != 0 || len(batch.Data) != 0 {
		t.Errorf("after Reset: RowCount=%d ByteCount=%d Data=%d, want all zero",
			batch.RowCount, batch.ByteCount, len(batch.Data))
	}
}

func TestFormatRowToCopyText_VariousIntTypes(t *testing.T) {
	tests := []struct {
		val  any
		want string
	}{
		{int64(123), "123"},
		{int32(456), "456"},
		{int16(78), "78"},
		{int8(9), "9"},
		{int(-1), "-1"},
		{uint64(999), "999"},
		{float64(3.14), "3.14"},
		{float32(2.5), "2.5"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got, err := FormatRowToCopyText(source.Row{tt.val})
			if err != nil {
				t.Fatalf("FormatRowToCopyText() error = %v", err)
			}
			// Remove trailing \n
			result := string(got[:len(got)-1])
			if result != tt.want {
				t.Errorf("FormatRowToCopyText() = %q, want %q", result, tt.want)
			}
		})
	}
}

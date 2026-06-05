// Package copy implements the PostgreSQL COPY protocol for data loading.
// It handles batch management, wire-format encoding, and error retry.
package copy

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/tking320/pgloader-go/internal/source"
)

// ---------------------------------------------------------------------------
// Batch: accumulates rows into pre-formatted COPY data
// ---------------------------------------------------------------------------

// Batch holds a collection of pre-formatted rows ready for COPY.
type Batch struct {
	Data      [][]byte // pre-formatted COPY data per row
	RowCount  int
	ByteCount int64
	MaxRows   int   // maximum number of rows
	MaxBytes  int64 // maximum total bytes (approximate)
}

// NewBatch creates a batch with the given limits.
func NewBatch(maxRows int, maxBytes int64) *Batch {
	return &Batch{
		Data:     make([][]byte, 0, maxRows),
		MaxRows:  maxRows,
		MaxBytes: maxBytes,
	}
}

// AddRow formats a row into COPY text format and appends it to the batch.
// Returns ErrBatchFull if the batch is full after adding this row.
func (b *Batch) AddRow(row source.Row, formatFn func(source.Row) ([]byte, error)) error {
	formatted, err := formatFn(row)
	if err != nil {
		return fmt.Errorf("format row: %w", err)
	}

	b.Data = append(b.Data, formatted)
	b.RowCount++
	b.ByteCount += int64(len(formatted))

	if b.RowCount >= b.MaxRows || b.ByteCount >= b.MaxBytes {
		return ErrBatchFull
	}
	return nil
}

// Reset clears the batch for reuse, retaining the underlying slice capacity.
func (b *Batch) Reset() {
	b.Data = b.Data[:0]
	b.RowCount = 0
	b.ByteCount = 0
}

// ErrBatchFull is returned by AddRow when the batch has reached capacity.
var ErrBatchFull = fmt.Errorf("batch is full")

// ---------------------------------------------------------------------------
// COPY text format encoding
// ---------------------------------------------------------------------------

// FormatRowToCopyText encodes a row into PostgreSQL COPY text format.
//
// Rules:
//   - NULL values are encoded as \N (backslash-N)
//   - Columns are separated by tabs (\t)
//   - Rows are terminated by newlines (\n)
//   - Special characters in data are escaped: \n, \r, \t, \\, \b, \f, \v
//   - Backslash itself must be escaped (\\)
//   - All string values are sanitized to valid UTF-8 before encoding
//   - []byte values (from binary/bytea columns) are hex-encoded as \x<hex>
func FormatRowToCopyText(row source.Row) ([]byte, error) {
	var buf bytes.Buffer

	for i, val := range row {
		if i > 0 {
			buf.WriteByte('\t')
		}

		if val == nil {
			buf.WriteString(`\N`)
			continue
		}

		switch v := val.(type) {
		case []byte:
			hexEncodeCopyBytes(&buf, v)
		case string:
			escapeCopyString(&buf, v)
		case int64, int32, int16, int8, int:
			fmt.Fprint(&buf, v)
		case uint64, uint32, uint16, uint8:
			fmt.Fprint(&buf, v)
		case float64, float32:
			fmt.Fprint(&buf, v)
		case bool:
			if v {
				buf.WriteString("t")
			} else {
				buf.WriteString("f")
			}
		default:
			s := fmt.Sprint(v)
			escapeCopyString(&buf, s)
		}
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// hexEncodeCopyBytes encodes binary data as PostgreSQL hex format (\\x....).
// The double backslash is required in COPY text mode so that after COPY text
// parsing (which interprets \\ as a single backslash), the bytea input parser
// sees the hex format \x<hex>.
func hexEncodeCopyBytes(buf *bytes.Buffer, data []byte) {
	buf.WriteString(`\\x`)
	const hexDigits = "0123456789abcdef"
	for _, b := range data {
		buf.WriteByte(hexDigits[b>>4])
		buf.WriteByte(hexDigits[b&0xf])
	}
}

// escapeCopyString writes s to buf, escaping special COPY characters.
// Invalid UTF-8 sequences are replaced with the Unicode replacement character
// to prevent PostgreSQL from rejecting the batch.
func escapeCopyString(buf *bytes.Buffer, s string) {
	// Sanitize to valid UTF-8 first (replaces invalid sequences with U+FFFD)
	s = strings.ToValidUTF8(s, "?")
	for _, r := range s {
		switch r {
		case '\\':
			buf.WriteString(`\\`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\v':
			buf.WriteString(`\v`)
		default:
			buf.WriteRune(r)
		}
	}
}

// escapeCopyBytes writes b to buf, escaping special COPY characters.
func escapeCopyBytes(buf *bytes.Buffer, b []byte) {
	for _, c := range b {
		switch c {
		case '\\':
			buf.WriteString(`\\`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\v':
			buf.WriteString(`\v`)
		default:
			buf.WriteByte(c)
		}
	}
}

// ---------------------------------------------------------------------------
// CopyWriter: sends batches to PostgreSQL via the COPY protocol
// ---------------------------------------------------------------------------

// CopyWriter manages writing COPY data to a PostgreSQL connection.
type CopyWriter struct {
	conn *pgx.Conn
}

// NewCopyWriter creates a CopyWriter for the given connection.
func NewCopyWriter(conn *pgx.Conn) *CopyWriter {
	return &CopyWriter{conn: conn}
}

// FlushBatch sends the pre-formatted rows in a batch to PostgreSQL via COPY.
// It constructs a COPY ... FROM STDIN command and pipes the data.
func (w *CopyWriter) FlushBatch(ctx context.Context, schema string, table string, columns []string, batch *Batch) error {
	if batch.RowCount == 0 {
		return nil
	}

	// Build COPY header
	var copySQL string
	if len(columns) > 0 {
		colList := make([]string, len(columns))
		for i, c := range columns {
			colList[i] = quoteIdent(c)
		}
		copySQL = fmt.Sprintf("COPY %s (%s) FROM STDIN",
			quoteQualified(schema, table),
			strings.Join(colList, ", "))
	} else {
		copySQL = fmt.Sprintf("COPY %s FROM STDIN",
			quoteQualified(schema, table))
	}

	// Send all formatted rows concatenated
	var allData bytes.Buffer
	for _, row := range batch.Data {
		allData.Write(row)
	}

	_, err := w.conn.PgConn().CopyFrom(ctx, &allData, copySQL)
	return err
}

// ---------------------------------------------------------------------------
// Retry logic: binary-search for bad rows
// ---------------------------------------------------------------------------

// RetryResult represents the outcome of a batch retry operation.
type RetryResult struct {
	Imported int          // number of rows successfully imported
	Rejected []source.Row // rows that failed (bad data)
}

// RetryBatch handles a COPY error by performing binary search to isolate
// bad rows, importing the good ones.
func RetryBatch(ctx context.Context, conn *pgx.Conn, schema, table string, columns []string,
	rows []source.Row, formatted [][]byte, writer *CopyWriter) (*RetryResult, error) {

	result := &RetryResult{}

	// Try the whole batch first
	batch := NewBatch(len(rows), int64(len(formatted))*200)
	batch.Data = formatted
	batch.RowCount = len(formatted)
	for _, f := range formatted {
		batch.ByteCount += int64(len(f))
	}

	err := writer.FlushBatch(ctx, schema, table, columns, batch)
	if err == nil {
		result.Imported = len(rows)
		return result, nil
	}

	// Binary search: split and retry each half
	badIndices := findBadRows(ctx, conn, schema, table, columns, rows, formatted, 0, len(formatted)-1, writer)

	// Import the good rows & collect the bad ones
	goodFormatted := make([][]byte, 0, len(formatted))
	goodRows := make([]source.Row, 0, len(rows))
	badSet := make(map[int]bool)
	for _, idx := range badIndices {
		badSet[idx] = true
	}

	for i, row := range rows {
		if badSet[i] {
			result.Rejected = append(result.Rejected, row)
		} else {
			goodRows = append(goodRows, row)
			goodFormatted = append(goodFormatted, formatted[i])
		}
	}

	// Import the cleaned batch
	if len(goodFormatted) > 0 {
		cleanBatch := NewBatch(len(goodFormatted), int64(len(goodFormatted))*200)
		cleanBatch.Data = goodFormatted
		cleanBatch.RowCount = len(goodFormatted)
		for _, f := range goodFormatted {
			cleanBatch.ByteCount += int64(len(f))
		}
		if err := writer.FlushBatch(ctx, schema, table, columns, cleanBatch); err != nil {
			return result, fmt.Errorf("retry flush failed: %w", err)
		}
		result.Imported = len(goodRows)
	}

	return result, nil
}

// findBadRows returns the indices of rows that cause COPY errors via binary search.
func findBadRows(ctx context.Context, conn *pgx.Conn, schema, table string, columns []string,
	rows []source.Row, formatted [][]byte, lo, hi int, writer *CopyWriter) []int {

	if lo > hi {
		return nil
	}

	// Test the sub-range
	if testRange(ctx, conn, schema, table, columns, formatted, lo, hi, writer) {
		return nil // no bad rows in this range
	}

	if lo == hi {
		return []int{lo} // single bad row
	}

	// Split and recurse
	mid := (lo + hi) / 2
	leftBad := findBadRows(ctx, conn, schema, table, columns, rows, formatted, lo, mid, writer)
	rightBad := findBadRows(ctx, conn, schema, table, columns, rows, formatted, mid+1, hi, writer)
	return append(leftBad, rightBad...)
}

// testRange tests whether the given range of rows can be COPY'd successfully.
func testRange(ctx context.Context, conn *pgx.Conn, schema, table string, columns []string,
	formatted [][]byte, lo, hi int, writer *CopyWriter) bool {

	if lo > hi {
		return true
	}
	batch := NewBatch(hi-lo+1, 0)
	for i := lo; i <= hi; i++ {
		batch.Data = append(batch.Data, formatted[i])
		batch.ByteCount += int64(len(formatted[i]))
	}
	batch.RowCount = hi - lo + 1

	err := writer.FlushBatch(ctx, schema, table, columns, batch)
	return err == nil
}

// ---------------------------------------------------------------------------
// Identifier helpers
// ---------------------------------------------------------------------------

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteQualified(schema, table string) string {
	if schema != "" {
		return quoteIdent(schema) + "." + quoteIdent(table)
	}
	return quoteIdent(table)
}

// Package csv implements loading data from CSV files into PostgreSQL.
// It corresponds to pgloader's src/sources/csv/csv.lisp.
package csv

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tking320/pgloader-go/internal/source"
)

// ---------------------------------------------------------------------------
// CSVSource
// ---------------------------------------------------------------------------

// CSVSource implements source.Source for CSV files.
type CSVSource struct {
	FilePath      string
	TargetName    string
	Fields        []source.Field
	Columns       []string
	Enc           string
	SkipLines     int
	HasHeader     bool
	Delimiter     rune
	Quote         rune
	Escape        rune
	Comment       rune
	TrimBlanks    bool
	NullIf        []string
	LazyQuotes    bool
	KeepNullIfs   bool // keep empty strings instead of converting to nil
}

// NewCSVSource creates a CSVSource with the given parameters.
func NewCSVSource(filePath, targetName string, opts ...Option) *CSVSource {
	s := &CSVSource{
		FilePath:   filePath,
		TargetName: targetName,
		Delimiter:  ',',
		Quote:      '"',
		Escape:     '"',
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option configures a CSVSource.
type Option func(*CSVSource)

// WithDelimiter sets the CSV delimiter.
func WithDelimiter(d rune) Option { return func(s *CSVSource) { s.Delimiter = d } }

// WithQuote sets the CSV quote character.
func WithQuote(q rune) Option { return func(s *CSVSource) { s.Quote = q } }

// WithEscape sets the CSV escape character.
func WithEscape(e rune) Option { return func(s *CSVSource) { s.Escape = e } }

// WithSkipLines skips the first N lines of the file.
func WithSkipLines(n int) Option { return func(s *CSVSource) { s.SkipLines = n } }

// WithHeader indicates the first row is a header.
func WithHeader(v bool) Option { return func(s *CSVSource) { s.HasHeader = v } }

// WithEncoding sets the file encoding.
func WithEncoding(enc string) Option { return func(s *CSVSource) { s.Enc = enc } }

// WithColumns sets the target column names.
func WithColumns(cols []string) Option { return func(s *CSVSource) { s.Columns = cols } }

// WithFields sets the field definitions.
func WithFields(fields []source.Field) Option { return func(s *CSVSource) { s.Fields = fields } }

// WithTrimBlanks enables trimming of leading/trailing whitespace.
func WithTrimBlanks(v bool) Option { return func(s *CSVSource) { s.TrimBlanks = v } }

// WithNullIf sets strings that should be treated as NULL.
func WithNullIf(v []string) Option { return func(s *CSVSource) { s.NullIf = v } }

// WithLazyQuotes enables lazy quote handling.
func WithLazyQuotes(v bool) Option { return func(s *CSVSource) { s.LazyQuotes = v } }

// ---------------------------------------------------------------------------
// Source interface implementation
// ---------------------------------------------------------------------------

func (s *CSVSource) TableName() string              { return s.TargetName }
func (s *CSVSource) Encoding() string               { return s.Enc }
func (s *CSVSource) DataIsPreformatted() bool        { return false }
func (s *CSVSource) CopyColumnList() []string        { return s.Columns }
func (s *CSVSource) ConcurrencySupport(_ context.Context, _ int) ([]source.Source, error) {
	return nil, nil // CSV files don't support concurrent sharding
}
func (s *CSVSource) Clone() source.Source {
	clone := *s
	return &clone
}

// MapRows reads all rows from the CSV file and calls processRow for each.
func (s *CSVSource) MapRows(ctx context.Context, processRow func(source.Row) error) error {
	f, err := os.Open(s.FilePath)
	if err != nil {
		return fmt.Errorf("open CSV file %s: %w", s.FilePath, err)
	}
	defer f.Close()

	reader := csv.NewReader(bufio.NewReader(f))
	reader.Comma = s.Delimiter
	reader.Comment = s.Comment
	reader.LazyQuotes = s.LazyQuotes
	reader.TrimLeadingSpace = s.TrimBlanks
	reader.ReuseRecord = true

	// Handle quoting
	if s.Quote != 0 {
		reader.LazyQuotes = s.LazyQuotes || s.Quote != '"'
	}

	// Skip lines before the header/data
	for i := 0; i < s.SkipLines; i++ {
		if _, err := reader.Read(); err != nil {
			return fmt.Errorf("skip line %d: %w", i+1, err)
		}
	}

	// If has header, read column names from first data row
	if s.HasHeader {
		header, err := reader.Read()
		if err != nil {
			return fmt.Errorf("read header: %w", err)
		}
		if len(s.Columns) == 0 {
			// Use header as implicit column names
			s.Columns = make([]string, len(header))
			for i, h := range header {
				s.Columns[i] = strings.TrimSpace(h)
			}
		}
	}

	// If no columns specified, generate placeholder names
	if len(s.Columns) == 0 {
		// We need to peek at first row to determine column count
		firstRow, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				return nil // empty file
			}
			return fmt.Errorf("read first row: %w", err)
		}
		s.Columns = make([]string, len(firstRow))
		for i := range s.Columns {
			s.Columns[i] = fmt.Sprintf("col_%d", i+1)
		}
		// Process the first row
		row := buildRow(s, firstRow)
		if err := processRow(row); err != nil {
			return err
		}
	}

	// Read remaining rows
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read CSV row: %w", err)
		}

		row := buildRow(s, record)
		if err := processRow(row); err != nil {
			return err
		}
	}

	return nil
}

// buildRow converts a CSV record ([]string) to a Row ([]any),
// applying NullIf replacements.
func buildRow(s *CSVSource, record []string) source.Row {
	row := make(source.Row, len(record))
	for i, val := range record {
		row[i] = applyNullIf(s, val)
	}
	return row
}

// applyNullIf checks if the value matches a NullIf pattern.
func applyNullIf(s *CSVSource, val string) any {
	val = strings.TrimSpace(val)
	if val == "" && !s.KeepNullIfs {
		return nil
	}
	for _, n := range s.NullIf {
		if val == n {
			return nil
		}
	}
	return val
}

// ---------------------------------------------------------------------------
// CSV parameter guessing
// ---------------------------------------------------------------------------

// GuessedParams holds the automatically detected CSV parameters.
type GuessedParams struct {
	Delimiter rune
	Quote     rune
	HasHeader bool
	SkipLines int
	NumCols   int
}

// candidateDelimiters lists delimiters to try when guessing.
var candidateDelimiters = []rune{',', ';', '|', '\t', '^'}

// GuessParams attempts to auto-detect CSV parameters from the file content.
func GuessParams(filePath string) (*GuessedParams, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open for guessing: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() && len(lines) < 10 {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty file")
	}

	bestDelim := ','
	bestScore := 0
	for _, delim := range candidateDelimiters {
		score := scoreDelimiter(lines, delim)
		if score > bestScore {
			bestScore = score
			bestDelim = delim
		}
	}

	// Guess header: check if first line looks like a header (non-numeric)
	hasHeader := guessHasHeader(lines, bestDelim)

	return &GuessedParams{
		Delimiter: bestDelim,
		Quote:     '"',
		HasHeader: hasHeader,
		SkipLines: 0,
		NumCols:   countCols(lines[0], bestDelim),
	}, nil
}

// scoreDelimiter returns a score for how likely 'delim' is the delimiter.
func scoreDelimiter(lines []string, delim rune) int {
	score := 0
	for _, line := range lines {
		count := strings.Count(line, string(delim))
		if count > 1 {
			score += count
		}
	}
	return score
}

// guessHasHeader checks if the first line looks like a header.
func guessHasHeader(lines []string, delim rune) bool {
	if len(lines) < 2 {
		return false
	}
	firstCols := strings.Split(lines[0], string(delim))
	secondCols := strings.Split(lines[1], string(delim))
	if len(firstCols) != len(secondCols) {
		return false
	}
	for _, c := range firstCols {
		c = strings.TrimSpace(c)
		if len(c) > 0 && isNumeric(c) {
			return false
		}
	}
	return true
}

// countCols returns the number of columns when split by delim.
func countCols(line string, delim rune) int {
	return len(strings.Split(line, string(delim)))
}

// isNumeric returns true if the string looks like a number.
func isNumeric(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '.' || r == '-' || r == '+' || r == 'e' || r == 'E' {
			continue
		}
		return false
	}
	return len(s) > 0
}

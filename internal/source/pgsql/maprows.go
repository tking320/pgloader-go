package pgsql

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tking320/pgloader-go/internal/source"
)

// MapRows — reads data via COPY ... TO STDOUT
func (s *PgSQLSource) MapRows(ctx context.Context, processRow func(source.Row) error) error {
	if s.schema_ == nil || len(s.schema_.Tables) == 0 {
		return fmt.Errorf("no table metadata: call FetchMetadata first")
	}

	t := s.ActiveTable()
	if t == nil {
		return fmt.Errorf("no active table")
	}

	conn, err := s.srcPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire source connection: %w", err)
	}
	defer conn.Release()

	colNames := make([]string, len(t.Columns))
	for i, col := range t.Columns {
		if col.Transform == "money-to-numeric" {
			colNames[i] = fmt.Sprintf("%s::numeric AS %s", quoteIdent(col.Name), quoteIdent(col.Name))
		} else {
			colNames[i] = quoteIdent(col.Name)
		}
	}
	colList := strings.Join(colNames, ", ")

	query := fmt.Sprintf("COPY (SELECT %s FROM %s) TO STDOUT WITH (FORMAT text)",
		colList, t.QualifiedName())
	if s.whereClause != "" {
		query = fmt.Sprintf("COPY (SELECT %s FROM %s WHERE %s) TO STDOUT WITH (FORMAT text)",
			colList, t.QualifiedName(), s.whereClause)
	}

	pr, pw := io.Pipe()

	go func() {
		_, err := conn.Conn().PgConn().CopyTo(ctx, pw, query)
		pw.CloseWithError(err)
	}()

	const maxRowSize = 256 * 1024 * 1024
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 1024*1024), maxRowSize)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			pr.Close()
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		rowData := make([]byte, len(line))
		copy(rowData, line)
		rowData = append(rowData, '\n')

		if err := processRow(source.Row{rowData}); err != nil {
			pr.Close()
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read COPY stream: %w", err)
	}

	return nil
}

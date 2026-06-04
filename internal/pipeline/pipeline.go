// Package pipeline orchestrates the data loading pipeline:
// reader → batch → PostgreSQL COPY writer.
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/config"
	"github.com/tking320/pgloader-go/internal/copy"
	"github.com/tking320/pgloader-go/internal/monitor"
	"github.com/tking320/pgloader-go/internal/source"
)

// Pipeline manages the complete data loading pipeline for a single table.
type Pipeline struct {
	cfg    *config.Config
	src    source.Source
	pool   *pgxpool.Pool
	mon    *monitor.Monitor
	schema string
	table  string
}

// New creates a new Pipeline.
func New(cfg *config.Config, src source.Source, pool *pgxpool.Pool, mon *monitor.Monitor, schema, table string) *Pipeline {
	return &Pipeline{cfg: cfg, src: src, pool: pool, mon: mon, schema: schema, table: table}
}

// Run executes the pipeline: reads from source, batches rows, writes via COPY.
func (p *Pipeline) Run(ctx context.Context) error {
	tableName := p.table
	if p.schema != "" {
		tableName = p.schema + "." + p.table
	}

	p.mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: tableName}
	startTime := time.Now()

	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	writer := copy.NewCopyWriter(conn.Conn())
	batch := copy.NewBatch(p.cfg.BatchSize, p.cfg.BatchBytes)

	err = p.src.MapRows(ctx, func(row source.Row) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var formatted []byte
		var err error

		if p.src.DataIsPreformatted() {
			// Preformatted data: row[0] is the raw COPY bytes
			if len(row) > 0 {
				if data, ok := row[0].([]byte); ok {
					formatted = data
				} else {
					return fmt.Errorf("expected preformatted []byte, got %T", row[0])
				}
			}
		} else {
			formatted, err = copy.FormatRowToCopyText(row)
			if err != nil {
				return fmt.Errorf("format row: %w", err)
			}
		}

		batch.Data = append(batch.Data, formatted)
		batch.RowCount++
		batch.ByteCount += int64(len(formatted))

		p.mon.Events() <- monitor.Event{Type: monitor.EventRead, Table: tableName, Count: 1}

		if batch.RowCount >= p.cfg.BatchSize || batch.ByteCount >= p.cfg.BatchBytes {
			if err := p.flush(ctx, conn.Conn(), writer, batch, tableName); err != nil {
				return fmt.Errorf("flush: %w", err)
			}
			batch.Reset()
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("map rows: %w", err)
	}

	// Flush remaining rows
	if batch.RowCount > 0 {
		if err := p.flush(ctx, conn.Conn(), writer, batch, tableName); err != nil {
			return fmt.Errorf("flush final: %w", err)
		}
	}

	elapsed := time.Since(startTime).Seconds()
	p.mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: tableName, Secs: elapsed}
	return nil
}

func (p *Pipeline) flush(ctx context.Context, conn *pgx.Conn, writer *copy.CopyWriter, batch *copy.Batch, tableName string) error {
	cols := p.src.CopyColumnList()
	err := writer.FlushBatch(ctx, p.schema, p.table, cols, batch)
	if err == nil {
		p.mon.Events() <- monitor.Event{
			Type: monitor.EventWrite, Table: tableName,
			Count: int64(batch.RowCount), Bytes: batch.ByteCount,
		}
		return nil
	}

	// COPY failed — log the error
	p.mon.Events() <- monitor.Event{
		Type: monitor.EventError, Table: tableName,
		Count: int64(batch.RowCount), Err: err,
	}
	return fmt.Errorf("COPY failed: %w", err)
}

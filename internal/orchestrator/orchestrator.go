// Package orchestrator coordinates the full schema migration lifecycle:
// fetch metadata → prepare target → copy data → complete target.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tking320/pgloader-go/internal/config"
	"github.com/tking320/pgloader-go/internal/monitor"
	"github.com/tking320/pgloader-go/internal/pipeline"
	"github.com/tking320/pgloader-go/internal/source"
)

// Migration orchestrates a full database migration.
type Migration struct {
	cfg  *config.Config
	src  source.DbSource
	pool *pgxpool.Pool
	mon  *monitor.Monitor

	// Target schema for all tables
	targetSchema string
}

// NewMigration creates a new Migration orchestrator.
func NewMigration(cfg *config.Config, src source.DbSource, pool *pgxpool.Pool, mon *monitor.Monitor, targetSchema string) *Migration {
	return &Migration{
		cfg:          cfg,
		src:          src,
		pool:         pool,
		mon:          mon,
		targetSchema: targetSchema,
	}
}

// Run executes the full migration lifecycle.
func (m *Migration) Run(ctx context.Context) error {
	startTime := time.Now()

	// Phase 1: Fetch metadata from source
	m.mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: "fetch"}

	if err := m.src.FetchMetadata(ctx); err != nil {
		return fmt.Errorf("fetch metadata: %w", err)
	}

	m.mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "fetch"}

	// Build prepare options from config
	prepareOpts := source.PrepareOptions{
		CreateTables:  m.cfg.CreateTables,
		Truncate:      m.cfg.Truncate,
		IncludeDrop:   m.cfg.IncludeDrop,
		CreateSchemas: true,
		DropIndexes:   m.cfg.IncludeDrop,
	}

	// Phase 2: Prepare target schema
	m.mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: "create, drop, truncate"}

	if err := m.src.PrepareTarget(ctx, prepareOpts); err != nil {
		return fmt.Errorf("prepare target: %w", err)
	}

	m.mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "create, drop, truncate"}

	// Phase 3: Copy data for each table
	if !m.cfg.SchemaOnly {
		if err := m.copyAllTables(ctx); err != nil {
			return err
		}
	}

	// Phase 4: Complete target (indexes, FKs, sequences)
	if !m.cfg.DataOnly && !m.cfg.SchemaOnly {
		completeOpts := source.CompleteOptions{
			CreateIndexes:  m.cfg.CreateIndexes,
			ForeignKeys:    m.cfg.ForeignKeys,
			CreateTriggers: m.cfg.CreateTriggers,
			ResetSequences: m.cfg.ResetSequences,
			Comments:       m.cfg.Comments,
		}

		m.mon.Events() <- monitor.Event{Type: monitor.EventStart, Table: "create indexes, fkeys"}

		if err := m.src.CompleteTarget(ctx, completeOpts); err != nil {
			return fmt.Errorf("complete target: %w", err)
		}

		m.mon.Events() <- monitor.Event{Type: monitor.EventEnd, Table: "create indexes, fkeys"}
	}

	elapsed := time.Since(startTime).Seconds()
	m.mon.Events() <- monitor.Event{
		Type:  monitor.EventEnd,
		Table: "all tables",
		Secs:  elapsed,
		Count: -1, // signal: total time
	}

	return nil
}

// copyAllTables runs the data pipeline for each table in the catalog.
// When the source supports PK-range sharding and concurrency > 1,
// tables are loaded in parallel using multiple shards.
func (m *Migration) copyAllTables(ctx context.Context) error {
	tableNames := m.src.TableNames()
	if len(tableNames) == 0 {
		return nil
	}

	for _, tableName := range tableNames {
		if err := m.src.SetActiveTable(tableName); err != nil {
			return fmt.Errorf("set active table %s: %w", tableName, err)
		}

		// Check if this table supports PK-range sharding for concurrent COPY
		concurrency := m.cfg.Concurrency
		shards, err := m.src.ConcurrencySupport(ctx, concurrency)
		if err != nil {
			return fmt.Errorf("concurrency support for %s: %w", tableName, err)
		}

		if len(shards) > 0 {
			// Parallel: run one pipeline per shard, each covering a PK range
			if err := m.copyWithConcurrency(ctx, shards, tableName); err != nil {
				return fmt.Errorf("parallel copy %s: %w", tableName, err)
			}
		} else {
			// Sequential: single pipeline for the whole table
			pipe := pipeline.New(m.cfg, m.src, m.pool, m.mon, m.targetSchema, tableName)
			if err := pipe.Run(ctx); err != nil {
				return fmt.Errorf("pipeline for %s: %w", tableName, err)
			}
		}
	}
	return nil
}

// copyWithConcurrency runs N shard pipelines in parallel for a single table.
// Each shard reads a different PK range from the source and writes via its own
// COPY session to PostgreSQL, using a dedicated connection from the pool.
// When one shard fails, the others are cancelled via context.
func (m *Migration) copyWithConcurrency(ctx context.Context, shards []source.Source, tableName string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, len(shards))

	for _, shard := range shards {
		wg.Add(1)
		go func(s source.Source) {
			defer wg.Done()
			pipe := pipeline.New(m.cfg, s, m.pool, m.mon, m.targetSchema, tableName)
			if err := pipe.Run(ctx); err != nil {
				errCh <- err
				cancel()
			}
		}(shard)
	}

	wg.Wait()
	close(errCh)

	// Return first non-cancel error; skip context.Canceled which can
	// arrive from goroutines that observed the cancel() from another shard.
	var firstErr error
	for err := range errCh {
		if err == nil {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		if !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return firstErr
}

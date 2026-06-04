// Package orchestrator coordinates the full schema migration lifecycle:
// fetch metadata → prepare target → copy data → complete target.
package orchestrator

import (
	"context"
	"fmt"
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
func (m *Migration) copyAllTables(ctx context.Context) error {
	tableNames := m.src.TableNames()
	if len(tableNames) == 0 {
		return nil
	}

	for _, tableName := range tableNames {
		if err := m.src.SetActiveTable(tableName); err != nil {
			return fmt.Errorf("set active table %s: %w", tableName, err)
		}

		pipe := pipeline.New(m.cfg, m.src, m.pool, m.mon, m.targetSchema, tableName)
		if err := pipe.Run(ctx); err != nil {
			return fmt.Errorf("pipeline for %s: %w", tableName, err)
		}
	}
	return nil
}

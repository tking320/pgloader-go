// Package monitor provides statistics collection and reporting for data loading.
// It corresponds to pgloader's src/utils/monitor.lisp and src/utils/report.lisp.
package monitor

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// EventType describes the kind of event being reported.
type EventType int

const (
	EventStart    EventType = iota // loading started
	EventEnd                       // loading ended
	EventRead                      // row read from source
	EventWrite                     // row written to target
	EventError                     // row error
	EventPrepare                   // schema preparation
	EventFinalize                  // schema finalization
	EventMessage                   // log message
)

// Event represents a single monitoring event.
type Event struct {
	Type  EventType
	Table string
	Secs  float64
	Count int64
	Bytes int64
	Err   error
}

// TableStats tracks per-table loading statistics.
type TableStats struct {
	Name         string
	RowsRead     int64
	RowsWritten  int64
	Errors       int64
	Bytes        int64
	StartTime    time.Time
	EndTime      time.Time
	IsPhase      bool    // true for phase timing entries (fetch metadata, create indexes, etc.)
	extraElapsed float64 // wall-clock elapsed from Event.Secs, used for "all tables" total
}

// Elapsed returns the elapsed time in seconds.
func (ts *TableStats) Elapsed() float64 {
	if ts.extraElapsed > 0 {
		return ts.extraElapsed
	}
	if ts.EndTime.IsZero() {
		return time.Since(ts.StartTime).Seconds()
	}
	return ts.EndTime.Sub(ts.StartTime).Seconds()
}

// ---------------------------------------------------------------------------
// Monitor
// ---------------------------------------------------------------------------

// Monitor collects events and produces summary reports.
type Monitor struct {
	mu      sync.Mutex
	tables  map[string]*TableStats
	order   []string // insertion order for deterministic iteration
	events  chan Event
	done    chan struct{}
	output  io.Writer
	verbose bool
}

// NewMonitor creates a new Monitor.
func NewMonitor(output io.Writer, verbose bool) *Monitor {
	return &Monitor{
		tables:  make(map[string]*TableStats),
		order:   make([]string, 0),
		events:  make(chan Event, 1000),
		done:    make(chan struct{}),
		output:  output,
		verbose: verbose,
	}
}

// Events returns the event channel for sending events.
func (m *Monitor) Events() chan<- Event {
	return m.events
}

// Start begins processing events in the background.
func (m *Monitor) Start() {
	go m.loop()
}

// Stop waits for all events to be processed and returns.
func (m *Monitor) Stop() {
	close(m.events)
	<-m.done
}

// loop processes events until the channel is closed.
func (m *Monitor) loop() {
	for evt := range m.events {
		m.processEvent(evt)
	}
	close(m.done)
}

func (m *Monitor) processEvent(evt Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ts, ok := m.tables[evt.Table]
	if !ok {
		ts = &TableStats{Name: evt.Table, StartTime: time.Now()}
		m.tables[evt.Table] = ts
		m.order = append(m.order, evt.Table)
	}

	switch evt.Type {
	case EventStart:
		ts.StartTime = time.Now()
	case EventEnd:
		ts.EndTime = time.Now()
		if evt.Secs > 0 {
			ts.extraElapsed = evt.Secs
		}
	case EventRead:
		ts.RowsRead += evt.Count
	case EventWrite:
		ts.RowsWritten += evt.Count
		ts.Bytes += evt.Bytes
	case EventError:
		ts.Errors++
	case EventPrepare:
		ts.IsPhase = true
	}

	if m.verbose {
		fmt.Fprintf(m.output, "  %s: %s\n", evt.Table, eventSummary(evt))
	}
}

// GetTableStats returns a copy of the stats for the given table.
func (m *Monitor) GetTableStats(name string) *TableStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ts, ok := m.tables[name]; ok {
		cp := *ts
		return &cp
	}
	return nil
}

// WriteSummary writes a summary report matching native pgloader format.
//
//	             table name     errors       rows      bytes      total time
//	-----------------------  ---------  ---------  ---------  --------------
//	  "public"."test_users"          2         20     1.0 kB          0.040s
//	-----------------------  ---------  ---------  ---------  --------------
//	      Total import time          2         20     1.0 kB          0.093s
func (m *Monitor) WriteSummary() {
	m.mu.Lock()
	defer m.mu.Unlock()

	const (
		dataCol = 9
		timeCol = 14
	)

	// First pass: find max name width across all entries
	type entry struct {
		name    string
		isTable bool
		ts      *TableStats
	}
	var entries []entry
	totalWritten := int64(0)
	totalErrors := int64(0)
	totalBytes := int64(0)
	var totalTime float64

	phaseNameWidth := 10 // "table name"
	tableNameWidth := 0

	for _, name := range m.order {
		ts := m.tables[name]
		if ts.Name == "all tables" {
			totalTime = ts.Elapsed()
			continue
		}
		isTable := strings.Contains(ts.Name, ".")
		var displayName string
		if isTable {
			displayName = quotedTableName(ts.Name)
			if len(displayName) > tableNameWidth {
				tableNameWidth = len(displayName)
			}
		} else {
			displayName = ts.Name
			if len(displayName) > phaseNameWidth {
				phaseNameWidth = len(displayName)
			}
		}
		entries = append(entries, entry{displayName, isTable, ts})
	}

	// Compute name column width: accommodate both phase and table names
	// Tables have a 2-space indent, so column must be wider for them.
	nameCol := phaseNameWidth
	tableIndent := 2
	if tableNameWidth+tableIndent > nameCol {
		nameCol = tableNameWidth + tableIndent
	}
	if nameCol < 23 {
		nameCol = 23
	}

	// Build format strings with dynamic width
	phaseFmt := fmt.Sprintf("%%%dd  %%%dd  %%%ds  %%%ds", dataCol, dataCol, dataCol, timeCol)
	tableFmt := fmt.Sprintf("  %%%ds  %%%dd  %%%dd  %%%ds  %%%ds", nameCol-tableIndent, dataCol, dataCol, dataCol, timeCol)
	totalFmt := fmt.Sprintf("%%%ds  %%%dd  %%%dd  %%%ds  %%%ds", nameCol, dataCol, dataCol, dataCol, timeCol)

	sep := strings.Repeat("-", nameCol) + "  " +
		strings.Repeat("-", dataCol) + "  " +
		strings.Repeat("-", dataCol) + "  " +
		strings.Repeat("-", dataCol) + "  " +
		strings.Repeat("-", timeCol)

	// Print separator line format for name column
	nameSepFmt := fmt.Sprintf("%%%ds", nameCol)

	fmt.Fprintln(m.output)
	fmt.Fprintf(m.output, nameSepFmt+"  %9s  %9s  %9s  %14s\n",
		"table name", "errors", "rows", "bytes", "time")
	fmt.Fprintln(m.output, sep)

	// Second pass: print entries with separators between groups
	prevIsTable := false
	first := true

	for _, e := range entries {
		// Insert separator when switching between phase/table groups
		if !first && e.isTable != prevIsTable {
			fmt.Fprintln(m.output, sep)
		}
		first = false

		if e.isTable {
			elapsed := e.ts.Elapsed()
			fmt.Fprintf(m.output, tableFmt+"\n",
				e.name, e.ts.Errors, e.ts.RowsWritten,
				formatBytes(e.ts.Bytes), formatTime(elapsed))
			totalWritten += e.ts.RowsWritten
			totalErrors += e.ts.Errors
			totalBytes += e.ts.Bytes
		} else {
			elapsed := e.ts.Elapsed()
			fmt.Fprintf(m.output, nameSepFmt+"  "+phaseFmt+"\n",
				e.name, e.ts.Errors, int64(0), "", formatTime(elapsed))
		}
		prevIsTable = e.isTable
	}

	fmt.Fprintln(m.output, sep)
	fmt.Fprintf(m.output, totalFmt+"\n",
		"Total import time", totalErrors, totalWritten,
		formatBytes(totalBytes), formatTime(totalTime))
}

// quotedTableName formats "schema.table" as "schema"."table".
// Names without dots are returned as-is (e.g. phase names like "fetch").
func quotedTableName(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return `"` + name[:i] + `"."` + name[i+1:] + `"`
	}
	return name
}

// formatBytes returns a human-readable byte size, e.g. "1.0 kB".
func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f kB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// formatTime returns a duration string like "0.040s" with 3 decimal places.
func formatTime(secs float64) string {
	return fmt.Sprintf("%.3fs", secs)
}

func eventSummary(evt Event) string {
	switch evt.Type {
	case EventStart:
		return "started"
	case EventEnd:
		return fmt.Sprintf("done in %.1fs", evt.Secs)
	case EventRead:
		return fmt.Sprintf("read %d rows", evt.Count)
	case EventWrite:
		return fmt.Sprintf("wrote %d rows (%.0f bytes)", evt.Count, float64(evt.Bytes))
	case EventError:
		return fmt.Sprintf("error: %v", evt.Err)
	case EventPrepare:
		return "prepared schema"
	case EventFinalize:
		return "finalized schema"
	default:
		return ""
	}
}

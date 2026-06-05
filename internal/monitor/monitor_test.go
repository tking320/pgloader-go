package monitor

import (
	"bytes"
	"strings"
	"testing"
)

func TestVerboseWriteThrottle(t *testing.T) {
	var buf bytes.Buffer
	m := NewMonitor(&buf, true)
	m.Start()

	for i := 0; i < 15; i++ {
		m.Events() <- Event{Type: EventWrite, Table: "test.t", Count: 100, Bytes: 1000}
	}
	m.Stop()

	lines := strings.Count(buf.String(), "\n")
	if lines != 1 {
		t.Errorf("expected 1 verbose line for 15 EventWrites (only every 10th), got %d", lines)
	}
}

func TestVerboseWriteThrottleStatsAccumulated(t *testing.T) {
	var buf bytes.Buffer
	m := NewMonitor(&buf, false)
	m.Start()

	for i := 0; i < 15; i++ {
		m.Events() <- Event{Type: EventWrite, Table: "test.t", Count: 100, Bytes: 1000}
	}
	m.Stop()

	ts := m.GetTableStats("test.t")
	if ts == nil {
		t.Fatal("expected table stats")
	}
	if ts.RowsWritten != 1500 {
		t.Errorf("expected 1500 rows, got %d", ts.RowsWritten)
	}
	if ts.Bytes != 15000 {
		t.Errorf("expected 15000 bytes, got %d", ts.Bytes)
	}
}

func TestVerboseNonWriteNotThrottled(t *testing.T) {
	var buf bytes.Buffer
	m := NewMonitor(&buf, true)
	m.Start()

	m.Events() <- Event{Type: EventStart, Table: "phase1"}
	m.Events() <- Event{Type: EventEnd, Table: "phase1", Secs: 1.0}
	m.Events() <- Event{Type: EventError, Table: "test.t", Err: nil}
	m.Stop()

	lines := strings.Count(buf.String(), "\n")
	if lines != 3 {
		t.Errorf("expected 3 verbose lines for non-write events, got %d", lines)
	}
}

func TestVerboseWriteDifferentTables(t *testing.T) {
	var buf bytes.Buffer
	m := NewMonitor(&buf, true)
	m.Start()

	// 5 writes per table — each table's counter is independent
	for i := 0; i < 5; i++ {
		m.Events() <- Event{Type: EventWrite, Table: "t1", Count: 10, Bytes: 100}
		m.Events() <- Event{Type: EventWrite, Table: "t2", Count: 10, Bytes: 100}
	}
	m.Stop()

	// With 5 writes each, neither table reaches count 10, so 0 verbose lines
	lines := strings.Count(buf.String(), "\n")
	if lines != 0 {
		t.Errorf("expected 0 verbose lines (5 writes < 10 per table), got %d", lines)
	}
}

func TestVerboseWriteCountsArePerTable(t *testing.T) {
	var buf bytes.Buffer
	m := NewMonitor(&buf, true)
	m.Start()

	// 15 writes to t1, 5 to t2 — only t1 reaches the 10th write
	for i := 0; i < 15; i++ {
		m.Events() <- Event{Type: EventWrite, Table: "t1", Count: 100, Bytes: 1000}
	}
	for i := 0; i < 5; i++ {
		m.Events() <- Event{Type: EventWrite, Table: "t2", Count: 100, Bytes: 1000}
	}
	m.Stop()

	lines := strings.Count(buf.String(), "\n")
	if lines != 1 {
		t.Errorf("expected 1 verbose line (t1 at count 10), got %d", lines)
	}
	if !strings.Contains(buf.String(), "t1") {
		t.Errorf("expected output to contain t1, got: %q", buf.String())
	}
	if strings.Contains(buf.String(), "t2") {
		t.Errorf("expected no t2 output (only 5 writes), got: %q", buf.String())
	}
}

func TestNonVerboseNoOutput(t *testing.T) {
	var buf bytes.Buffer
	m := NewMonitor(&buf, false) // verbose=false
	m.Start()

	m.Events() <- Event{Type: EventStart, Table: "test"}
	m.Events() <- Event{Type: EventWrite, Table: "test.t", Count: 100, Bytes: 1000}
	m.Stop()

	if buf.Len() != 0 {
		t.Errorf("expected no output in non-verbose mode, got %q", buf.String())
	}
}

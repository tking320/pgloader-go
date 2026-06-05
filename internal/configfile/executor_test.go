package configfile

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// DebugTracer 测试
// ---------------------------------------------------------------------------

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestDebugTracer_TraceQueryStart(t *testing.T) {
	var d DebugTracer
	out := captureStderr(t, func() {
		ctx := d.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
		if ctx == nil {
			t.Error("expected non-nil context")
		}
	})
	if !strings.Contains(out, "SELECT 1") {
		t.Errorf("expected SQL in output, got %q", out)
	}
}

func TestDebugTracer_TraceQueryEndNoError(t *testing.T) {
	var d DebugTracer
	out := captureStderr(t, func() {
		d.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})
	})
	if out != "" {
		t.Errorf("expected no output on success, got %q", out)
	}
}

func TestDebugTracer_TraceQueryEndWithError(t *testing.T) {
	var d DebugTracer
	out := captureStderr(t, func() {
		d.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: assertError{}})
	})
	if !strings.Contains(out, "DEBUG SQL ERROR") {
		t.Errorf("expected error output, got %q", out)
	}
}

func TestDebugTracer_TraceCopyFromStart(t *testing.T) {
	var d DebugTracer
	out := captureStderr(t, func() {
		ctx := d.TraceCopyFromStart(context.Background(), nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"test_table"}})
		if ctx == nil {
			t.Error("expected non-nil context")
		}
	})
	if !strings.Contains(out, "test_table") {
		t.Errorf("expected table name in output, got %q", out)
	}
}

func TestDebugTracer_TraceBatchQuery(t *testing.T) {
	var d DebugTracer
	out := captureStderr(t, func() {
		d.TraceBatchQuery(context.Background(), nil, pgx.TraceBatchQueryData{SQL: "INSERT INTO t (x) VALUES ($1)", Err: nil})
	})
	if !strings.Contains(out, "INSERT INTO t") {
		t.Errorf("expected SQL in output, got %q", out)
	}
}

func TestDebugTracer_TracePrepareStart(t *testing.T) {
	var d DebugTracer
	out := captureStderr(t, func() {
		ctx := d.TracePrepareStart(context.Background(), nil, pgx.TracePrepareStartData{Name: "foo", SQL: "SELECT 1"})
		if ctx == nil {
			t.Error("expected non-nil context")
		}
	})
	if !strings.Contains(out, "foo") || !strings.Contains(out, "SELECT") {
		t.Errorf("expected name and SQL in output, got %q", out)
	}
}

func TestDebugTracer_TraceConnectStart(t *testing.T) {
	var d DebugTracer
	// TraceConnectStart uses ConnConfig.ConnString(), so we need to create a config
	cfg, err := pgx.ParseConfig("postgres://localhost:5432/testdb")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	out := captureStderr(t, func() {
		ctx := d.TraceConnectStart(context.Background(), pgx.TraceConnectStartData{ConnConfig: cfg})
		if ctx == nil {
			t.Error("expected non-nil context")
		}
	})
	if !strings.Contains(out, "localhost") {
		t.Errorf("expected conn string in output, got %q", out)
	}
}

func TestDebugTracer_TraceConnectEndNoError(t *testing.T) {
	var d DebugTracer
	out := captureStderr(t, func() {
		d.TraceConnectEnd(context.Background(), pgx.TraceConnectEndData{})
	})
	if out != "" {
		t.Errorf("expected no output on success, got %q", out)
	}
}

// assertError is a simple error for testing error-printing behavior.
type assertError struct{}

func (assertError) Error() string { return "test error" }

// ---------------------------------------------------------------------------
// extractQuotedChar 测试
// ---------------------------------------------------------------------------

func TestExtractQuotedChar_逗号分隔符(t *testing.T) {
	got := extractQuotedChar("fields terminated by ','")
	if got != ',' {
		t.Errorf("期望 ','，得到 %q", got)
	}
}

func TestExtractQuotedChar_制表符分隔符(t *testing.T) {
	got := extractQuotedChar("fields terminated by '\\t'")
	if got != '\t' {
		t.Errorf("期望 '\\t'，得到 %q", got)
	}
}

func TestExtractQuotedChar_换行符分隔符(t *testing.T) {
	got := extractQuotedChar("fields terminated by '\\n'")
	if got != '\n' {
		t.Errorf("期望 '\\n'，得到 %q", got)
	}
}

func TestExtractQuotedChar_管道符分隔符(t *testing.T) {
	got := extractQuotedChar("fields terminated by '|'")
	if got != '|' {
		t.Errorf("期望 '|'，得到 %q", got)
	}
}

func TestExtractQuotedChar_无引号返回默认逗号(t *testing.T) {
	got := extractQuotedChar("fields terminated by")
	if got != ',' {
		t.Errorf("期望默认 ','，得到 %q", got)
	}
}

func TestExtractQuotedChar_空引号返回默认逗号(t *testing.T) {
	got := extractQuotedChar("fields terminated by ''")
	if got != ',' {
		t.Errorf("期望默认 ','，得到 %q", got)
	}
}

// ---------------------------------------------------------------------------
// extractSkipCount 测试
// ---------------------------------------------------------------------------

func TestExtractSkipHeader_标准跳过头部(t *testing.T) {
	got := extractSkipCount("skip header = 1")
	if got != 1 {
		t.Errorf("期望 1，得到 %d", got)
	}
}

func TestExtractSkipHeader_跳过五行(t *testing.T) {
	got := extractSkipCount("skip header = 5")
	if got != 5 {
		t.Errorf("期望 5，得到 %d", got)
	}
}

func TestExtractSkipHeader_无等号默认返回1(t *testing.T) {
	got := extractSkipCount("skip header")
	if got != 1 {
		t.Errorf("期望默认 1，得到 %d", got)
	}
}

func TestExtractSkipHeader_非法数字默认返回1(t *testing.T) {
	got := extractSkipCount("skip header = abc")
	if got != 1 {
		t.Errorf("期望默认 1，得到 %d", got)
	}
}

// ---------------------------------------------------------------------------
// parseCSVWithOptions 测试
// ---------------------------------------------------------------------------

func TestParseCSVWithOptions_默认选项(t *testing.T) {
	delim, header, skip := parseCSVWithOptions(nil)
	if delim != ',' {
		t.Errorf("期望分隔符 ','，得到 %q", delim)
	}
	if header {
		t.Error("期望 header=false，得到 true")
	}
	if skip != 0 {
		t.Errorf("期望 skip=0，得到 %d", skip)
	}
}

func TestParseCSVWithOptions_制表符和跳过头部(t *testing.T) {
	opts := []string{
		"fields terminated by '\\t'",
		"skip header = 1",
		"header",
	}
	delim, header, skip := parseCSVWithOptions(opts)
	if delim != '\t' {
		t.Errorf("期望分隔符 '\\t'，得到 %q", delim)
	}
	if !header {
		t.Error("期望 header=true，得到 false")
	}
	if skip != 1 {
		t.Errorf("期望 skip=1，得到 %d", skip)
	}
}

func TestParseCSVWithOptions_分号分隔符跳过五行无头部(t *testing.T) {
	opts := []string{
		"fields terminated by ';'",
		"skip header = 5",
	}
	delim, header, skip := parseCSVWithOptions(opts)
	if delim != ';' {
		t.Errorf("期望分隔符 ';'，得到 %q", delim)
	}
	if header {
		t.Error("期望 header=false，得到 true")
	}
	if skip != 5 {
		t.Errorf("期望 skip=5，得到 %d", skip)
	}
}

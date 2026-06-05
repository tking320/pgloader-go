package mysql

import (
	"testing"
)

// ---------------------------------------------------------------------------
// compileTablePattern tests
// ---------------------------------------------------------------------------

func TestCompileTablePattern_Regex(t *testing.T) {
	re, err := compileTablePattern("~/actor/")
	if err != nil {
		t.Fatalf("compileTablePattern: %v", err)
	}
	if !re.MatchString("actor") {
		t.Error("expected 'actor' to match ~/actor/")
	}
	if !re.MatchString("actor_history") {
		t.Error("expected 'actor_history' to match ~/actor/ (substring)")
	}
}

func TestCompileTablePattern_RegexAnchored(t *testing.T) {
	// Use explicit ^ $ in regex for full match
	re, err := compileTablePattern("~/^actor$/")
	if err != nil {
		t.Fatalf("compileTablePattern: %v", err)
	}
	if !re.MatchString("actor") {
		t.Error("expected 'actor' to match ~/^actor$/")
	}
	if re.MatchString("actor_history") {
		t.Error("expected 'actor_history' to NOT match ~/^actor$/")
	}
}

func TestCompileTablePattern_RegexCaseInsensitive(t *testing.T) {
	re, err := compileTablePattern("~/ACTOR/")
	if err != nil {
		t.Fatalf("compileTablePattern: %v", err)
	}
	if !re.MatchString("Actor") {
		t.Error("expected case-insensitive regex match")
	}
}

func TestCompileTablePattern_QuotedExact(t *testing.T) {
	re, err := compileTablePattern("'customer'")
	if err != nil {
		t.Fatalf("compileTablePattern: %v", err)
	}
	if !re.MatchString("customer") {
		t.Error("expected 'customer' to match 'customer'")
	}
	if re.MatchString("customer_addr") {
		t.Error("expected 'customer_addr' to NOT match 'customer'")
	}
}

func TestCompileTablePattern_QuotedCaseInsensitive(t *testing.T) {
	re, err := compileTablePattern("'CUSTOMER'")
	if err != nil {
		t.Fatalf("compileTablePattern: %v", err)
	}
	if !re.MatchString("Customer") {
		t.Error("expected case-insensitive quoted match")
	}
}

func TestCompileTablePattern_PlainText(t *testing.T) {
	re, err := compileTablePattern("payment")
	if err != nil {
		t.Fatalf("compileTablePattern: %v", err)
	}
	if !re.MatchString("payment") {
		t.Error("expected 'payment' to match 'payment'")
	}
	if re.MatchString("payment_method") {
		t.Error("expected 'payment_method' to NOT match 'payment'")
	}
}

func TestCompileTablePattern_LikeWildcard(t *testing.T) {
	re, err := compileTablePattern("'test_%'")
	if err != nil {
		t.Fatalf("compileTablePattern: %v", err)
	}
	if !re.MatchString("test_1") {
		t.Error("expected 'test_1' to match 'test_%'")
	}
	if !re.MatchString("test_abc") {
		t.Error("expected 'test_abc' to match 'test_%'")
	}
	if re.MatchString("not_match") {
		t.Error("expected 'not_match' to NOT match 'test_%'")
	}
}

func TestCompileTablePattern_InvalidRegex(t *testing.T) {
	_, err := compileTablePattern("~/[invalid/") // unterminated bracket
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

// ---------------------------------------------------------------------------
// tableMatches tests
// ---------------------------------------------------------------------------

func TestTableMatches_EmptyPatterns(t *testing.T) {
	match, err := tableMatches("any_table", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !match {
		t.Error("expected nil patterns to match all")
	}
	match, err = tableMatches("any_table", []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !match {
		t.Error("expected empty patterns to match all")
	}
}

func TestTableMatches_MultiplePatterns(t *testing.T) {
	patterns := []string{"~/film/", "'customer'", "payment"}
	for _, name := range []string{"film", "customer", "payment"} {
		match, err := tableMatches(name, patterns)
		if err != nil {
			t.Fatalf("tableMatches(%q): %v", name, err)
		}
		if !match {
			t.Errorf("expected %q to match one of the patterns", name)
		}
	}
}

func TestTableMatches_NoMatch(t *testing.T) {
	patterns := []string{"~/actor/", "'customer'"}
	match, err := tableMatches("payment", patterns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if match {
		t.Error("expected 'payment' to not match any pattern")
	}
}

func TestTableMatches_AllEmptyPatternsMatch(t *testing.T) {
	match, err := tableMatches("anything", []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !match {
		t.Error("empty patterns should match all")
	}
}

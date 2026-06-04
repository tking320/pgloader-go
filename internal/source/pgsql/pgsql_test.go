package pgsql

import (
	"testing"

	"github.com/tking320/pgloader-go/internal/catalog"
)

func TestParseBaseType(t *testing.T) {
	tests := []struct {
		fullType string
		want     string
	}{
		{"integer", "integer"},
		{"character varying(255)", "character varying"},
		{"numeric(18,6)", "numeric"},
		{"timestamp with time zone", "timestamp with time zone"},
		{"double precision", "double precision"},
		{"money", "money"},
		{"character(1)", "character"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.fullType, func(t *testing.T) {
			if got := parseBaseType(tt.fullType); got != tt.want {
				t.Errorf("parseBaseType(%q) = %q, want %q", tt.fullType, got, tt.want)
			}
		})
	}
}

func TestParseTriggerDef(t *testing.T) {
	tests := []struct {
		def        string
		wantTiming string
		wantEvents string
	}{
		{
			"CREATE TRIGGER check_update BEFORE UPDATE ON t FOR EACH ROW EXECUTE FUNCTION f()",
			"BEFORE",
			"UPDATE",
		},
		{
			"CREATE TRIGGER audit AFTER INSERT OR DELETE OR UPDATE ON t FOR EACH ROW EXECUTE FUNCTION audit()",
			"AFTER",
			"INSERT, DELETE, UPDATE",
		},
		{
			"CREATE TRIGGER instead_trig INSTEAD OF INSERT ON v FOR EACH ROW EXECUTE FUNCTION f()",
			"INSTEAD OF",
			"INSERT",
		},
		{
			"CREATE TRIGGER no_match ON t FOR EACH ROW EXECUTE FUNCTION f()",
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			gotTiming, gotEvents := parseTriggerDef(tt.def)
			if gotTiming != tt.wantTiming {
				t.Errorf("parseTriggerDef(%q) timing = %q, want %q", tt.def, gotTiming, tt.wantTiming)
			}
			if gotEvents != tt.wantEvents {
				t.Errorf("parseTriggerDef(%q) events = %q, want %q", tt.def, gotEvents, tt.wantEvents)
			}
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"it's", "'it''s'"},
		{"", "''"},
		{"a'b'c", "'a''b''c'"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := quoteLiteral(tt.input); got != tt.want {
				t.Errorf("quoteLiteral(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTypeCreateSQL(t *testing.T) {
	// Enum
	enumType := &catalog.SQLType{
		Name:     "mood",
		Schema:   "public",
		Type:     "enum",
		Elements: []string{"happy", "sad", "neutral"},
	}
	wantEnum := `CREATE TYPE "public"."mood" AS ENUM ('happy', 'sad', 'neutral')`
	if got := typeCreateSQL(enumType); got != wantEnum {
		t.Errorf("typeCreateSQL(enum) = %q, want %q", got, wantEnum)
	}

	// Domain
	domainType := &catalog.SQLType{
		Name:     "positive_int",
		Schema:   "public",
		Type:     "domain",
		BaseType: "integer",
	}
	wantDomain := `CREATE DOMAIN "public"."positive_int" AS integer`
	if got := typeCreateSQL(domainType); got != wantDomain {
		t.Errorf("typeCreateSQL(domain) = %q, want %q", got, wantDomain)
	}

	// Composite
	compositeType := &catalog.SQLType{
		Name:   "address",
		Schema: "public",
		Type:   "composite",
		AttrDefs: []catalog.CompositeAttr{
			{Name: "street", TypeName: "text"},
			{Name: "city", TypeName: "text"},
			{Name: "zip", TypeName: "integer"},
		},
	}
	wantComposite := `CREATE TYPE "public"."address" AS ("street" text, "city" text, "zip" integer)`
	if got := typeCreateSQL(compositeType); got != wantComposite {
		t.Errorf("typeCreateSQL(composite) = %q, want %q", got, wantComposite)
	}

	// Unknown type
	unknownType := &catalog.SQLType{
		Name: "unknown",
		Type: "unknown",
	}
	if got := typeCreateSQL(unknownType); got != "" {
		t.Errorf("typeCreateSQL(unknown) = %q, want ''", got)
	}
}

func TestEnumCreateSQL(t *testing.T) {
	typ := &catalog.SQLType{
		Name:     "color",
		Schema:   "public",
		Elements: []string{"red", "green", "blue"},
	}
	want := `CREATE TYPE "public"."color" AS ENUM ('red', 'green', 'blue')`
	if got := enumCreateSQL(typ); got != want {
		t.Errorf("enumCreateSQL() = %q, want %q", got, want)
	}
}

func TestAddPartitionClause(t *testing.T) {
	baseSQL := "CREATE TABLE public.measurements (\n    \"id\" integer,\n    \"logdate\" date\n)"
	pi := &catalog.PartitionInfo{
		Strategy:   "RANGE",
		KeyColumns: []string{"logdate"},
	}
	want := "CREATE TABLE public.measurements (\n    \"id\" integer,\n    \"logdate\" date\n) PARTITION BY RANGE (\"logdate\")"
	if got := addPartitionClause(baseSQL, pi); got != want {
		t.Errorf("addPartitionClause() = %q, want %q", got, want)
	}

	// Hash partition with multiple keys
	pi2 := &catalog.PartitionInfo{
		Strategy:   "HASH",
		KeyColumns: []string{"id", "logdate"},
	}
	want2 := "CREATE TABLE public.measurements (\n    \"id\" integer,\n    \"logdate\" date\n) PARTITION BY HASH (\"id\", \"logdate\")"
	if got := addPartitionClause(baseSQL, pi2); got != want2 {
		t.Errorf("addPartitionClause(hash) = %q, want %q", got, want2)
	}
}

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", `"simple"`},
		{"has space", `"has space"`},
		{"quo\"te", `"quo""te"`},
		{"", `""`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := quoteIdent(tt.input); got != tt.want {
				t.Errorf("quoteIdent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Package catalog defines the data model for database schema introspection
// and DDL generation. It corresponds to pgloader's src/utils/catalog.lisp.
package catalog

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Catalog data model
// ---------------------------------------------------------------------------

// Catalog represents a full database catalog containing schemas.
type Catalog struct {
	Schemas []*Schema
}

// Schema represents a database schema (namespace).
type Schema struct {
	Name       string
	Catalog    *Catalog
	Tables     []*Table
	Views      []*View
	Types      []*SQLType
	Extensions []*Extension
	Functions  []*Function
}

// PartitionInfo describes table partitioning for PostgreSQL source.
type PartitionInfo struct {
	Strategy       string           // RANGE, LIST, HASH
	KeyColumns     []string         // partition key column names
	KeyExpressions []string         // partition key expressions (for expression-based)
	Partitions     []PartitionChild // child partition definitions (populated for parents)
}

// PartitionChild describes a single partition child table.
type PartitionChild struct {
	Name  string // partition table name
	Bound string // partition bound (e.g., "FOR VALUES WITH (modulus 4, remainder 0)")
}

// Table represents a database table.
type Table struct {
	Name             string
	SourceName       string
	Schema           *Schema
	OID              uint32
	Comment          string
	StorageParams    map[string]string
	RowCountEstimate int64
	ParentTable      string         // parent table name for inheritance
	PartitionInfo    *PartitionInfo // nil for non-partitioned tables
	Columns          []*Column
	Indexes          []*Index
	ForeignKeys      []*ForeignKey
	Triggers         []*Trigger
}

// Column represents a table column.
type Column struct {
	Name         string
	TypeName     string
	TypeMod      string
	Nullable     bool
	Default      string
	Comment      string
	Extra        string // source-specific metadata (e.g., MySQL "auto_increment")
	ExtraDDL     string // DDL fragment appended to CREATE TABLE (e.g., "GENERATED ALWAYS AS IDENTITY")
	Transform    string // transform function name
	SourceType   string // original source type (e.g., MySQL "tinyint(1) unsigned")
	IsPK         bool   // true if part of primary key
	IsAutoInc    bool   // true if auto-incrementing
	SequenceName string // PostgreSQL sequence name (for serial/bigserial)
}

// Index represents a table index.
type Index struct {
	Name    string
	Type    string // btree, hash, gist, etc.
	Schema  string
	Table   string
	Primary bool
	Unique  bool
	Columns []string
	SQL     string
	Filter  string
}

// ForeignKey represents a foreign key constraint.
type ForeignKey struct {
	Name              string
	TableName         string
	Columns           []string
	ForeignTable      string
	ForeignColumns    []string
	UpdateRule        string
	DeleteRule        string
	MatchRule         string
	Deferrable        bool
	InitiallyDeferred bool
}

// Trigger represents a table trigger.
type Trigger struct {
	Name      string
	Table     string
	Action    string
	Procedure string
	Timing    string // BEFORE, AFTER, INSTEAD OF
	Events    string // INSERT, UPDATE, DELETE
	ForEach   string // ROW, STATEMENT
}

// View represents a database view.
type View struct {
	Name       string
	SourceName string
	Schema     *Schema
	Definition string
}

// CompositeAttr describes an attribute of a composite type.
type CompositeAttr struct {
	Name      string
	TypeName  string
	Collation string
}

// Function represents a user-defined function.
type Function struct {
	Name       string
	Schema     string
	Definition string // full CREATE OR REPLACE FUNCTION definition
}

// SQLType represents a custom SQL type (DOMAIN, ENUM, etc.).
type SQLType struct {
	Name        string
	Schema      string
	Type        string // "enum", "domain", "composite", "range"
	SourceDef   string
	Extra       string
	Extension   string
	Elements    []string        // for ENUM: the label values
	BaseType    string          // for DOMAIN: the underlying type
	BaseTypeMod string          // for DOMAIN: typemod of underlying type
	AttrDefs    []CompositeAttr // for composite types
}

// Extension represents a PostgreSQL extension.
type Extension struct {
	Name   string
	Schema string
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// QualifiedName returns the fully qualified name "schema.table".
func (t *Table) QualifiedName() string {
	if t.Schema != nil && t.Schema.Name != "" {
		return fmt.Sprintf("%s.%s", QuoteIdent(t.Schema.Name), QuoteIdent(t.Name))
	}
	return QuoteIdent(t.Name)
}

// ColumnNames returns the list of column names.
func (t *Table) ColumnNames() []string {
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	return names
}

// ---------------------------------------------------------------------------
// DDL generation
// ---------------------------------------------------------------------------

// CreateTableSQL generates a CREATE TABLE statement.
func (t *Table) CreateTableSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", t.QualifiedName())
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		colSQL := fmt.Sprintf("    %s %s", QuoteIdent(c.Name), c.TypeName)
		if c.TypeMod != "" {
			colSQL += c.TypeMod
		}
		if !c.Nullable {
			colSQL += " NOT NULL"
		}
		if c.ExtraDDL != "" {
			colSQL += " " + c.ExtraDDL
		} else if c.Default != "" {
			colSQL += " DEFAULT " + c.Default
		}
		cols[i] = colSQL
	}
	b.WriteString(strings.Join(cols, ",\n"))
	b.WriteString("\n)")
	return b.String()
}

// DropTableSQL generates a DROP TABLE statement.
func (t *Table) DropTableSQL() string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE;", t.QualifiedName())
}

// TableCommentSQL generates a COMMENT ON TABLE statement.
// Returns empty string if no comment is set.
func (t *Table) TableCommentSQL() string {
	if t.Comment == "" {
		return ""
	}
	return fmt.Sprintf("COMMENT ON TABLE %s IS '%s';", t.QualifiedName(), escapeComment(t.Comment))
}

// ColumnCommentSQL generates a COMMENT ON COLUMN statement.
// Returns empty string if no comment is set.
func (c *Column) ColumnCommentSQL(table *Table) string {
	if c.Comment == "" {
		return ""
	}
	return fmt.Sprintf("COMMENT ON COLUMN %s.%s IS '%s';",
		table.QualifiedName(), QuoteIdent(c.Name), escapeComment(c.Comment))
}

// CreateIndexSQL generates a CREATE INDEX statement.
func (i *Index) CreateIndexSQL() string {
	if i.SQL != "" {
		return i.SQL
	}
	tableName := i.qualifiedTableName()
	if i.Primary {
		return fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s);",
			tableName,
			strings.Join(QuoteIdents(i.Columns), ", "))
	}
	unique := ""
	indexName := i.Name
	if indexName == "" {
		indexName = fmt.Sprintf("%s_%s_idx", i.Table, strings.Join(i.Columns, "_"))
	}
	if i.Unique {
		unique = " UNIQUE"
	}

	using, opclass := pgIndexOptions(i.Type)

	return fmt.Sprintf("CREATE%s INDEX IF NOT EXISTS %s ON %s%s (%s%s);",
		unique, QuoteIdent(indexName), tableName, using,
		strings.Join(QuoteIdents(i.Columns), ", "), opclass)
}

// qualifiedTableName returns the schema-qualified table name if schema is set.
func (i *Index) qualifiedTableName() string {
	if i.Schema != "" {
		return fmt.Sprintf("%s.%s", QuoteIdent(i.Schema), QuoteIdent(i.Table))
	}
	return QuoteIdent(i.Table)
}

// pgIndexOptions returns the USING clause and operator class for a source index type.
func pgIndexOptions(sourceType string) (using string, opclass string) {
	switch strings.ToLower(sourceType) {
	case "fulltext":
		return " USING gin", " gin_trgm_ops"
	case "hash":
		return " USING hash", ""
	case "gist":
		return " USING gist", ""
	case "gin":
		return " USING gin", ""
	case "spatial":
		return " USING gist", ""
	case "rtree":
		return " USING gist", ""
	default:
		return "", "" // default is btree
	}
}

// DropIndexSQL generates a DROP INDEX statement.
func (i *Index) DropIndexSQL() string {
	return fmt.Sprintf("DROP INDEX IF EXISTS %s;", QuoteIdent(i.Name))
}

// CreateFKeySQL generates an ALTER TABLE ADD FOREIGN KEY statement.
func (fk *ForeignKey) CreateFKeySQL() string {
	return fmt.Sprintf(
		"ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)%s%s%s;",
		QuoteIdent(fk.TableName),
		QuoteIdent(fk.Name),
		strings.Join(QuoteIdents(fk.Columns), ", "),
		quoteQualifiedIdent(fk.ForeignTable),
		strings.Join(QuoteIdents(fk.ForeignColumns), ", "),
		fkeyMatch(fk.MatchRule),
		fkeyAction("ON DELETE", fk.DeleteRule),
		fkeyAction("ON UPDATE", fk.UpdateRule),
	)
}

// ---------------------------------------------------------------------------
// Identifier quoting
// ---------------------------------------------------------------------------

func QuoteIdent(name string) string {
	if name == "" {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteQualifiedIdent quotes a possibly schema-qualified identifier.
// "public.orders" → "public"."orders"
func quoteQualifiedIdent(name string) string {
	if !strings.Contains(name, ".") {
		return QuoteIdent(name)
	}
	parts := strings.SplitN(name, ".", 2)
	return QuoteIdent(parts[0]) + "." + QuoteIdent(parts[1])
}

func QuoteIdents(names []string) []string {
	result := make([]string, len(names))
	for i, n := range names {
		result[i] = QuoteIdent(n)
	}
	return result
}

func fkeyAction(keyword, rule string) string {
	if rule == "" {
		return ""
	}
	return fmt.Sprintf(" %s %s", keyword, rule)
}

func fkeyMatch(rule string) string {
	if rule == "" {
		return ""
	}
	return fmt.Sprintf(" MATCH %s", rule)
}

// escapeComment escapes single quotes in string literals for COMMENT ON SQL.
func escapeComment(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

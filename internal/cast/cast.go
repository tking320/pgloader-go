// Package cast implements a type conversion rule engine for mapping source
// database types (MySQL, SQLite, etc.) to PostgreSQL types.
package cast

import (
	"fmt"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Rule matching
// ---------------------------------------------------------------------------

// MatchRule defines the conditions for a cast rule to apply.
type MatchRule struct {
	// SourceType is the MySQL type name to match (e.g., "tinyint", "varchar").
	// Empty matches any type.
	SourceType string

	// TypeMod is a typemod pattern. If non-empty, the column's full type
	// string (e.g., "tinyint(1)") must contain this substring. If empty,
	// no typemod restriction.
	TypeMod string

	// Unsigned restricts rule to unsigned types when true, signed when false,
	// or ignores when Zero is true (the zero value).
	Unsigned TriState

	// AutoIncrement restricts rule to auto_increment columns when true.
	AutoIncrement TriState

	// OnUpdateCurrentTimestamp restricts rule to columns with
	// ON UPDATE CURRENT_TIMESTAMP.
	OnUpdateCurrentTimestamp TriState
}

// TriState is a three-state boolean.
type TriState int8

const (
	Any TriState = 0
	Yes TriState = 1
	No  TriState = -1
)

// matches reports whether the rule matches the given column attributes.
func (m MatchRule) matches(typeName, columnType, extra string) bool {
	// Source type must match
	if m.SourceType != "" && !strings.EqualFold(m.SourceType, typeName) {
		return false
	}

	// TypeMod pattern must be present in column_type
	if m.TypeMod != "" && !strings.Contains(columnType, m.TypeMod) {
		return false
	}

	// Unsigned check
	if m.Unsigned != Any {
		isUnsigned := strings.Contains(columnType, "unsigned")
		if (m.Unsigned == Yes) != isUnsigned {
			return false
		}
	}

	// Auto increment check
	if m.AutoIncrement != Any {
		isAutoInc := strings.Contains(extra, "auto_increment")
		if (m.AutoIncrement == Yes) != isAutoInc {
			return false
		}
	}

	// ON UPDATE CURRENT_TIMESTAMP check
	if m.OnUpdateCurrentTimestamp != Any {
		hasOnUpdate := strings.Contains(extra, "on update CURRENT_TIMESTAMP")
		if (m.OnUpdateCurrentTimestamp == Yes) != hasOnUpdate {
			return false
		}
	}

	return true
}

// ---------------------------------------------------------------------------
// CastRule and Engine
// ---------------------------------------------------------------------------

// CastRule defines a single type conversion rule.
type CastRule struct {
	Match MatchRule

	// TargetType is the PostgreSQL target type (e.g., "boolean", "timestamptz").
	// If it contains "$mod", the MySQL typemod is substituted.
	TargetType string

	// DropTypemod, when true, removes the typemod from the target column.
	DropTypemod bool

	// Transform is the named data transform function to apply (empty = none).
	Transform string
}

// Engine holds a set of cast rules and applies them in priority order.
type Engine struct {
	rules []CastRule
}

// NewEngine creates a cast engine with the given rules.
func NewEngine(rules []CastRule) *Engine {
	return &Engine{rules: rules}
}

// ApplyResult describes the CAST result for a column.
type ApplyResult struct {
	TargetType  string // PostgreSQL target type
	DropTypemod bool   // true if typemod should be dropped
	Transform   string // data transform function name (empty = none)
}

// Apply finds the first matching rule and returns the cast result.
// If no rule matches, it returns a best-guess default.
func (e *Engine) Apply(typeName, columnType, extra string) ApplyResult {
	for _, rule := range e.rules {
		if rule.Match.matches(typeName, columnType, extra) {
			return ApplyResult{
				TargetType:  rule.TargetType,
				DropTypemod: rule.DropTypemod,
				Transform:   rule.Transform,
			}
		}
	}

	// Default: keep the original type name, lowercase
	return ApplyResult{
		TargetType: strings.ToLower(typeName),
	}
}

// ParseTypemod extracts the typemod from a MySQL column_type string.
// "varchar(255)" → "255", "decimal(18,6)" → "18,6"
func ParseTypemod(columnType string) string {
	start := strings.IndexByte(columnType, '(')
	if start == -1 {
		return ""
	}
	end := strings.LastIndexByte(columnType, ')')
	if end == -1 || end <= start {
		return ""
	}
	return columnType[start+1 : end]
}

// BaseTypeName extracts the base type from a MySQL column_type string.
// "tinyint(1) unsigned" → "tinyint", "varchar(255)" → "varchar"
func BaseTypeName(columnType string) string {
	// Strip unsigned/signed
	s := strings.TrimSpace(columnType)
	if i := strings.Index(s, " unsigned"); i > 0 {
		s = s[:i]
	} else if i := strings.Index(s, " signed"); i > 0 {
		s = s[:i]
	}

	// Strip typemod
	if i := strings.IndexByte(s, '('); i > 0 {
		s = s[:i]
	}

	return strings.TrimSpace(s)
}

// TypemodParams parses "255" → (255, 0) and "18,6" → (18, 6).
func TypemodParams(mod string) (int64, int64) {
	if mod == "" {
		return 0, 0
	}
	parts := strings.SplitN(mod, ",", 2)
	p1, _ := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	var p2 int64
	if len(parts) > 1 {
		p2, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	}
	return p1, p2
}

// SubstituteTypemod replaces "$mod" in the target type with the MySQL typemod.
func SubstituteTypemod(targetType, mysqlTypemod string) string {
	if strings.Contains(targetType, "$mod") {
		return strings.ReplaceAll(targetType, "$mod", mysqlTypemod)
	}
	return targetType
}

// ---------------------------------------------------------------------------
// MySQL default cast rules
// ---------------------------------------------------------------------------

// MySQLDefaultRules returns the default MySQL → PostgreSQL cast rules,
// matching pgloader's *mysql-default-cast-rules*.
func MySQLDefaultRules() []CastRule {
	return []CastRule{
		// BIT(1) → boolean
		{Match: MatchRule{SourceType: "bit", TypeMod: "(1)"}, TargetType: "boolean", DropTypemod: true, Transform: "bit-to-bool"},
		// BIT(N) → bit varying(N) for N > 1
		{Match: MatchRule{SourceType: "bit"}, TargetType: "bit varying($mod)", Transform: "bit-to-binstr"},
		// TINYINT(1) → boolean
		{Match: MatchRule{SourceType: "tinyint", TypeMod: "(1)"}, TargetType: "boolean", DropTypemod: true, Transform: "tinyint-to-bool"},
		// INT AUTO_INCREMENT → serial
		{Match: MatchRule{SourceType: "int", AutoIncrement: Yes}, TargetType: "serial", DropTypemod: true, Transform: "int-to-id"},
		// INT UNSIGNED AUTO_INCREMENT → bigserial
		{Match: MatchRule{SourceType: "int", Unsigned: Yes, AutoIncrement: Yes}, TargetType: "bigserial", DropTypemod: true, Transform: "int-to-id"},
		// BIGINT AUTO_INCREMENT → bigserial
		{Match: MatchRule{SourceType: "bigint", AutoIncrement: Yes}, TargetType: "bigserial", DropTypemod: true, Transform: "int-to-id"},
		// MEDIUMINT AUTO_INCREMENT → serial
		{Match: MatchRule{SourceType: "mediumint", AutoIncrement: Yes}, TargetType: "serial", DropTypemod: true},
		// SMALLINT AUTO_INCREMENT → serial
		{Match: MatchRule{SourceType: "smallint", AutoIncrement: Yes}, TargetType: "serial", DropTypemod: true},
		// TINYINT AUTO_INCREMENT → serial
		{Match: MatchRule{SourceType: "tinyint", AutoIncrement: Yes}, TargetType: "serial", DropTypemod: true},
		// Unsigned integers → wider types
		{Match: MatchRule{SourceType: "tinyint", Unsigned: Yes}, TargetType: "smallint", DropTypemod: true},
		{Match: MatchRule{SourceType: "smallint", Unsigned: Yes}, TargetType: "integer", DropTypemod: true},
		{Match: MatchRule{SourceType: "mediumint", Unsigned: Yes}, TargetType: "integer", DropTypemod: true},
		{Match: MatchRule{SourceType: "int", Unsigned: Yes}, TargetType: "bigint", DropTypemod: true},
		{Match: MatchRule{SourceType: "bigint", Unsigned: Yes}, TargetType: "numeric(20)", DropTypemod: true},
		// Integer types by size
		{Match: MatchRule{SourceType: "tinyint"}, TargetType: "smallint", DropTypemod: true},
		{Match: MatchRule{SourceType: "smallint"}, TargetType: "smallint", DropTypemod: true},
		{Match: MatchRule{SourceType: "mediumint"}, TargetType: "integer", DropTypemod: true},
		{Match: MatchRule{SourceType: "int"}, TargetType: "integer", DropTypemod: true},
		{Match: MatchRule{SourceType: "integer"}, TargetType: "integer", DropTypemod: true},
		{Match: MatchRule{SourceType: "bigint"}, TargetType: "bigint", DropTypemod: true},
		// Float types
		{Match: MatchRule{SourceType: "float"}, TargetType: "float"},
		{Match: MatchRule{SourceType: "double"}, TargetType: "double precision"},
		{Match: MatchRule{SourceType: "real"}, TargetType: "float"},
		// Decimal
		{Match: MatchRule{SourceType: "decimal"}, TargetType: "numeric($mod)"},
		{Match: MatchRule{SourceType: "numeric"}, TargetType: "numeric($mod)"},
		{Match: MatchRule{SourceType: "dec"}, TargetType: "numeric($mod)"},
		{Match: MatchRule{SourceType: "fixed"}, TargetType: "numeric($mod)"},
		// String types
		{Match: MatchRule{SourceType: "char"}, TargetType: "char($mod)"},
		{Match: MatchRule{SourceType: "varchar"}, TargetType: "varchar($mod)"},
		{Match: MatchRule{SourceType: "tinytext"}, TargetType: "text", DropTypemod: true, Transform: "remove-null-chars"},
		{Match: MatchRule{SourceType: "text"}, TargetType: "text", DropTypemod: true, Transform: "remove-null-chars"},
		{Match: MatchRule{SourceType: "mediumtext"}, TargetType: "text", DropTypemod: true, Transform: "remove-null-chars"},
		{Match: MatchRule{SourceType: "longtext"}, TargetType: "text", DropTypemod: true, Transform: "remove-null-chars"},
		// Binary types → bytea
		{Match: MatchRule{SourceType: "binary"}, TargetType: "bytea", DropTypemod: true},
		{Match: MatchRule{SourceType: "varbinary"}, TargetType: "bytea", DropTypemod: true},
		{Match: MatchRule{SourceType: "tinyblob"}, TargetType: "bytea", DropTypemod: true},
		{Match: MatchRule{SourceType: "blob"}, TargetType: "bytea", DropTypemod: true},
		{Match: MatchRule{SourceType: "mediumblob"}, TargetType: "bytea", DropTypemod: true},
		{Match: MatchRule{SourceType: "longblob"}, TargetType: "bytea", DropTypemod: true},
		// Date/time types
		{Match: MatchRule{SourceType: "datetime"}, TargetType: "timestamptz", DropTypemod: true, Transform: "zero-dates-to-null"},
		{Match: MatchRule{SourceType: "timestamp"}, TargetType: "timestamptz", DropTypemod: true, Transform: "zero-dates-to-null"},
		{Match: MatchRule{SourceType: "date"}, TargetType: "date", DropTypemod: true, Transform: "zero-dates-to-null"},
		{Match: MatchRule{SourceType: "time"}, TargetType: "time", DropTypemod: true},
		{Match: MatchRule{SourceType: "year"}, TargetType: "smallint", DropTypemod: true},
		// Enum/Set/JSON
		{Match: MatchRule{SourceType: "enum"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "set"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "json"}, TargetType: "jsonb", DropTypemod: true},
		// Spatial types
		{Match: MatchRule{SourceType: "geometry"}, TargetType: "geometry", DropTypemod: true, Transform: "wkt-to-geometry"},
		{Match: MatchRule{SourceType: "point"}, TargetType: "point", DropTypemod: true, Transform: "wkt-to-geometry"},
		{Match: MatchRule{SourceType: "linestring"}, TargetType: "path", DropTypemod: true, Transform: "wkt-to-geometry"},
		{Match: MatchRule{SourceType: "polygon"}, TargetType: "polygon", DropTypemod: true},
		// Serial types (these use IdentSeedRef in pgloader for sequence naming)
		{Match: MatchRule{SourceType: "serial"}, TargetType: "integer", DropTypemod: true},
		{Match: MatchRule{SourceType: "bigserial"}, TargetType: "bigint", DropTypemod: true},
	}
}

// ---------------------------------------------------------------------------
// Column type parsing utilities
// ---------------------------------------------------------------------------

// ColumnInfo holds parsed column metadata from MySQL information_schema.
type ColumnInfo struct {
	Name         string
	DataType     string // base type: "int", "varchar"
	ColumnType   string // full type: "int(10) unsigned"
	Nullable     bool
	Default      *string // nil = no default, empty string = default NULL
	Extra        string  // "auto_increment", "on update CURRENT_TIMESTAMP"
	Comment      string
	CharMaxLen   *int64
	CharOctLen   *int64
	NumericPrec  *int64
	NumericScale *int64
}

// ParsePrecision extracts numeric precision from ColumnType.
// "decimal(18,6)" → 18
func (ci ColumnInfo) Precision() int64 {
	if ci.NumericPrec != nil {
		return *ci.NumericPrec
	}
	p, _ := TypemodParams(ParseTypemod(ci.ColumnType))
	return p
}

// ParseScale extracts numeric scale from ColumnType.
// "decimal(18,6)" → 6
func (ci ColumnInfo) Scale() int64 {
	if ci.NumericScale != nil {
		return *ci.NumericScale
	}
	_, s := TypemodParams(ParseTypemod(ci.ColumnType))
	return s
}

// IsUnsigned reports whether the column type includes "unsigned".
func (ci ColumnInfo) IsUnsigned() bool {
	return strings.Contains(ci.ColumnType, "unsigned")
}

// IsAutoIncrement reports whether the column is auto_increment.
func (ci ColumnInfo) IsAutoIncrement() bool {
	return strings.Contains(ci.Extra, "auto_increment")
}

// FormatDefault formats a default value for PostgreSQL DDL.
func (ci ColumnInfo) FormatDefault() string {
	if ci.Default == nil {
		return ""
	}
	d := *ci.Default
	if d == "" {
		return "" // no default
	}
	// Handle MySQL special default values
	if d == "CURRENT_TIMESTAMP" || d == "current_timestamp()" || d == "CURRENT_TIMESTAMP()" {
		return "DEFAULT CURRENT_TIMESTAMP"
	}
	if d == "NULL" {
		return "DEFAULT NULL"
	}
	// String type defaults need quoting
	switch ci.DataType {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext",
		"enum", "set", "date", "datetime", "timestamp", "time":
		return fmt.Sprintf("DEFAULT '%s'", escapeDefault(d))
	default:
		return fmt.Sprintf("DEFAULT %s", d)
	}
}

func escapeDefault(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`'`, `''`,
	).Replace(s)
}

// IsSupportedType returns true if the MySQL type is supported for import.
func IsSupportedType(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "geometry", "point", "linestring", "polygon",
		"multipoint", "multilinestring", "multipolygon",
		"geometrycollection":
		return true
	default:
		return true // all types are supported, some just need special handling
	}
}

// MustUseSTAsText returns true if the MySQL type needs ST_AsText() in SELECT.
func MustUseSTAsText(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "geometry", "point", "linestring", "polygon",
		"multipoint", "multilinestring", "multipolygon",
		"geometrycollection":
		return true
	default:
		return false
	}
}

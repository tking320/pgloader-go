package pgsql

import (
	"fmt"
	"strings"

	"github.com/tking320/pgloader-go/internal/catalog"
)

// ---------------------------------------------------------------------------
// Type DDL generation
// ---------------------------------------------------------------------------

// typeCreateSQL generates a CREATE TYPE statement for a custom type.
func typeCreateSQL(typ *catalog.SQLType) string {
	switch typ.Type {
	case "enum":
		return enumCreateSQL(typ)
	case "domain":
		return domainCreateSQL(typ)
	case "composite":
		return compositeCreateSQL(typ)
	default:
		return ""
	}
}

// enumCreateSQL generates CREATE TYPE ... AS ENUM.
func enumCreateSQL(typ *catalog.SQLType) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TYPE %s AS ENUM (", qualifiedTypeName(typ))
	labels := make([]string, len(typ.Elements))
	for i, e := range typ.Elements {
		labels[i] = quoteLiteral(e)
	}
	b.WriteString(strings.Join(labels, ", "))
	b.WriteString(")")
	return b.String()
}

// domainCreateSQL generates CREATE DOMAIN statement.
func domainCreateSQL(typ *catalog.SQLType) string {
	return fmt.Sprintf("CREATE DOMAIN %s AS %s", qualifiedTypeName(typ), typ.BaseType)
}

// compositeCreateSQL generates CREATE TYPE ... AS (attr1 type1, attr2 type2).
func compositeCreateSQL(typ *catalog.SQLType) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TYPE %s AS (", qualifiedTypeName(typ))
	attrs := make([]string, len(typ.AttrDefs))
	for i, a := range typ.AttrDefs {
		col := fmt.Sprintf("%s %s", quoteIdent(a.Name), a.TypeName)
		if a.Collation != "" {
			col += " COLLATE " + quoteIdent(a.Collation)
		}
		attrs[i] = col
	}
	b.WriteString(strings.Join(attrs, ", "))
	b.WriteString(")")
	return b.String()
}

// qualifiedTypeName returns the fully qualified name for a type.
func qualifiedTypeName(typ *catalog.SQLType) string {
	if typ.Schema != "" {
		return fmt.Sprintf("%s.%s", quoteIdent(typ.Schema), quoteIdent(typ.Name))
	}
	return quoteIdent(typ.Name)
}

// ---------------------------------------------------------------------------
// Trigger DDL
// ---------------------------------------------------------------------------

// trigCreateSQL reconstructs a CREATE TRIGGER statement from catalog data.
// If the trigger has a full SQL definition stored in Action, uses it directly.
func trigCreateSQL(trig *catalog.Trigger, t *catalog.Table) string {
	if trig.Action != "" {
		// Use the stored definition (from pg_get_triggerdef)
		return trig.Action
	}

	// Simple trigger reconstruction (limited)
	if trig.Timing == "" || trig.Events == "" {
		return ""
	}
	return fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH %s %s",
		quoteIdent(trig.Name), trig.Timing, trig.Events,
		t.QualifiedName(), trig.ForEach, trig.Procedure)
}

// ---------------------------------------------------------------------------
// SQL literal quoting
// ---------------------------------------------------------------------------

// quoteLiteral quotes a string as a SQL literal.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

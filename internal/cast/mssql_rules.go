package cast

// MSSQLDefaultRules returns the default MSSQL -> PostgreSQL cast rules,
// matching pgloader's *mssql-default-cast-rules*.
func MSSQLDefaultRules() []CastRule {
	return []CastRule{
		// String types: all variable-length → text with drop typemod
		{Match: MatchRule{SourceType: "char"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "nchar"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "varchar"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "nvarchar"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "ntext"}, TargetType: "text", DropTypemod: true},

		// XML → xml (PG native)
		{Match: MatchRule{SourceType: "xml"}, TargetType: "xml", DropTypemod: true},

		// Integer identity (auto-increment) → serial types
		{Match: MatchRule{SourceType: "int", AutoIncrement: Yes}, TargetType: "bigserial", DropTypemod: true},
		{Match: MatchRule{SourceType: "bigint", AutoIncrement: Yes}, TargetType: "bigserial", DropTypemod: true},
		{Match: MatchRule{SourceType: "smallint", AutoIncrement: Yes}, TargetType: "smallserial", DropTypemod: true},
		{Match: MatchRule{SourceType: "tinyint", AutoIncrement: Yes}, TargetType: "serial", DropTypemod: true},

		// Integer types
		{Match: MatchRule{SourceType: "tinyint"}, TargetType: "smallint", DropTypemod: true},
		{Match: MatchRule{SourceType: "smallint"}, TargetType: "smallint", DropTypemod: true},
		{Match: MatchRule{SourceType: "int"}, TargetType: "integer", DropTypemod: true},
		{Match: MatchRule{SourceType: "bigint"}, TargetType: "bigint", DropTypemod: true},

		// Bit → boolean (with transform for 0/1 → true/false)
		{Match: MatchRule{SourceType: "bit"}, TargetType: "boolean", DropTypemod: true, Transform: "sql-server-bit-to-boolean"},

		// Uniqueidentifier → uuid (with transform to strip braces/hyphens)
		{Match: MatchRule{SourceType: "uniqueidentifier"}, TargetType: "uuid", DropTypemod: true, Transform: "sql-server-uniqueidentifier-to-uuid"},

		// Hierarchyid / geography → bytea
		{Match: MatchRule{SourceType: "hierarchyid"}, TargetType: "bytea", DropTypemod: true, Transform: "byte-vector-to-bytea"},
		{Match: MatchRule{SourceType: "geography"}, TargetType: "bytea", DropTypemod: true, Transform: "byte-vector-to-bytea"},

		// Float types: use float-to-string transform for COPY compatibility
		{Match: MatchRule{SourceType: "float"}, TargetType: "float", Transform: "float-to-string"},
		{Match: MatchRule{SourceType: "real"}, TargetType: "real", Transform: "float-to-string"},
		// MSSQL "double" → PG "double precision" (PG has no bare "double" type)
		{Match: MatchRule{SourceType: "double"}, TargetType: "double precision", DropTypemod: true, Transform: "float-to-string"},

		// Decimal / numeric
		{Match: MatchRule{SourceType: "numeric"}, TargetType: "numeric", Transform: "float-to-string"},
		{Match: MatchRule{SourceType: "decimal"}, TargetType: "numeric", Transform: "float-to-string"},

		// Money types → numeric
		{Match: MatchRule{SourceType: "money"}, TargetType: "numeric", DropTypemod: true, Transform: "float-to-string"},
		{Match: MatchRule{SourceType: "smallmoney"}, TargetType: "numeric", DropTypemod: true, Transform: "float-to-string"},

		// Binary types → bytea
		{Match: MatchRule{SourceType: "binary"}, TargetType: "bytea", DropTypemod: true, Transform: "byte-vector-to-bytea"},
		{Match: MatchRule{SourceType: "varbinary"}, TargetType: "bytea", DropTypemod: true, Transform: "byte-vector-to-bytea"},
		{Match: MatchRule{SourceType: "image"}, TargetType: "bytea", DropTypemod: true, Transform: "byte-vector-to-bytea"},

		// Date/time types → timestamptz
		{Match: MatchRule{SourceType: "smalldatetime"}, TargetType: "timestamptz", DropTypemod: true},
		{Match: MatchRule{SourceType: "datetime"}, TargetType: "timestamptz", DropTypemod: true},
		{Match: MatchRule{SourceType: "datetime2"}, TargetType: "timestamptz", DropTypemod: true},
		{Match: MatchRule{SourceType: "datetimeoffset"}, TargetType: "timestamptz", DropTypemod: true},
	}
}

// GetMSSQLColumnType returns the column type string for the given MSSQL type.
// This mirrors mssql-column-ctype in the reference implementation, handling
// float precision and decimal/numeric scale.
func GetMSSQLColumnType(typeName string, numericPrecision, numericScale, charMaxLen, datetimePrecision int64) string {
	switch typeName {
	case "float", "real":
		if numericPrecision > 0 {
			return formatMSSQLType(typeName, numericPrecision)
		}
		return typeName
	case "decimal", "numeric":
		if numericPrecision > 0 {
			return formatMSSQLTypeWithScale(typeName, numericPrecision, numericScale)
		}
		return typeName
	case "char", "nchar", "varchar", "nvarchar", "binary":
		if charMaxLen > 0 && charMaxLen != -1 {
			return formatMSSQLType(typeName, charMaxLen)
		}
		return typeName
	case "smalldatetime", "datetime":
		if datetimePrecision > 0 {
			return formatMSSQLType(typeName, datetimePrecision)
		}
		return typeName
	default:
		return typeName
	}
}

func formatMSSQLType(name string, param int64) string {
	return name + "(" + formatInt64(param) + ")"
}

func formatMSSQLTypeWithScale(name string, precision, scale int64) string {
	if scale <= 0 {
		return formatMSSQLType(name, precision)
	}
	return name + "(" + formatInt64(precision) + "," + formatInt64(scale) + ")"
}

func formatInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	neg := n < 0
	if neg {
		n = -n
	}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

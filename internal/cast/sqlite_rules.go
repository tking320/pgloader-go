package cast

// SQLiteDefaultRules returns the default SQLite -> PostgreSQL cast rules.
func SQLiteDefaultRules() []CastRule {
	return []CastRule{
		// INTEGER PRIMARY KEY AUTOINCREMENT -> bigserial
		{Match: MatchRule{SourceType: "integer", AutoIncrement: Yes}, TargetType: "bigserial", DropTypemod: true, Transform: "int-to-id"},
		// INTEGER -> bigint (SQLite INTEGER can be 1-8 bytes)
		{Match: MatchRule{SourceType: "integer"}, TargetType: "bigint", DropTypemod: true},
		// TINYINT -> smallint
		{Match: MatchRule{SourceType: "tinyint"}, TargetType: "smallint", DropTypemod: true},
		// SMALLINT -> smallint
		{Match: MatchRule{SourceType: "smallint"}, TargetType: "smallint", DropTypemod: true},
		// MEDIUMINT -> integer
		{Match: MatchRule{SourceType: "mediumint"}, TargetType: "integer", DropTypemod: true},
		// BIGINT -> bigint
		{Match: MatchRule{SourceType: "bigint"}, TargetType: "bigint", DropTypemod: true},
		// INT -> integer
		{Match: MatchRule{SourceType: "int"}, TargetType: "integer", DropTypemod: true},
		// REAL -> real
		{Match: MatchRule{SourceType: "real"}, TargetType: "real", DropTypemod: true},
		// FLOAT -> float
		{Match: MatchRule{SourceType: "float"}, TargetType: "float", DropTypemod: true},
		// DOUBLE / DOUBLE PRECISION -> double precision
		{Match: MatchRule{SourceType: "double"}, TargetType: "double precision", DropTypemod: true},
		// NUMERIC -> numeric (with typemod)
		{Match: MatchRule{SourceType: "numeric"}, TargetType: "numeric($mod)"},
		// DECIMAL -> decimal (with typemod)
		{Match: MatchRule{SourceType: "decimal"}, TargetType: "decimal($mod)"},
		// TEXT types -> text
		{Match: MatchRule{SourceType: "text"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "varchar"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "nvarchar"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "char"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "nchar"}, TargetType: "text", DropTypemod: true},
		{Match: MatchRule{SourceType: "clob"}, TargetType: "text", DropTypemod: true},
		// BLOB -> bytea
		{Match: MatchRule{SourceType: "blob"}, TargetType: "bytea", DropTypemod: true},
		// Boolean (SQLite stores as INTEGER)
		{Match: MatchRule{SourceType: "boolean"}, TargetType: "boolean", DropTypemod: true},
		// BOOL -> boolean
		{Match: MatchRule{SourceType: "bool"}, TargetType: "boolean", DropTypemod: true},
		// Date/time types
		{Match: MatchRule{SourceType: "datetime"}, TargetType: "timestamptz", DropTypemod: true, Transform: "zero-dates-to-null"},
		{Match: MatchRule{SourceType: "timestamp"}, TargetType: "timestamp", DropTypemod: true},
		{Match: MatchRule{SourceType: "date"}, TargetType: "date", DropTypemod: true},
		{Match: MatchRule{SourceType: "time"}, TargetType: "time", DropTypemod: true},
	}
}

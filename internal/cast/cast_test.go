package cast

import (
	"testing"
)

func TestEngine_Apply(t *testing.T) {
	engine := NewEngine(MySQLDefaultRules())

	tests := []struct {
		name       string
		typeName   string
		columnType string
		extra      string
		wantType   string
		wantDrop   bool
		wantXform  string
	}{
		// Integer types
		{"tinyint(1)", "tinyint", "tinyint(1)", "", "boolean", true, "tinyint-to-bool"},
		{"tinyint(4)", "tinyint", "tinyint(4)", "", "smallint", true, ""},
		{"smallint(6)", "smallint", "smallint(6)", "", "smallint", true, ""},
		{"mediumint(9)", "mediumint", "mediumint(9)", "", "integer", true, ""},
		{"int(11)", "int", "int(11)", "", "integer", true, ""},
		{"bigint(20)", "bigint", "bigint(20)", "", "bigint", true, ""},

		// Unsigned integers
		{"tinyint unsigned", "tinyint", "tinyint(3) unsigned", "", "smallint", true, ""},
		{"smallint unsigned", "smallint", "smallint(5) unsigned", "", "integer", true, ""},
		{"mediumint unsigned", "mediumint", "mediumint(8) unsigned", "", "integer", true, ""},
		{"int unsigned", "int", "int(10) unsigned", "", "bigint", true, ""},
		{"bigint unsigned", "bigint", "bigint(20) unsigned", "", "numeric(20)", true, ""},

		// Auto increment
		{"int auto_increment", "int", "int(11)", "auto_increment", "serial", true, "int-to-id"},
		{"bigint auto_increment", "bigint", "bigint(20)", "auto_increment", "bigserial", true, "int-to-id"},
		{"mediumint auto_increment", "mediumint", "mediumint(9)", "auto_increment", "serial", true, ""},
		{"smallint auto_increment", "smallint", "smallint(6)", "auto_increment", "serial", true, ""},
		{"tinyint auto_increment", "tinyint", "tinyint(4)", "auto_increment", "serial", true, ""},

		// Float types
		{"float", "float", "float", "", "float", false, ""},
		{"double", "double", "double", "", "double precision", false, ""},

		// Decimal
		{"decimal(18,6)", "decimal", "decimal(18,6)", "", "numeric($mod)", false, ""},
		{"numeric(10,2)", "numeric", "numeric(10,2)", "", "numeric($mod)", false, ""},

		// String types
		{"char(1)", "char", "char(1)", "", "char($mod)", false, ""},
		{"varchar(255)", "varchar", "varchar(255)", "", "varchar($mod)", false, ""},
		{"tinytext", "tinytext", "tinytext", "", "text", true, "remove-null-chars"},
		{"text", "text", "text", "", "text", true, "remove-null-chars"},
		{"mediumtext", "mediumtext", "mediumtext", "", "text", true, "remove-null-chars"},
		{"longtext", "longtext", "longtext", "", "text", true, "remove-null-chars"},

		// Binary types
		{"binary(16)", "binary", "binary(16)", "", "bytea", true, ""},
		{"varbinary(255)", "varbinary", "varbinary(255)", "", "bytea", true, ""},
		{"blob", "blob", "blob", "", "bytea", true, ""},
		{"longblob", "longblob", "longblob", "", "bytea", true, ""},

		// Date/time types
		{"datetime", "datetime", "datetime", "", "timestamptz", true, "zero-dates-to-null"},
		{"timestamp", "timestamp", "timestamp", "", "timestamptz", true, "zero-dates-to-null"},
		{"date", "date", "date", "", "date", true, "zero-dates-to-null"},
		{"year", "year", "year(4)", "", "smallint", true, ""},

		// Enum/Set/JSON
		{"enum", "enum", "enum('a','b')", "", "text", true, ""},
		{"set", "set", "set('x','y')", "", "text", true, ""},
		{"json", "json", "json", "", "jsonb", true, ""},

		// BIT
		{"bit(1)", "bit", "bit(1)", "", "boolean", true, "bit-to-bool"},
		{"bit(8)", "bit", "bit(8)", "", "bit varying($mod)", false, "bit-to-binstr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.Apply(tt.typeName, tt.columnType, tt.extra)
			if got.TargetType != tt.wantType {
				t.Errorf("Apply().TargetType = %q, want %q", got.TargetType, tt.wantType)
			}
			if got.DropTypemod != tt.wantDrop {
				t.Errorf("Apply().DropTypemod = %v, want %v", got.DropTypemod, tt.wantDrop)
			}
			if got.Transform != tt.wantXform {
				t.Errorf("Apply().Transform = %q, want %q", got.Transform, tt.wantXform)
			}
		})
	}
}

func TestParseTypemod(t *testing.T) {
	tests := []struct {
		columnType string
		want       string
	}{
		{"varchar(255)", "255"},
		{"decimal(18,6)", "18,6"},
		{"int(11)", "11"},
		{"int", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.columnType, func(t *testing.T) {
			if got := ParseTypemod(tt.columnType); got != tt.want {
				t.Errorf("ParseTypemod(%q) = %q, want %q", tt.columnType, got, tt.want)
			}
		})
	}
}

func TestBaseTypeName(t *testing.T) {
	tests := []struct {
		columnType string
		want       string
	}{
		{"tinyint(1)", "tinyint"},
		{"int(10) unsigned", "int"},
		{"varchar(255)", "varchar"},
		{"decimal(18,6)", "decimal"},
		{"bigint(20) unsigned", "bigint"},
	}
	for _, tt := range tests {
		t.Run(tt.columnType, func(t *testing.T) {
			if got := BaseTypeName(tt.columnType); got != tt.want {
				t.Errorf("BaseTypeName(%q) = %q, want %q", tt.columnType, got, tt.want)
			}
		})
	}
}

func TestSubstituteTypemod(t *testing.T) {
	tests := []struct {
		target string
		mod    string
		want   string
	}{
		{"varchar($mod)", "255", "varchar(255)"},
		{"numeric($mod)", "18,6", "numeric(18,6)"},
		{"integer", "", "integer"},
		{"bit varying($mod)", "8", "bit varying(8)"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			if got := SubstituteTypemod(tt.target, tt.mod); got != tt.want {
				t.Errorf("SubstituteTypemod(%q, %q) = %q, want %q", tt.target, tt.mod, got, tt.want)
			}
		})
	}
}

func TestColumnInfo_FormatDefault(t *testing.T) {
	tests := []struct {
		name string
		info ColumnInfo
		want string
	}{
		{
			"null default",
			ColumnInfo{DataType: "int", Default: strPtr("NULL")},
			"DEFAULT NULL",
		},
		{
			"numeric default",
			ColumnInfo{DataType: "int", Default: strPtr("0")},
			"DEFAULT 0",
		},
		{
			"string default",
			ColumnInfo{DataType: "varchar", Default: strPtr("hello")},
			"DEFAULT 'hello'",
		},
		{
			"current timestamp",
			ColumnInfo{DataType: "datetime", Default: strPtr("CURRENT_TIMESTAMP")},
			"DEFAULT CURRENT_TIMESTAMP",
		},
		{
			"no default",
			ColumnInfo{DataType: "int", Default: nil},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.FormatDefault(); got != tt.want {
				t.Errorf("FormatDefault() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMustUseSTAsText(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		want     bool
	}{
		{"geometry", "geometry", true},
		{"point", "point", true},
		{"linestring", "linestring", true},
		{"int", "int", false},
		{"varchar", "varchar", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MustUseSTAsText(tt.typeName); got != tt.want {
				t.Errorf("MustUseSTAsText(%q) = %v, want %v", tt.typeName, got, tt.want)
			}
		})
	}
}

func TestPgDefaultRules(t *testing.T) {
	engine := NewEngine(PgDefaultRules())

	tests := []struct {
		name       string
		typeName   string
		columnType string
		extra      string
		wantType   string
		wantDrop   bool
		wantXform  string
	}{
		{"money", "money", "money", "", "numeric", true, "money-to-numeric"},
		{"oid", "oid", "oid", "", "oid", true, ""},
		{"regclass", "regclass", "regclass", "", "regclass", true, ""},
		{"xid", "xid", "xid", "", "bigint", true, ""},
		{"txid_snapshot", "txid_snapshot", "txid_snapshot", "", "text", true, ""},
		{"tid", "tid", "tid", "", "tid", true, ""},
		{"pg_lsn", "pg_lsn", "pg_lsn", "", "text", true, ""},
		{"pg_node_tree", "pg_node_tree", "pg_node_tree", "", "text", true, ""},
		{"integer", "integer", "integer", "", "integer", false, ""},
		{"text", "text", "text", "", "text", false, ""},
		{"boolean", "boolean", "boolean", "", "boolean", false, ""},
		{"jsonb", "jsonb", "jsonb", "", "jsonb", false, ""},
		{"uuid", "uuid", "uuid", "", "uuid", false, ""},
		{"bytea", "bytea", "bytea", "", "bytea", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.Apply(tt.typeName, tt.columnType, tt.extra)
			if got.TargetType != tt.wantType {
				t.Errorf("Apply().TargetType = %q, want %q", got.TargetType, tt.wantType)
			}
			if got.DropTypemod != tt.wantDrop {
				t.Errorf("Apply().DropTypemod = %v, want %v", got.DropTypemod, tt.wantDrop)
			}
			if got.Transform != tt.wantXform {
				t.Errorf("Apply().Transform = %q, want %q", got.Transform, tt.wantXform)
			}
		})
	}
}

func TestMSSQLDefaultRules(t *testing.T) {
	engine := NewEngine(MSSQLDefaultRules())

	tests := []struct {
		name       string
		typeName   string
		columnType string
		extra      string
		wantType   string
		wantDrop   bool
		wantXform  string
	}{
		// String types
		{"char", "char", "char", "", "text", true, ""},
		{"nchar", "nchar", "nchar", "", "text", true, ""},
		{"varchar", "varchar", "varchar", "", "text", true, ""},
		{"nvarchar", "nvarchar", "nvarchar", "", "text", true, ""},
		{"ntext", "ntext", "ntext", "", "text", true, ""},

		// XML
		{"xml", "xml", "xml", "", "xml", true, ""},

		// Integer identity
		{"int identity", "int", "int", "auto_increment", "bigserial", true, ""},
		{"bigint identity", "bigint", "bigint", "auto_increment", "bigserial", true, ""},
		{"smallint identity", "smallint", "smallint", "auto_increment", "smallserial", true, ""},
		{"tinyint identity", "tinyint", "tinyint", "auto_increment", "serial", true, ""},

		// Integer types
		{"tinyint", "tinyint", "tinyint", "", "smallint", true, ""},
		{"smallint", "smallint", "smallint", "", "smallint", true, ""},
		{"int", "int", "int", "", "integer", true, ""},
		{"bigint", "bigint", "bigint", "", "bigint", true, ""},

		// Bit → boolean
		{"bit", "bit", "bit", "", "boolean", true, "sql-server-bit-to-boolean"},

		// Uniqueidentifier → uuid
		{"uniqueidentifier", "uniqueidentifier", "uniqueidentifier", "", "uuid", true, "sql-server-uniqueidentifier-to-uuid"},

		// Hierarchyid / geography → bytea
		{"hierarchyid", "hierarchyid", "hierarchyid", "", "bytea", true, "byte-vector-to-bytea"},
		{"geography", "geography", "geography", "", "bytea", true, "byte-vector-to-bytea"},

		// Float types with float-to-string transform
		{"float", "float", "float", "", "float", false, "float-to-string"},
		{"real", "real", "real", "", "real", false, "float-to-string"},

		// Decimal/numeric with float-to-string
		{"numeric", "numeric", "numeric", "", "numeric", false, "float-to-string"},
		{"decimal", "decimal", "decimal", "", "numeric", false, "float-to-string"},

		// Money types
		{"money", "money", "money", "", "numeric", true, "float-to-string"},
		{"smallmoney", "smallmoney", "smallmoney", "", "numeric", true, "float-to-string"},

		// Binary types → bytea
		{"binary", "binary", "binary", "", "bytea", true, "byte-vector-to-bytea"},
		{"varbinary", "varbinary", "varbinary", "", "bytea", true, "byte-vector-to-bytea"},
		{"image", "image", "image", "", "bytea", true, "byte-vector-to-bytea"},

		// Date/time types
		{"smalldatetime", "smalldatetime", "smalldatetime", "", "timestamptz", true, ""},
		{"datetime", "datetime", "datetime", "", "timestamptz", true, ""},
		{"datetime2", "datetime2", "datetime2", "", "timestamptz", true, ""},
		{"datetimeoffset", "datetimeoffset", "datetimeoffset", "", "timestamptz", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.Apply(tt.typeName, tt.columnType, tt.extra)
			if got.TargetType != tt.wantType {
				t.Errorf("Apply().TargetType = %q, want %q", got.TargetType, tt.wantType)
			}
			if got.DropTypemod != tt.wantDrop {
				t.Errorf("Apply().DropTypemod = %v, want %v", got.DropTypemod, tt.wantDrop)
			}
			if got.Transform != tt.wantXform {
				t.Errorf("Apply().Transform = %q, want %q", got.Transform, tt.wantXform)
			}
		})
	}
}

func TestGetMSSQLColumnType(t *testing.T) {
	tests := []struct {
		name            string
		typeName        string
		numericPrec     int64
		numericScale    int64
		charMaxLen      int64
		datetimePrec    int64
		want            string
	}{
		{"float with precision", "float", 53, 0, 0, 0, "float(53)"},
		{"float no precision", "float", 0, 0, 0, 0, "float"},
		{"decimal with scale", "decimal", 18, 6, 0, 0, "decimal(18,6)"},
		{"decimal no scale", "decimal", 18, 0, 0, 0, "decimal(18)"},
		{"varchar with length", "varchar", 0, 0, 255, 0, "varchar(255)"},
		{"nchar with length", "nchar", 0, 0, 10, 0, "nchar(10)"},
		{"varchar max", "varchar", 0, 0, -1, 0, "varchar"},
		{"datetime with precision", "datetime", 0, 0, 0, 3, "datetime(3)"},
		{"plain type", "int", 0, 0, 0, 0, "int"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetMSSQLColumnType(tt.typeName, tt.numericPrec, tt.numericScale, tt.charMaxLen, tt.datetimePrec); got != tt.want {
				t.Errorf("GetMSSQLColumnType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func strPtr(s string) *string {
	return &s
}

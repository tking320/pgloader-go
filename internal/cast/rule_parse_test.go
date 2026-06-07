package cast

import (
	"testing"
)

func TestParseCastRule(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    CastRule
		wantErr bool
	}{
		{
			name:  "simple type with drop typemod",
			input: "type integer to bigint drop typemod",
			want: CastRule{
				Match:       MatchRule{SourceType: "integer"},
				TargetType:  "bigint",
				DropTypemod: true,
			},
		},
		{
			name:  "with attached typemod and transform",
			input: "type tinyint(1) to boolean drop typemod using tinyint-to-bool",
			want: CastRule{
				Match:       MatchRule{SourceType: "tinyint", TypeMod: "(1)"},
				TargetType:  "boolean",
				DropTypemod: true,
				Transform:   "tinyint-to-bool",
			},
		},
		{
			name:  "with detached typemod and transform",
			input: "type bit (1) to boolean drop typemod using bit-to-bool",
			want: CastRule{
				Match:       MatchRule{SourceType: "bit", TypeMod: "(1)"},
				TargetType:  "boolean",
				DropTypemod: true,
				Transform:   "bit-to-bool",
			},
		},
		{
			name:  "with auto_increment",
			input: "type integer auto_increment to bigserial drop typemod using int-to-id",
			want: CastRule{
				Match:       MatchRule{SourceType: "integer", AutoIncrement: Yes},
				TargetType:  "bigserial",
				DropTypemod: true,
				Transform:   "int-to-id",
			},
		},
		{
			name:  "with unsigned and auto_increment",
			input: "type int unsigned auto_increment to bigserial drop typemod using int-to-id",
			want: CastRule{
				Match:       MatchRule{SourceType: "int", Unsigned: Yes, AutoIncrement: Yes},
				TargetType:  "bigserial",
				DropTypemod: true,
				Transform:   "int-to-id",
			},
		},
		{
			name:  "with unsigned",
			input: "type int unsigned to bigint drop typemod",
			want: CastRule{
				Match:       MatchRule{SourceType: "int", Unsigned: Yes},
				TargetType:  "bigint",
				DropTypemod: true,
			},
		},
		{
			name:  "multi-word target type",
			input: "type double to double precision",
			want: CastRule{
				Match:      MatchRule{SourceType: "double"},
				TargetType: "double precision",
			},
		},
		{
			name:  "multi-word target with $mod",
			input: "type bit to bit varying($mod) using bit-to-binstr",
			want: CastRule{
				Match:      MatchRule{SourceType: "bit"},
				TargetType: "bit varying($mod)",
				Transform:  "bit-to-binstr",
			},
		},
		{
			name:  "target with $mod",
			input: "type numeric to numeric($mod)",
			want: CastRule{
				Match:      MatchRule{SourceType: "numeric"},
				TargetType: "numeric($mod)",
			},
		},
		{
			name:  "with drop default drop not null",
			input: "type datetime to timestamptz drop default drop not null using zero-dates-to-null",
			want: CastRule{
				Match:      MatchRule{SourceType: "datetime"},
				TargetType: "timestamptz",
				Transform:  "zero-dates-to-null",
			},
		},
		{
			name:  "boolean pass-through",
			input: "type boolean to boolean drop typemod",
			want: CastRule{
				Match:       MatchRule{SourceType: "boolean"},
				TargetType:  "boolean",
				DropTypemod: true,
			},
		},
		{
			name:  "blob to bytea",
			input: "type blob to bytea drop typemod",
			want: CastRule{
				Match:       MatchRule{SourceType: "blob"},
				TargetType:  "bytea",
				DropTypemod: true,
			},
		},
		{
			name:  "mssql uniqueidentifier to uuid",
			input: "type uniqueidentifier to uuid drop typemod using sql-server-uniqueidentifier-to-uuid",
			want: CastRule{
				Match:       MatchRule{SourceType: "uniqueidentifier"},
				TargetType:  "uuid",
				DropTypemod: true,
				Transform:   "sql-server-uniqueidentifier-to-uuid",
			},
		},
		{
			name:  "mssql varchar to text",
			input: "type varchar to text drop typemod",
			want: CastRule{
				Match:       MatchRule{SourceType: "varchar"},
				TargetType:  "text",
				DropTypemod: true,
			},
		},
		{
			name:    "missing to keyword",
			input:   "type integer bigint",
			wantErr: true,
		},
		{
			name:    "missing target type",
			input:   "type integer to",
			wantErr: true,
		},
		{
			name:    "empty rule",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCastRule(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCastRule() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if got.Match.SourceType != tt.want.Match.SourceType {
				t.Errorf("SourceType = %q, want %q", got.Match.SourceType, tt.want.Match.SourceType)
			}
			if got.Match.TypeMod != tt.want.Match.TypeMod {
				t.Errorf("TypeMod = %q, want %q", got.Match.TypeMod, tt.want.Match.TypeMod)
			}
			if got.Match.Unsigned != tt.want.Match.Unsigned {
				t.Errorf("Unsigned = %v, want %v", got.Match.Unsigned, tt.want.Match.Unsigned)
			}
			if got.Match.AutoIncrement != tt.want.Match.AutoIncrement {
				t.Errorf("AutoIncrement = %v, want %v", got.Match.AutoIncrement, tt.want.Match.AutoIncrement)
			}
			if got.TargetType != tt.want.TargetType {
				t.Errorf("TargetType = %q, want %q", got.TargetType, tt.want.TargetType)
			}
			if got.DropTypemod != tt.want.DropTypemod {
				t.Errorf("DropTypemod = %v, want %v", got.DropTypemod, tt.want.DropTypemod)
			}
			if got.Transform != tt.want.Transform {
				t.Errorf("Transform = %q, want %q", got.Transform, tt.want.Transform)
			}
		})
	}
}

func TestParseCastRules(t *testing.T) {
	t.Run("multiple rules", func(t *testing.T) {
		rules := []string{
			"type datetime to timestamptz drop typemod using zero-dates-to-null",
			"type date to date drop typemod",
			"type int auto_increment to serial drop typemod using int-to-id",
		}
		got, err := ParseCastRules(rules)
		if err != nil {
			t.Fatalf("ParseCastRules() error = %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d rules, want 3", len(got))
		}

		// First rule: datetime → timestamptz
		if got[0].Match.SourceType != "datetime" {
			t.Errorf("rule[0] SourceType = %q, want %q", got[0].Match.SourceType, "datetime")
		}
		if got[0].TargetType != "timestamptz" {
			t.Errorf("rule[0] TargetType = %q, want %q", got[0].TargetType, "timestamptz")
		}
		if got[0].Transform != "zero-dates-to-null" {
			t.Errorf("rule[0] Transform = %q, want %q", got[0].Transform, "zero-dates-to-null")
		}

		// Second rule: date → date
		if got[1].Match.SourceType != "date" {
			t.Errorf("rule[1] SourceType = %q, want %q", got[1].Match.SourceType, "date")
		}
		if got[1].TargetType != "date" {
			t.Errorf("rule[1] TargetType = %q, want %q", got[1].TargetType, "date")
		}

		// Third rule: int auto_increment → serial
		if got[2].Match.SourceType != "int" {
			t.Errorf("rule[2] SourceType = %q, want %q", got[2].Match.SourceType, "int")
		}
		if got[2].Match.AutoIncrement != Yes {
			t.Errorf("rule[2] AutoIncrement should be Yes")
		}
		if got[2].TargetType != "serial" {
			t.Errorf("rule[2] TargetType = %q, want %q", got[2].TargetType, "serial")
		}
		if got[2].Transform != "int-to-id" {
			t.Errorf("rule[2] Transform = %q, want %q", got[2].Transform, "int-to-id")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		got, err := ParseCastRules(nil)
		if err != nil {
			t.Fatalf("ParseCastRules(nil) error = %v", err)
		}
		if got != nil {
			t.Errorf("ParseCastRules(nil) = %v, want nil", got)
		}

		got, err = ParseCastRules([]string{})
		if err != nil {
			t.Fatalf("ParseCastRules([]) error = %v", err)
		}
		if got != nil {
			t.Errorf("ParseCastRules([]) = %v, want nil", got)
		}
	})

	t.Run("error in one rule fails all", func(t *testing.T) {
		rules := []string{
			"type int to bigint",
			"this is not a valid rule",
		}
		_, err := ParseCastRules(rules)
		if err == nil {
			t.Fatal("ParseCastRules() expected error for invalid rule")
		}
	})

	t.Run("mysql sample rules", func(t *testing.T) {
		rules := []string{
			"type datetime to timestamptz drop typemod using zero-dates-to-null",
			"type tinyint to smallint drop typemod",
			"type smallint unsigned to integer drop typemod",
			"type int auto_increment to serial drop typemod using int-to-id",
			"type int unsigned auto_increment to bigserial drop typemod using int-to-id",
			"type double to double precision",
			"type decimal to numeric($mod)",
			"type enum to text drop typemod",
			"type geometry to geometry drop typemod using wkt-to-geometry",
		}
		got, err := ParseCastRules(rules)
		if err != nil {
			t.Fatalf("ParseCastRules() error = %v", err)
		}
		if len(got) != 9 {
			t.Fatalf("got %d rules, want 9", len(got))
		}
	})

	t.Run("mssql sample rules", func(t *testing.T) {
		rules := []string{
			"type datetime to timestamptz drop default drop not null using zero-dates-to-null",
			"type varchar to text drop typemod",
			"type int to bigint drop typemod",
			"type bit to boolean drop typemod using sql-server-bit-to-boolean",
			"type uniqueidentifier to uuid drop typemod using sql-server-uniqueidentifier-to-uuid",
			"type hierarchyid to bytea drop typemod using byte-vector-to-bytea",
			"type money to numeric drop typemod",
		}
		got, err := ParseCastRules(rules)
		if err != nil {
			t.Fatalf("ParseCastRules() error = %v", err)
		}
		if len(got) != 7 {
			t.Fatalf("got %d rules, want 7", len(got))
		}
	})
}

func TestParseCastRuleIntegration(t *testing.T) {
	// Verify that parsed rules work correctly with Engine.Apply
	t.Run("parsed rules integrate with engine", func(t *testing.T) {
		userRules, err := ParseCastRules([]string{
			"type foo to text drop typemod",
		})
		if err != nil {
			t.Fatalf("ParseCastRules() error = %v", err)
		}

		allRules := append(userRules, MySQLDefaultRules()...)
		engine := NewEngine(allRules)

		result := engine.Apply("foo", "foo", "")
		if result.TargetType != "text" {
			t.Errorf("Apply('foo') TargetType = %q, want %q", result.TargetType, "text")
		}
		if !result.DropTypemod {
			t.Errorf("Apply('foo') DropTypemod should be true")
		}

		// Default MySQL rules still work for types not in user rules
		result = engine.Apply("tinyint", "tinyint(1)", "")
		if result.TargetType != "boolean" {
			t.Errorf("Apply('tinyint') TargetType = %q, want %q", result.TargetType, "boolean")
		}
		if result.Transform != "tinyint-to-bool" {
			t.Errorf("Apply('tinyint') Transform = %q, want %q", result.Transform, "tinyint-to-bool")
		}
	})

	t.Run("user rules override defaults", func(t *testing.T) {
		userRules, err := ParseCastRules([]string{
			"type tinyint to bigint drop typemod",
		})
		if err != nil {
			t.Fatalf("ParseCastRules() error = %v", err)
		}

		allRules := append(userRules, MySQLDefaultRules()...)
		engine := NewEngine(allRules)

		result := engine.Apply("tinyint", "tinyint", "")
		if result.TargetType != "bigint" {
			t.Errorf("Apply('tinyint') TargetType = %q, want %q (should override default 'smallint')", result.TargetType, "bigint")
		}
	})

	t.Run("empty user rules don't affect engine", func(t *testing.T) {
		userRules, err := ParseCastRules(nil)
		if err != nil {
			t.Fatalf("ParseCastRules() error = %v", err)
		}

		allRules := append(userRules, MySQLDefaultRules()...)
		engine := NewEngine(allRules)

		result := engine.Apply("tinyint", "tinyint(1)", "")
		if result.TargetType != "boolean" {
			t.Errorf("Apply('tinyint(1)') TargetType = %q, want %q", result.TargetType, "boolean")
		}
	})
}

package configfile

import (
	"os"
	"reflect"
	"testing"
)

func TestParseMySQLDatabase(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://user:pass@localhost:3306/sakila
     INTO postgresql://localhost/sakila
     WITH include drop, create tables, create indexes, reset sequences, foreign keys
     SET maintenance_work_mem to '128MB', work_mem to '12MB'
     CAST type datetime to timestamptz drop default drop not null using zero-dates-to-null
     BEFORE LOAD DO
     $$ create schema if not exists sakila; $$;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if len(cf.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cf.Commands))
	}

	cmd := cf.Commands[0]
	if cmd.LoadType != SourceMySQL {
		t.Fatalf("expected SourceMySQL, got %v", cmd.LoadType)
	}
	if cmd.SourceURI != "mysql://user:pass@localhost:3306/sakila" {
		t.Fatalf("expected mysql URI, got %q", cmd.SourceURI)
	}
	if cmd.TargetURI != "postgresql://localhost/sakila" {
		t.Fatalf("expected pg URI, got %q", cmd.TargetURI)
	}

	if len(cmd.WITH) != 5 {
		t.Fatalf("expected 5 WITH options, got %d: %v", len(cmd.WITH), cmd.WITH)
	}
	if cmd.WITH[0] != "include drop" {
		t.Fatalf("expected WITH[0]=include drop, got %q", cmd.WITH[0])
	}

	if len(cmd.SET) != 2 {
		t.Fatalf("expected 2 SET options, got %d: %v", len(cmd.SET), cmd.SET)
	}
	if cmd.SET[0] != "maintenance_work_mem to '128MB'" {
		t.Fatalf("expected SET[0]=maintenance_work_mem, got %q", cmd.SET[0])
	}

	if len(cmd.CastRules) != 1 {
		t.Fatalf("expected 1 CAST rule, got %d: %v", len(cmd.CastRules), cmd.CastRules)
	}

	if len(cmd.BeforeLoad) != 1 {
		t.Fatalf("expected 1 BEFORE LOAD, got %d", len(cmd.BeforeLoad))
	}
	if cmd.BeforeLoad[0] != "create schema if not exists sakila;" {
		t.Fatalf("unexpected BEFORE LOAD: %q", cmd.BeforeLoad[0])
	}
}

func TestParseSQLiteURI(t *testing.T) {
	input := `LOAD DATABASE
	     FROM sqlite:///tmp/test.db
	     INTO postgresql://localhost:5432/target

	     WITH include drop, create tables

	     CAST type datetime to timestamptz,
	          type tinyint to smallint

	     INCLUDING ONLY TABLE NAMES MATCHING 'test_%';
	`

	cmds, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if len(cmds.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds.Commands))
	}
	cmd := cmds.Commands[0]
	if cmd.LoadType != SourceSQLite {
		t.Errorf("expected SourceSQLite, got %v", cmd.LoadType)
	}
	if cmd.SourceURI != "sqlite:///tmp/test.db" {
		t.Errorf("expected sqlite:///tmp/test.db, got %s", cmd.SourceURI)
	}
	if len(cmd.IncludingOnly) != 1 || cmd.IncludingOnly[0] != "'test_%'" {
		t.Errorf("expected including ['test_%%'], got %v", cmd.IncludingOnly)
	}
}

func TestParsePgSQLDatabase(t *testing.T) {
	input := `LOAD DATABASE
     FROM pgsql://localhost/pgloader
     INTO postgresql://localhost/copy
     INCLUDING ONLY TABLE NAMES MATCHING ~/geolocations/ in schema 'public'
     WITH include drop, create tables
     BEFORE LOAD DO $$ create schema if not exists copy; $$;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	if cmd.LoadType != SourcePostgreSQL {
		t.Fatalf("expected SourcePostgreSQL, got %v", cmd.LoadType)
	}
	if cmd.SourceURI != "pgsql://localhost/pgloader" {
		t.Fatalf("expected pgsql URI, got %q", cmd.SourceURI)
	}

	if len(cmd.IncludingOnly) < 1 {
		t.Fatalf("expected INCLUDING patterns")
	}
	if len(cmd.WITH) != 2 {
		t.Fatalf("expected 2 WITH options, got %d", len(cmd.WITH))
	}
}

func TestParseCSVLoad(t *testing.T) {
	input := `LOAD CSV
     FROM 'data.csv'
     INTO postgresql://localhost/mydb
     TARGET TABLE my_schema.my_table
     WITH truncate, skip header = 1,
          fields optionally enclosed by '"',
          fields terminated by ','
     SET client_encoding to 'utf8'
     BEFORE LOAD DO $$ drop table if exists my_schema.my_table; $$;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	if cmd.LoadType != SourceCSV {
		t.Fatalf("expected SourceCSV, got %v", cmd.LoadType)
	}
	if cmd.FilePath != "data.csv" {
		t.Fatalf("expected data.csv, got %q", cmd.FilePath)
	}
	if cmd.TargetURI != "postgresql://localhost/mydb" {
		t.Fatalf("expected pg URI, got %q", cmd.TargetURI)
	}
	if cmd.TargetSchema != "my_schema" {
		t.Fatalf("expected my_schema, got %q", cmd.TargetSchema)
	}
	if cmd.TargetTable != "my_table" {
		t.Fatalf("expected my_table, got %q", cmd.TargetTable)
	}

	if len(cmd.WITH) < 4 {
		t.Fatalf("expected 4+ WITH options, got %d: %v", len(cmd.WITH), cmd.WITH)
	}
	if len(cmd.BeforeLoad) != 1 {
		t.Fatalf("expected 1 BEFORE LOAD, got %d", len(cmd.BeforeLoad))
	}
}

func TestParseMultipleCommands(t *testing.T) {
	input := `LOAD CSV FROM 'a.csv' INTO postgresql://localhost/db TARGET TABLE t1 WITH truncate;
LOAD CSV FROM 'b.csv' INTO postgresql://localhost/db TARGET TABLE t2 WITH truncate;`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(cf.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cf.Commands))
	}
	if cf.Commands[0].FilePath != "a.csv" {
		t.Fatalf("expected a.csv, got %q", cf.Commands[0].FilePath)
	}
	if cf.Commands[1].FilePath != "b.csv" {
		t.Fatalf("expected b.csv, got %q", cf.Commands[1].FilePath)
	}
}

func TestBlockComments(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://localhost/db
     INTO postgresql://localhost/db
     /* This is a block comment
        spanning multiple lines */
     WITH include drop;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(cf.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cf.Commands))
	}
}

func TestLineComments(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://localhost/db
     INTO postgresql://localhost/db
     -- This is a line comment
     WITH include drop;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(cf.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cf.Commands))
	}
}

func TestBeforeAfterLoad(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://localhost/db
     INTO postgresql://localhost/db
     WITH include drop
     BEFORE LOAD DO
     $$ create schema if not exists public; $$,
     $$ alter database db set search_path to public; $$
     AFTER LOAD DO
     $$ create index on t(id); $$;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	if len(cmd.BeforeLoad) != 2 {
		t.Fatalf("expected 2 BEFORE LOAD, got %d", len(cmd.BeforeLoad))
	}
	if len(cmd.AfterLoad) != 1 {
		t.Fatalf("expected 1 AFTER LOAD, got %d", len(cmd.AfterLoad))
	}
	if cmd.AfterLoad[0] != "create index on t(id);" {
		t.Fatalf("unexpected AFTER LOAD: %q", cmd.AfterLoad[0])
	}
}

func TestCSVWithInlineColumns(t *testing.T) {
	input := `LOAD CSV
     FROM inline (a, b, c)
     INTO postgresql://localhost/db
     TARGET TABLE public.simple
     WITH truncate;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	if cmd.FilePath != "inline" {
		t.Fatalf("expected inline source, got %q", cmd.FilePath)
	}
	if !reflect.DeepEqual(cmd.SourceColumns, []string{"a", "b", "c"}) {
		t.Fatalf("expected [a b c], got %v", cmd.SourceColumns)
	}
}

func TestMaterializeViews(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://localhost/db
     INTO postgresql://localhost/db
     MATERIALIZE ALL VIEWS
     WITH include drop;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	if len(cmd.MaterializeViews) != 1 || cmd.MaterializeViews[0] != "*" {
		t.Fatalf("expected MATERIALIZE ALL VIEWS, got %v", cmd.MaterializeViews)
	}
}

func TestExcludingTableNames(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://localhost/db
     INTO postgresql://localhost/db
     EXCLUDING TABLE NAMES MATCHING ~/test/, 'temp'
     WITH include drop;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	if len(cmd.Excluding) == 0 {
		t.Fatalf("expected EXCLUDING patterns")
	}
	if len(cmd.Excluding) != 2 {
		t.Fatalf("expected 2 EXCLUDING patterns, got %d: %v", len(cmd.Excluding), cmd.Excluding)
	}
	if cmd.Excluding[0] != "~/test/" {
		t.Fatalf("expected Excluding[0]=~/test/, got %q", cmd.Excluding[0])
	}
	if cmd.Excluding[1] != "'temp'" {
		t.Fatalf("expected Excluding[1]='temp', got %q", cmd.Excluding[1])
	}
}

func TestIncludingOnlyTableNames(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://localhost/db
     INTO postgresql://localhost/db
     INCLUDING ONLY TABLE NAMES MATCHING ~/film/, 'actor', 'customer_%'
     WITH include drop;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	if len(cmd.IncludingOnly) != 3 {
		t.Fatalf("expected 3 INCLUDING patterns, got %d: %v", len(cmd.IncludingOnly), cmd.IncludingOnly)
	}
	if cmd.IncludingOnly[0] != "~/film/" {
		t.Fatalf("expected IncludingOnly[0]=~/film/, got %q", cmd.IncludingOnly[0])
	}
	if cmd.IncludingOnly[1] != "'actor'" {
		t.Fatalf("expected IncludingOnly[1]='actor', got %q", cmd.IncludingOnly[1])
	}
	if cmd.IncludingOnly[2] != "'customer_%'" {
		t.Fatalf("expected IncludingOnly[2]='customer_%%', got %q", cmd.IncludingOnly[2])
	}
}

func TestParseSampleMySQLFile(t *testing.T) {
	data, err := os.ReadFile("../../test/mysql.load")
	if err != nil {
		t.Fatalf("read sample file: %v", err)
	}
	cf, err := ParseFile(string(data))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	cmd := cf.Commands[0]
	if cmd.LoadType != SourceMySQL {
		t.Errorf("expected SourceMySQL, got %v", cmd.LoadType)
	}
	if len(cmd.WITH) == 0 {
		t.Error("expected WITH options")
	}
	if len(cmd.SET) == 0 {
		t.Error("expected SET options")
	}
	if len(cmd.CastRules) == 0 {
		t.Error("expected CAST rules")
	}
	if len(cmd.Excluding) == 0 {
		t.Error("expected EXCLUDING patterns")
	}
	if len(cmd.BeforeLoad) == 0 {
		t.Error("expected BEFORE LOAD")
	}
	if len(cmd.AfterLoad) == 0 {
		t.Error("expected AFTER LOAD")
	}
	if len(cmd.MaterializeViews) == 0 {
		t.Error("expected MATERIALIZE VIEWS")
	}
}

func TestParseSampleSQLiteFile(t *testing.T) {
	data, err := os.ReadFile("../../test/sqlite.load")
	if err != nil {
		t.Fatalf("read sample file: %v", err)
	}
	cf, err := ParseFile(string(data))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	cmd := cf.Commands[0]
	if cmd.LoadType != SourceSQLite {
		t.Errorf("expected SourceSQLite, got %v", cmd.LoadType)
	}
	if len(cmd.WITH) == 0 {
		t.Error("expected WITH options")
	}
	if len(cmd.CastRules) == 0 {
		t.Error("expected CAST rules")
	}
}

func TestParseSamplePgSQLFile(t *testing.T) {
	data, err := os.ReadFile("../../test/pgsql.load")
	if err != nil {
		t.Fatalf("read sample file: %v", err)
	}
	cf, err := ParseFile(string(data))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	cmd := cf.Commands[0]
	if cmd.LoadType != SourcePostgreSQL {
		t.Errorf("expected SourcePostgreSQL, got %v", cmd.LoadType)
	}
	if len(cmd.WITH) == 0 {
		t.Error("expected WITH options")
	}
}

func TestEmptyInput(t *testing.T) {
	_, err := ParseFile("")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestCommentOnlyInput(t *testing.T) {
	_, err := ParseFile("-- just a comment\n/* another */")
	if err == nil {
		t.Fatal("expected error for comment-only input")
	}
}

func TestMissingSemicolon(t *testing.T) {
	_, err := ParseFile("LOAD DATABASE FROM mysql://localhost/db INTO postgresql://localhost/db")
	if err == nil {
		t.Fatal("expected error for missing semicolon")
	}
}

func TestPostgresqlSourceDetection(t *testing.T) {
	input := `LOAD DATABASE
     FROM postgresql://host1/db1
     INTO postgresql://host2/db2
     WITH include drop;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if cf.Commands[0].LoadType != SourcePostgreSQL {
		t.Fatalf("expected SourcePostgreSQL")
	}
}

func TestParseWithCommentsOption(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://root@localhost/sakila
     INTO postgresql://localhost/mydb
     WITH include drop, comments, batch size = 1000;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	foundComments := false
	for _, opt := range cmd.WITH {
		if opt == "comments" {
			foundComments = true
			break
		}
	}
	if !foundComments {
		t.Fatalf("expected 'comments' in WITH options, got %v", cmd.WITH)
	}
}

func TestParseWithNoCommentsOption(t *testing.T) {
	input := `LOAD DATABASE
     FROM mysql://root@localhost/sakila
     INTO postgresql://localhost/mydb
     WITH include drop, no comments;
`

	cf, err := ParseFile(input)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	cmd := cf.Commands[0]
	foundNoComments := false
	for _, opt := range cmd.WITH {
		if opt == "no comments" {
			foundNoComments = true
			break
		}
	}
	if !foundNoComments {
		t.Fatalf("expected 'no comments' in WITH options, got %v", cmd.WITH)
	}
}

func TestParseSampleMSSQLFile(t *testing.T) {
	data, err := os.ReadFile("../../test/mssql.load")
	if err != nil {
		t.Fatalf("read sample file: %v", err)
	}
	cf, err := ParseFile(string(data))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	cmd := cf.Commands[0]
	if cmd.LoadType != SourceMSSQL {
		t.Errorf("expected SourceMSSQL, got %v", cmd.LoadType)
	}
	if len(cmd.WITH) == 0 {
		t.Error("expected WITH options")
	}
	if len(cmd.SET) == 0 {
		t.Error("expected SET options")
	}
	if len(cmd.CastRules) == 0 {
		t.Error("expected CAST rules")
	}
	if len(cmd.Excluding) == 0 {
		t.Error("expected EXCLUDING patterns")
	}
	if len(cmd.BeforeLoad) == 0 {
		t.Error("expected BEFORE LOAD")
	}
	if len(cmd.AfterLoad) == 0 {
		t.Error("expected AFTER LOAD")
	}
}

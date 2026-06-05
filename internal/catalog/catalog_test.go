package catalog

import (
	"testing"
)

func TestTableCommentSQL_无注释返回空(t *testing.T) {
	tbl := &Table{Name: "test", Schema: &Schema{Name: "public"}}
	if sql := tbl.TableCommentSQL(); sql != "" {
		t.Errorf("expected empty string, got %q", sql)
	}
}

func TestTableCommentSQL_生成注释SQL(t *testing.T) {
	tbl := &Table{Name: "actor", Schema: &Schema{Name: "sakila"}, Comment: "Actor information"}
	want := `COMMENT ON TABLE "sakila"."actor" IS 'Actor information';`
	if sql := tbl.TableCommentSQL(); sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestTableCommentSQL_单引号转义(t *testing.T) {
	tbl := &Table{Name: "film", Schema: &Schema{Name: "public"}, Comment: "It's a film"}
	want := `COMMENT ON TABLE "public"."film" IS 'It''s a film';`
	if sql := tbl.TableCommentSQL(); sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestColumnCommentSQL_无注释返回空(t *testing.T) {
	tbl := &Table{Name: "test", Schema: &Schema{Name: "public"}}
	col := &Column{Name: "id"}
	if sql := col.ColumnCommentSQL(tbl); sql != "" {
		t.Errorf("expected empty string, got %q", sql)
	}
}

func TestColumnCommentSQL_生成注释SQL(t *testing.T) {
	tbl := &Table{Name: "actor", Schema: &Schema{Name: "sakila"}}
	col := &Column{Name: "first_name", Comment: "Given name of the actor"}
	want := `COMMENT ON COLUMN "sakila"."actor"."first_name" IS 'Given name of the actor';`
	if sql := col.ColumnCommentSQL(tbl); sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestColumnCommentSQL_表无Schema(t *testing.T) {
	tbl := &Table{Name: "actor"}
	col := &Column{Name: "first_name", Comment: "Given name"}
	want := `COMMENT ON COLUMN "actor"."first_name" IS 'Given name';`
	if sql := col.ColumnCommentSQL(tbl); sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestColumnCommentSQL_单引号转义(t *testing.T) {
	tbl := &Table{Name: "film", Schema: &Schema{Name: "public"}}
	col := &Column{Name: "title", Comment: "Film's title"}
	want := `COMMENT ON COLUMN "public"."film"."title" IS 'Film''s title';`
	if sql := col.ColumnCommentSQL(tbl); sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestEscapeComment_普通字符串不变(t *testing.T) {
	got := escapeComment("hello world")
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestEscapeComment_单引号加倍(t *testing.T) {
	got := escapeComment("it's a test")
	if got != "it''s a test" {
		t.Errorf("expected 'it''s a test', got %q", got)
	}
}

func TestEscapeComment_多个单引号(t *testing.T) {
	got := escapeComment("'hello' and 'world'")
	if got != "''hello'' and ''world''" {
		t.Errorf("expected doubled quotes, got %q", got)
	}
}

func TestEscapeComment_空字符串(t *testing.T) {
	got := escapeComment("")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// CreateTableSQL tests
// ---------------------------------------------------------------------------

func TestCreateTableSQL_MySQLAutoIncrementSkipsExtra(t *testing.T) {
	tbl := &Table{
		Name: "test",
		Schema: &Schema{Name: "public"},
		Columns: []*Column{
			{Name: "id", TypeName: "integer", IsAutoInc: true, Extra: "auto_increment", Default: ""},
			{Name: "name", TypeName: "text"},
		},
	}
	want := `CREATE TABLE "public"."test" (
    "id" integer NOT NULL,
    "name" text NOT NULL
)`
	if sql := tbl.CreateTableSQL(); sql != want {
		t.Errorf("got:\n%s\nwant:\n%s", sql, want)
	}
}

func TestCreateTableSQL_PGIdentityIncludesExtra(t *testing.T) {
	tbl := &Table{
		Name: "users",
		Schema: &Schema{Name: "public"},
		Columns: []*Column{
			{Name: "id", TypeName: "integer", IsAutoInc: true, ExtraDDL: "GENERATED ALWAYS AS IDENTITY", Default: ""},
			{Name: "name", TypeName: "text"},
		},
	}
	want := `CREATE TABLE "public"."users" (
    "id" integer NOT NULL GENERATED ALWAYS AS IDENTITY,
    "name" text NOT NULL
)`
	if sql := tbl.CreateTableSQL(); sql != want {
		t.Errorf("got:\n%s\nwant:\n%s", sql, want)
	}
}

func TestCreateTableSQL_NotAutoIncUsesDefault(t *testing.T) {
	tbl := &Table{
		Name: "t",
		Schema: &Schema{Name: "public"},
		Columns: []*Column{
			{Name: "x", TypeName: "integer", IsAutoInc: false, Default: "42"},
		},
	}
	want := `CREATE TABLE "public"."t" (
    "x" integer NOT NULL DEFAULT 42
)`
	if sql := tbl.CreateTableSQL(); sql != want {
		t.Errorf("got:\n%s\nwant:\n%s", sql, want)
	}
}

func TestCreateTableSQL_MySQLAutoIncrementWithDefaultSkipsDefault(t *testing.T) {
	// MySQL auto_increment: Extra="auto_increment" takes precedence over Default
	tbl := &Table{
		Name: "t",
		Schema: &Schema{Name: "public"},
		Columns: []*Column{
			{Name: "id", TypeName: "bigint", IsAutoInc: true, Extra: "auto_increment", Default: "0"},
		},
	}
	want := `CREATE TABLE "public"."t" (
    "id" bigint NOT NULL DEFAULT 0
)`
	if sql := tbl.CreateTableSQL(); sql != want {
		t.Errorf("got:\n%s\nwant:\n%s", sql, want)
	}
}

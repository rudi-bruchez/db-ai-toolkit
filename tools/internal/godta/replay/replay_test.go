package replay_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/replay"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

const proc = "#godta_r1x_r"

// baseOpts is a usual application's set_options: ANSI_PADDING 1, parallel
// plan 2, CONCAT_NULL_YIELDS_NULL 8, ANSI_WARNINGS 16, ANSI_NULLS 32,
// QUOTED_IDENTIFIER 64, ANSI_NULL_DFLT_ON 128, ARITHABORT 4096.
const baseOpts = 4347

var baseSet = []string{
	"SET ANSI_PADDING ON",
	"SET CONCAT_NULL_YIELDS_NULL ON",
	"SET ANSI_WARNINGS ON",
	"SET ANSI_NULLS ON",
	"SET QUOTED_IDENTIFIER ON",
	"SET ARITHABORT ON",
	"SET NUMERIC_ROUNDABORT OFF",
	"SET DATEFIRST 7",
	"SET DATEFORMAT mdy",
}

func workload(collation string) *model.Workload {
	return &model.Workload{
		Database: model.DBInfo{Name: "godta_db", Collation: collation},
		Tables: map[int]*model.Table{
			1: {ObjectID: 1, Schema: "dbo", Name: "godta_t"},
			2: {ObjectID: 2, Schema: "Contact", Name: "ProspectUS"},
		},
	}
}

func access(db, schema, table string) showplan.Access {
	return showplan.Access{Obj: showplan.ObjRef{Database: db, Schema: schema, Table: table}, LookupOf: -1}
}

func procReplay(decl, body, args string) replay.Replay {
	return replay.Replay{
		Kind:       replay.KindProc,
		Setup:      []string{"CREATE PROCEDURE " + proc + " (" + decl + ") AS " + body},
		Call:       "EXEC " + proc + " " + args,
		Teardown:   []string{"DROP PROCEDURE " + proc},
		SetOptions: baseSet,
	}
}

func adhoc(text string) replay.Replay {
	return replay.Replay{Kind: replay.KindAdhoc, Call: text, SetOptions: baseSet}
}

func locals(call string) replay.Replay {
	return replay.Replay{Kind: replay.KindBatchLocals, Call: call, SetOptions: baseSet}
}

func unreplayable(reason string) replay.Replay {
	return replay.Replay{Kind: replay.KindUnreplayable, Reason: reason}
}

const pspHint = "PLAN PER VALUE(ObjectID = 1, QueryVariantID = 2, predicate_range([x].[y] = @c, 100.0, 1000000.0))"

func TestBuild(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		objectID   int64
		dateFormat int // 1 when zero
		collation  string
		params     []showplan.Param
		accesses   []showplan.Access
		want       replay.Replay
	}{
		{
			name:     "prefix_varchar",
			text:     "(@1 varchar(8000))SELECT COUNT(*) FROM [Contact].[ProspectUS] WHERE [Nom]=@1",
			params:   []showplan.Param{{Name: "@1", DataType: "varchar(8000)", CompiledValue: "'Martin'"}},
			accesses: []showplan.Access{access("godta_db", "Contact", "ProspectUS")},
			want: procReplay("@1 varchar(8000)",
				"SELECT COUNT(*) FROM [Contact].[ProspectUS] WHERE [Nom]=@1", "@1 = 'Martin'"),
		},
		{
			name: "prefix_decimal",
			text: "(@a int,@b decimal(10,2))SELECT 1 WHERE @a = 1 AND @b = 1.5",
			params: []showplan.Param{
				{Name: "@b", DataType: "decimal(10,2)", CompiledValue: "(1.50)"},
				{Name: "@a", DataType: "int", CompiledValue: "(933)"},
			},
			want: procReplay("@a int, @b decimal(10,2)", "SELECT 1 WHERE @a = 1 AND @b = 1.5", "@a = 933, @b = 1.50"),
		},
		{
			name: "batch_locals",
			text: "(@n int)SELECT @n = COUNT(*) FROM Contact.ProspectUS",
			want: locals("DECLARE @n int; SELECT @n = COUNT(*) FROM Contact.ProspectUS"),
		},
		{
			name: "body_declares_its_variable",
			text: "(@s int)DECLARE @s int = (SELECT 1)",
			want: locals("DECLARE @s int = (SELECT 1)"),
		},
		{
			name:   "psp_option_removed",
			text:   "(@c int)SELECT COUNT(*) FROM Marketing.Envoi WHERE CampagneId = @c option (" + pspHint + ")",
			params: []showplan.Param{{Name: "@c", DataType: "int", CompiledValue: "(5)"}},
			want:   procReplay("@c int", "SELECT COUNT(*) FROM Marketing.Envoi WHERE CampagneId = @c", "@c = 5"),
		},
		{
			name: "psp_after_recompile",
			text: "SELECT COUNT(*) FROM Marketing.Envoi WHERE CampagneId = 1 OPTION (RECOMPILE, " + pspHint + ")",
			want: adhoc("SELECT COUNT(*) FROM Marketing.Envoi WHERE CampagneId = 1 OPTION (RECOMPILE)"),
		},
		{
			name: "psp_before_recompile",
			text: "SELECT COUNT(*) FROM Marketing.Envoi WHERE CampagneId = 1 OPTION (" + pspHint + ", RECOMPILE)",
			want: adhoc("SELECT COUNT(*) FROM Marketing.Envoi WHERE CampagneId = 1 OPTION (RECOMPILE)"),
		},
		{
			name: "create_index",
			text: "CREATE INDEX godta_v_cx ON dbo.godta_v(a)",
			want: unreplayable("statement kind: CREATE"),
		},
		{
			name: "restore",
			text: "RESTORE DATABASE x FROM DISK = 'y'",
			want: unreplayable("statement kind: RESTORE"),
		},
		{
			name: "comment_then_with",
			text: "/* c */ WITH s AS (SELECT 1 a) SELECT a FROM s",
			want: adhoc("/* c */ WITH s AS (SELECT 1 a) SELECT a FROM s"),
		},
		{
			name: "semicolon_with",
			text: ";WITH s AS (SELECT 1 a) SELECT a FROM s",
			want: adhoc(";WITH s AS (SELECT 1 a) SELECT a FROM s"),
		},
		{
			name: "parenthesized_union",
			text: "(SELECT 1) UNION ALL (SELECT 2)",
			want: adhoc("(SELECT 1) UNION ALL (SELECT 2)"),
		},
		{
			name: "keyword_in_comment_and_string",
			text: "-- DROP TABLE t\nSELECT 'DROP' /* CREATE */",
			want: adhoc("-- DROP TABLE t\nSELECT 'DROP' /* CREATE */"),
		},
		{
			name: "select_into",
			text: "SELECT a INTO #t FROM b",
			want: unreplayable("select into"),
		},
		{
			name: "output_into_is_not_select_into",
			text: "DELETE FROM dbo.godta_t OUTPUT deleted.a INTO dbo.godta_t (a) WHERE a = 1",
			want: adhoc("DELETE FROM dbo.godta_t OUTPUT deleted.a INTO dbo.godta_t (a) WHERE a = 1"),
		},
		{
			name: "bulk_insert",
			text: "BULK INSERT t FROM 'f'",
			want: unreplayable("statement kind: BULK"),
		},
		{
			name: "declare_then_drop",
			text: "DECLARE @x int; DROP TABLE t",
			want: unreplayable("statement kind: DROP"),
		},
		{
			name: "set_option",
			text: "SET NOCOUNT ON",
			want: unreplayable("statement kind: SET"),
		},
		{
			name: "exec_without_semicolon",
			text: "SELECT 1 EXEC p",
			want: unreplayable("statement kind: EXEC"),
		},
		{
			name:   "datetime_iso",
			text:   "(@d datetime)SELECT 1 WHERE @d > 0",
			params: []showplan.Param{{Name: "@d", DataType: "datetime", CompiledValue: "'2026-11-08 10:11:12.000'"}},
			want:   procReplay("@d datetime", "SELECT 1 WHERE @d > 0", "@d = '2026-11-08T10:11:12.000'"),
		},
		{
			name:   "datetime2_unchanged",
			text:   "(@d datetime2(7))SELECT 1 WHERE @d > '20000101'",
			params: []showplan.Param{{Name: "@d", DataType: "datetime2(7)", CompiledValue: "'2026-11-08 10:11:12.1234567'"}},
			want:   procReplay("@d datetime2(7)", "SELECT 1 WHERE @d > '20000101'", "@d = '2026-11-08 10:11:12.1234567'"),
		},
		{
			name: "params_by_name",
			text: "(@b int,@a int)SELECT 1 WHERE @a = 1 AND @b = 2",
			params: []showplan.Param{
				{Name: "@a", DataType: "int", CompiledValue: "(1)"},
				{Name: "@b", DataType: "int", CompiledValue: "(2)"},
			},
			want: procReplay("@b int, @a int", "SELECT 1 WHERE @a = 1 AND @b = 2", "@b = 2, @a = 1"),
		},
		{
			name:     "procedure_statement",
			text:     "SELECT c FROM dbo.godta_t WHERE a = @a",
			objectID: 5,
			params:   []showplan.Param{{Name: "@a", DataType: "int", CompiledValue: "(933)"}},
			accesses: []showplan.Access{access("godta_db", "dbo", "godta_t")},
			want:     procReplay("@a int", "SELECT c FROM dbo.godta_t WHERE a = @a", "@a = 933"),
		},
		{
			name:     "temp_table",
			text:     "SELECT 1 FROM #tmp",
			accesses: []showplan.Access{access("tempdb", "dbo", "#tmp")},
			want:     unreplayable("temp table"),
		},
		{
			name:     "table_variable",
			text:     "SELECT 1",
			accesses: []showplan.Access{access("", "", "@tv")},
			want:     unreplayable("table variable"),
		},
		{
			name:     "other_database",
			text:     "SELECT 1 FROM other.dbo.godta_t",
			accesses: []showplan.Access{access("other", "dbo", "godta_t")},
			want:     unreplayable("other database: other"),
		},
		{
			name:     "missing_object",
			text:     "SELECT 1 FROM dbo.gone",
			accesses: []showplan.Access{access("godta_db", "dbo", "gone")},
			want:     unreplayable("missing object"),
		},
		{
			name:      "case_insensitive_match",
			text:      "SELECT 1 FROM dbo.GODTA_T",
			collation: "SQL_Latin1_General_CP1_CI_AS",
			accesses:  []showplan.Access{access("godta_db", "DBO", "GODTA_T")},
			want:      adhoc("SELECT 1 FROM dbo.GODTA_T"),
		},
		{
			name:      "case_sensitive_mismatch",
			text:      "SELECT 1 FROM dbo.GODTA_T",
			collation: "Latin1_General_CS_AS",
			accesses:  []showplan.Access{access("godta_db", "dbo", "GODTA_T")},
			want:      unreplayable("missing object"),
		},
		{
			name:      "binary_collation_mismatch",
			text:      "SELECT 1 FROM dbo.GODTA_T",
			collation: "Latin1_General_BIN2",
			accesses:  []showplan.Access{access("godta_db", "dbo", "GODTA_T")},
			want:      unreplayable("missing object"),
		},
		{
			name:       "unknown_date_format",
			text:       "SELECT 1",
			dateFormat: 7,
			want:       unreplayable("unknown date_format 7"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			df := c.dateFormat
			if df == 0 {
				df = 1
			}
			collation := c.collation
			if collation == "" {
				collation = "SQL_Latin1_General_CP1_CI_AS"
			}
			q := model.Query{Text: c.text, ObjectID: c.objectID, SetOptions: baseOpts, DateFirst: 7, DateFormat: df}
			st := &showplan.Statement{Params: c.params, Accesses: c.accesses}
			got := replay.Build(q, st, workload(collation), proc)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Build(%q)\n got %#v\nwant %#v", c.text, got, c.want)
			}
		})
	}
}

func TestDateFormats(t *testing.T) {
	// Measured on 2022 through sys.dm_exec_plan_attributes, each SET DATEFORMAT
	// in its own batch: mdy 1, dmy 2, ymd 3, ydm 4, myd 5, dym 6.
	for n, f := range map[int]string{1: "mdy", 2: "dmy", 3: "ymd", 4: "ydm", 5: "myd", 6: "dym"} {
		q := model.Query{Text: "SELECT 1", SetOptions: baseOpts, DateFirst: 1, DateFormat: n}
		got := replay.Build(q, &showplan.Statement{}, workload("Latin1_General_CI_AS"), proc).SetOptions
		if len(got) < 2 || got[len(got)-2] != "SET DATEFIRST 1" || got[len(got)-1] != "SET DATEFORMAT "+f {
			t.Errorf("date_format %d: SetOptions %v, want ... SET DATEFIRST 1, SET DATEFORMAT %s", n, got, f)
		}
	}
}

// TestUnknownDateFirst: the plan cache gives 0 when the attribute is missing,
// and SET DATEFIRST 0 fails.
func TestUnknownDateFirst(t *testing.T) {
	q := model.Query{Text: "SELECT 1", SetOptions: baseOpts, DateFirst: 0, DateFormat: 1}
	got := replay.Build(q, &showplan.Statement{}, workload("Latin1_General_CI_AS"), proc)
	if want := unreplayable("unknown date_first 0"); !reflect.DeepEqual(got, want) {
		t.Errorf("Build with date_first 0: %#v, want %#v", got, want)
	}
}

func TestSetStatements(t *testing.T) {
	want := []string{
		"SET ANSI_PADDING ON",
		"SET CONCAT_NULL_YIELDS_NULL ON",
		"SET ANSI_WARNINGS ON",
		"SET ANSI_NULLS ON",
		"SET QUOTED_IDENTIFIER ON",
		"SET ARITHABORT ON",
		"SET NUMERIC_ROUNDABORT OFF",
	}
	if got := replay.SetStatements(4219); !reflect.DeepEqual(got, want) {
		t.Errorf("SetStatements(4219)\n got %v\nwant %v", got, want)
	}
	// Every option off, and NUMERIC_ROUNDABORT on alone.
	got := replay.SetStatements(8192)
	if len(got) != 7 {
		t.Fatalf("SetStatements(8192) has %d statements, want 7: %v", len(got), got)
	}
	for i, s := range got {
		w := " OFF"
		if i == 6 {
			w = " ON"
		}
		if !strings.HasSuffix(s, w) {
			t.Errorf("SetStatements(8192)[%d] = %q, want it to end with %q", i, s, w)
		}
	}
}

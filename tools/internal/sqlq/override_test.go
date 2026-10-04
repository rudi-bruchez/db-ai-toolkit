package sqlq

import (
	"strings"
	"testing"

	"github.com/golang-sql/civil"
)

const hostScript = "SET NOCOUNT ON;\nDECLARE @hostname sysname = N'%';\nSELECT host_name FROM sys.dm_exec_sessions WHERE host_name LIKE @hostname;\n"

func TestDeclareOverrideBindsNotConcatenates(t *testing.T) {
	ps, err := AnalyseOverrides(hostScript, []string{"hostname"})
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Type.String() != "sysname" || ps[0].Default != "N'%'" {
		t.Errorf("param = %+v", ps[0])
	}
	got := Rewrite(hostScript, ps, map[string]bool{"hostname": true})
	want := strings.Replace(hostScript, "= N'%';", "= @sqlq_hostname;", 1)
	if got != want {
		t.Errorf("Rewrite =\n%s\nwant\n%s", got, want)
	}
	if Rewrite(hostScript, ps, map[string]bool{}) != hostScript {
		t.Errorf("a parameter not passed must leave the text untouched")
	}
}

func TestDeclareOverrideKeepsOffsetsWithAccents(t *testing.T) {
	src := "-- sessions de l'hôte, données à jour\nDECLARE @h sysname = N'é%';\nSELECT @h;"
	ps, err := AnalyseOverrides(src, []string{"h"})
	if err != nil {
		t.Fatal(err)
	}
	if got := Rewrite(src, ps, map[string]bool{"h": true}); !strings.Contains(got, "DECLARE @h sysname = @sqlq_h;\nSELECT @h;") {
		t.Errorf("Rewrite = %q", got)
	}
}

func TestOverrideKeepsCRLF(t *testing.T) {
	src := "DECLARE @n int = 20;\r\nSELECT TOP (@n) name FROM sys.objects;\r\n"
	ps, err := AnalyseOverrides(src, []string{"n"})
	if err != nil {
		t.Fatal(err)
	}
	if got := Rewrite(src, ps, map[string]bool{"n": true}); got != "DECLARE @n int = @sqlq_n;\r\nSELECT TOP (@n) name FROM sys.objects;\r\n" {
		t.Errorf("Rewrite = %q", got)
	}
}

func TestEmptyStringDefaultIsAccepted(t *testing.T) {
	if _, err := AnalyseOverrides("DECLARE @indexName sysname = '';\nSELECT @indexName;", []string{"indexname"}); err != nil {
		t.Errorf("'' is a value, not an empty initializer: %v", err)
	}
	if _, err := AnalyseOverrides("DECLARE @x int = ;\nSELECT @x;", []string{"x"}); err == nil {
		t.Errorf("a truly empty initializer must be refused")
	}
}

func TestDeclareLineMustStandAlone(t *testing.T) {
	cases := []struct{ name, src, variable string }{
		{"statement after", "DECLARE @n int = 20 SELECT TOP (@n) name FROM sys.objects;", "n"},
		{"no semicolon", "DECLARE @n int = 20\nSELECT @n;", "n"},
		{"two lines", "DECLARE @p nvarchar(200) = N'%'\n  + N'Orders%';\nSELECT @p;", "p"},
		{"case on two lines", "DECLARE @d int = CASE\n WHEN 1 = 1 THEN 1 END;\nSELECT @d;", "d"},
		{"literal semicolon", "DECLARE @p varchar(10) = 'a;b' SELECT 2;", "p"},
		{"multi variable", "DECLARE @p int = 1, @q int = 2;\nSELECT @p, @q;", "p"},
		{"code before", "SELECT 1; DECLARE @p int = 1;\nSELECT @p;", "p"},
		{"not declared", "SELECT @p;", "p"},
		{"declared twice", "DECLARE @p int = 1;\nDECLARE @P int = 2;\nSELECT @p;", "p"},
		{"no initializer", "DECLARE @p int;\nSELECT @p;", "p"},
	}
	for _, c := range cases {
		if _, err := AnalyseOverrides(c.src, []string{c.variable}); err == nil {
			t.Errorf("%s: %q accepted", c.name, c.src)
		}
	}
}

func TestCompoundAssignmentIsRejected(t *testing.T) {
	_, err := AnalyseOverrides("DECLARE @p int = 1;\nSET @p += 2;\nSELECT @p;", []string{"p"})
	if err == nil || !strings.Contains(err.Error(), "assigned at line 2") {
		t.Errorf("err = %v", err)
	}
}

func TestSelectAssignmentIsRejected(t *testing.T) {
	for _, src := range []string{
		"DECLARE @db sysname = N'%';\nSELECT TOP (1) @db = name FROM sys.databases;\nSELECT @db;",
		"DECLARE @db sysname = N'%';\nDECLARE @x int = 0;\nSELECT @x = 1, @db = N'master';",
		"DECLARE @db sysname = N'%';\nSET @db = N'x';",
	} {
		if _, err := AnalyseOverrides(src, []string{"db"}); err == nil {
			t.Errorf("assignment accepted: %q", src)
		}
	}
}

func TestComparisonIsAccepted(t *testing.T) {
	src := "DECLARE @online bit = 1;\nSELECT IIF(@online = 1, 'ON', 'OFF');\nSELECT name FROM sys.objects WHERE @online = 1 AND 1 = 1;\nIF @online = 0 SELECT 0;"
	if _, err := AnalyseOverrides(src, []string{"online"}); err != nil {
		t.Errorf("comparisons refused: %v", err)
	}
}

func TestRewriteOrderIndependentOfMarker(t *testing.T) {
	src := "DECLARE @a int = 1;\nDECLARE @b int = 2;\nSELECT @a, @b;"
	want := "DECLARE @a int = @sqlq_a;\nDECLARE @b int = @sqlq_b;\nSELECT @a, @b;"
	// Both orders: "b,a" is already the descending order Rewrite needs, so on
	// its own it would pass without any sort.
	for _, names := range [][]string{{"b", "a"}, {"a", "b"}} {
		ps, err := AnalyseOverrides(src, names)
		if err != nil {
			t.Fatal(err)
		}
		if got := Rewrite(src, ps, map[string]bool{"a": true, "b": true}); got != want {
			t.Errorf("marker %v: Rewrite = %q, want %q", names, got, want)
		}
	}
}

func TestUnbalancedInitializerIsRejected(t *testing.T) {
	for _, src := range []string{"DECLARE @p int = (1 + 2));\nSELECT @p;", "DECLARE @p int = ((1 + 2);\nSELECT @p;"} {
		if _, err := AnalyseOverrides(src, []string{"p"}); err == nil {
			t.Errorf("accepted %q", src)
		}
	}
}

func TestOutputTargetIsRejected(t *testing.T) {
	if _, err := AnalyseOverrides("DECLARE @p int = 1;\nSELECT @p OUTPUT;", []string{"p"}); err == nil {
		t.Error("@p OUTPUT accepted")
	}
}

func TestReservedPrefixIsRejected(t *testing.T) {
	if _, err := AnalyseOverrides("DECLARE @p int = 1;\nDECLARE @sqlq_p int = 2;\nSELECT @p;", []string{"p"}); err == nil {
		t.Errorf("@sqlq_ identifiers must be refused")
	}
}

func TestUnsupportedTypeIsRejected(t *testing.T) {
	_, err := AnalyseOverrides("DECLARE @r decimal(5,1) = 1.2;\nSELECT @r;", []string{"r"})
	if err == nil || !strings.Contains(err.Error(), "decimal") {
		t.Errorf("err = %v", err)
	}
}

func TestParameterNamesAreCaseInsensitive(t *testing.T) {
	if _, err := AnalyseOverrides("DECLARE @HostName sysname = N'%';\nSELECT @HOSTNAME;", []string{"hostname"}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestParamValueValidatedByType(t *testing.T) {
	ok := []struct {
		typ  ParamType
		in   string
		want any
	}{
		{ParamType{"sysname", 128}, "SRV-APP01", "SRV-APP01"},
		{ParamType{"nvarchar", -1}, strings.Repeat("é", 5000), strings.Repeat("é", 5000)},
		{ParamType{"varchar", 10}, "ROW", "ROW"},
		{ParamType{"int", 0}, "-42", int64(-42)},
		{ParamType{"tinyint", 0}, "255", int64(255)},
		{ParamType{"bit", 0}, "TRUE", true},
		{ParamType{"date", 0}, "2026-10-04", civil.Date{Year: 2026, Month: 10, Day: 4}},
		{ParamType{"datetime2", 0}, "2026-10-04T10:30", civil.DateTime{Date: civil.Date{Year: 2026, Month: 10, Day: 4}, Time: civil.Time{Hour: 10, Minute: 30}}},
	}
	for _, c := range ok {
		got, err := BindValue(c.typ, c.in)
		if err != nil || got != c.want {
			t.Errorf("BindValue(%v, %q) = %v, %v; want %v", c.typ, c.in, got, err, c.want)
		}
	}
	bad := []struct {
		typ ParamType
		in  string
	}{
		{ParamType{"varchar", 10}, "COLUMNSTORE_ARCHIVE"},
		{ParamType{"varchar", 10}, "été"},
		{ParamType{"nchar", 3}, "abcd"},
		{ParamType{"tinyint", 0}, "256"},
		{ParamType{"int", 0}, "1.5"},
		{ParamType{"bit", 0}, "2"},
		{ParamType{"date", 0}, "04/10/2026"},
		{ParamType{"datetime", 0}, "2026-10-04 10:30"},
		{ParamType{"nvarchar", 1}, "😀"},
		{ParamType{"smalldatetime", 0}, "2026-10-04T10:30:30"},
	}
	for _, c := range bad {
		if _, err := BindValue(c.typ, c.in); err == nil {
			t.Errorf("BindValue(%v, %q) accepted", c.typ, c.in)
		}
	}
}

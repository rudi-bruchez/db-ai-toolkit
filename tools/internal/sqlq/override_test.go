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

// Defects found by the code review of task 5. Each case was proven on a real
// SQL Server to change or lose the bound value.

func TestAssignmentInsideParenthesesIsRejected(t *testing.T) {
	for _, src := range []string{
		"DECLARE @p int = 1;\n(SELECT @p = 2);\nSELECT @p;",
		"DECLARE @p int = 1;\nSELECT 1 UNION (SELECT @p = 2);\nSELECT @p;",
		"DECLARE @p int = 1;\nIF EXISTS (SELECT @p = 2) SELECT 'y';\nSELECT @p;",
		"DECLARE @p int = 1;\nSELECT x FROM (VALUES(1)) v(x) ORDER BY (SELECT @p = 2);\nSELECT @p;",
	} {
		if _, err := AnalyseOverrides(src, []string{"p"}); err == nil {
			t.Errorf("assignment accepted: %q", src)
		}
	}
	ok := "DECLARE @p int = 1;\nSELECT name FROM sys.objects WHERE (@p = 1) OR object_id = 3;\nSELECT CASE WHEN @p = 1 THEN 1 END;"
	if _, err := AnalyseOverrides(ok, []string{"p"}); err != nil {
		t.Errorf("comparison refused: %v", err)
	}
}

func TestVariableGluedToNumberIsRejected(t *testing.T) {
	for _, src := range []string{
		"DECLARE @p int = 1;\nSELECT TOP 1@p = object_id FROM sys.objects;\nSELECT @p;",
		"DECLARE @p int = 1;\nSELECT TOP 1@P = object_id FROM sys.objects;\nSELECT @p;",
	} {
		if _, err := AnalyseOverrides(src, []string{"p"}); err == nil {
			t.Errorf("glued assignment accepted: %q", src)
		}
	}
	// x@p is one identifier for SQL Server too.
	if _, err := AnalyseOverrides("DECLARE @p int = 1;\nSELECT x@p FROM t WHERE c = @p;", []string{"p"}); err != nil {
		t.Errorf("identifier x@p refused: %v", err)
	}
}

func TestInitializerMustBeOneExpression(t *testing.T) {
	for _, init := range []string{
		"1 GOTO x", "1 (SELECT 2 AS two)", "1 CHECKPOINT", "1 WAITFOR DELAY '00:00:01'",
		"1 COMMIT", "1 OPEN c", "1 BREAK", "'a' COMMIT", "N'a' COMMIT", "1 'a'",
		"CASE WHEN 1 = 1 THEN 1 END", "N'a' COLLATE Latin1_General_BIN", "1 +", "1 = 1",
		"f (1)", "1(2)", "1.5(SELECT 2)", "x.(1)", "1 ~ 2",
	} {
		src := "DECLARE @p int = " + init + ";\nSELECT @p;"
		if _, err := AnalyseOverrides(src, []string{"p"}); err == nil {
			t.Errorf("initializer %q accepted", init)
		}
	}
	for _, init := range []string{
		"N'%'", "''", "'%PROFIL%'", "-1", "100", "NULL", "@@SPID", "~0", "0x1F", "1.5", ".5",
		"DATEADD(hour, -@LookbackHours, GETDATE())",
		"DATEPART(hour,@RunDateStart)*10000 + DATEPART(minute,@RunDateStart)*100 + DATEPART(second,@RunDateStart)",
		"CONVERT(INT, CONVERT(VARCHAR(8), @RunDateStart, 112))",
		"dbo.f(1) + (1 + 2) * - 3", "N'a' + N'b' /* why */", "[x]",
	} {
		src := "DECLARE @p int = " + init + ";\nSELECT @p;"
		if _, err := AnalyseOverrides(src, []string{"p"}); err != nil {
			t.Errorf("initializer %q refused: %v", init, err)
		}
	}
}

func TestConditionalDeclareIsRejected(t *testing.T) {
	for _, src := range []string{
		"IF 1 = 0\nDECLARE @p int = 5;\nSELECT @p;",
		"IF 1 = 1 SELECT 1 ELSE\nDECLARE @p int = 5;\nSELECT @p;",
		"WHILE 1 = 0\nDECLARE @p int = 5;\nSELECT @p;",
		"BEGIN TRY\nDECLARE @p int = 5;\nSELECT @p;\nEND TRY BEGIN CATCH END CATCH",
		"GOTO x;\nDECLARE @p int = 5;\nx:\nSELECT @p;",
		"x:\nDECLARE @p int = 5;\nSELECT @p;",
	} {
		if _, err := AnalyseOverrides(src, []string{"p"}); err == nil {
			t.Errorf("conditional declaration accepted: %q", src)
		}
	}
}

func TestFractionalSecondsAreRejected(t *testing.T) {
	for _, c := range []struct{ typ, in string }{
		{"datetime", "2020-12-31T23:59:59.999"},
		{"datetime2", "2020-12-31T23:59:59.5"},
		{"datetime2", "2020-12-31T23:59:59,5"},
		{"datetime", "2020-12-31T23:59:59.000"},
	} {
		if _, err := BindValue(ParamType{Base: c.typ}, c.in); err == nil {
			t.Errorf("BindValue(%s, %q) accepted", c.typ, c.in)
		}
	}
}

func TestDateOutsideTypeRangeIsRejected(t *testing.T) {
	bad := []struct{ typ, in string }{
		{"date", "0000-01-01"}, {"datetime2", "0000-12-31T10:00"}, {"datetime", "1752-12-31"},
		{"smalldatetime", "1899-12-31T23:59"}, {"smalldatetime", "2079-06-07"},
	}
	for _, c := range bad {
		if _, err := BindValue(ParamType{Base: c.typ}, c.in); err == nil {
			t.Errorf("BindValue(%s, %q) accepted", c.typ, c.in)
		}
	}
	good := []struct{ typ, in string }{
		{"date", "0001-01-01"}, {"datetime2", "0001-01-01T00:00"}, {"datetime", "1753-01-01"},
		{"smalldatetime", "1900-01-01"}, {"smalldatetime", "2079-06-06T23:59"}, {"date", "9999-12-31"},
	}
	for _, c := range good {
		if _, err := BindValue(ParamType{Base: c.typ}, c.in); err != nil {
			t.Errorf("BindValue(%s, %q) refused: %v", c.typ, c.in, err)
		}
	}
}

func TestNonASCIIVariableNamesAreRejected(t *testing.T) {
	cases := []struct{ src, name string }{
		{"DECLARE @p int = 1;\nSET @ｐ = 7;\nSELECT @p;", "p"},
		{"DECLARE @strasse int = 1;\nSET @straße = 2;\nSELECT @strasse;", "strasse"},
		{"DECLARE @straße int = 1;\nSELECT @straße;", "straße"},
	}
	for _, c := range cases {
		if _, err := AnalyseOverrides(c.src, []string{c.name}); err == nil {
			t.Errorf("accepted %q for %s", c.src, c.name)
		}
	}
}

func TestDeclareWithAsIsAccepted(t *testing.T) {
	src := "DECLARE @StartTime  as datetime2 = '2023-02-26 06:00:00';\nSELECT @StartTime;"
	ps, err := AnalyseOverrides(src, []string{"starttime"})
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Type.Base != "datetime2" {
		t.Errorf("type = %v", ps[0].Type)
	}
	if got := Rewrite(src, ps, map[string]bool{"starttime": true}); got != "DECLARE @StartTime  as datetime2 = @sqlq_starttime;\nSELECT @StartTime;" {
		t.Errorf("Rewrite = %q", got)
	}
}

func TestBracketedTypeIsNamedInTheRefusal(t *testing.T) {
	_, err := AnalyseOverrides("DECLARE @p [int] = 1;\nSELECT @p;", []string{"p"})
	if err == nil || !strings.Contains(err.Error(), "bracket") {
		t.Errorf("err = %v", err)
	}
}

func TestOverrideSeesWhatTheServerSees(t *testing.T) {
	for _, src := range []string{
		"DECLARE @p int = 1; -- c\rSELECT @p = 99\nSELECT @p AS v;",
		"-- c\rIF 1=0\nDECLARE @p int = 5;\nSELECT @p AS v;",
		"SELECT 1IF 1=0\nDECLARE @p int = 5;\nSELECT @p AS v;",
		"SELECT 1WHILE 1=0\nDECLARE @p int = 5;\nSELECT @p AS v;",
		"DECLARE @p int = 1RETURN;\nSELECT @p AS v;",
		"DECLARE @p int = 0xRETURN;\nSELECT @p AS v;",
	} {
		if _, err := AnalyseOverrides(src, []string{"p"}); err == nil {
			t.Errorf("accepted: %q", src)
		}
	}
}

func TestOverrideRefusalQuotesNoIdentifier(t *testing.T) {
	for _, src := range []string{
		"DECLARE @p int = 1;\nSELECT @sqlq_secret_path;",
		"DECLARE @p int = 1;\nSELECT @straße_secret;",
		"DECLARE @p int = secret_word 1;",
	} {
		_, err := AnalyseOverrides(src, []string{"p"})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: err = %v", src, err)
		}
	}
}

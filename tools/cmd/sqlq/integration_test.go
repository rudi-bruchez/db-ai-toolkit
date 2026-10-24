package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// testProfile loads the profile named by $SQLQ_TEST_PROFILE from the file named
// by $SQLQ_TEST_PROFILES, or skips. The profile must be readonly: these tests
// create nothing.
func testProfile(t *testing.T) (sqlq.Profile, sqlq.SecretResolver) {
	t.Helper()
	path, name := os.Getenv("SQLQ_TEST_PROFILES"), os.Getenv("SQLQ_TEST_PROFILE")
	if path == "" || name == "" {
		t.Skip("SQLQ_TEST_PROFILES and SQLQ_TEST_PROFILE not set; no instance to test against")
	}
	profiles, err := sqlq.LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := profiles.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ReadOnly() {
		t.Fatalf("profile %q must be readonly for the integration tests", name)
	}
	// Never the user's real layers: no clone, a throwaway registry and query dir.
	t.Setenv("DB_AI_TOOLKIT_TSQL_SCRIPTS", "")
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", filepath.Join(t.TempDir(), "verified.json"))
	t.Setenv("DB_AI_TOOLKIT_QUERIES", t.TempDir())
	r := sqlq.NewResolver(os.Getenv, "", func(string) {})
	return p, r.Resolve
}

func TestEveryResultSetIsReturned(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; SELECT 2 AS b UNION ALL SELECT 3;", nil,
		options{maxRows: 1, timeoutSec: 120}, resolve)
	if code != exitOK || res.Error != nil {
		t.Fatalf("code %d, error %+v", code, res.Error)
	}
	if len(res.Rows) != 1 || res.Rows[0]["a"] != int64(1) {
		t.Errorf("first set: %+v", res.Rows)
	}
	if len(res.MoreResults) != 1 {
		t.Fatalf("want 1 extra set, got %d", len(res.MoreResults))
	}
	extra := res.MoreResults[0]
	if extra.RowCount != 2 || !extra.Truncated || len(extra.Rows) != 1 {
		t.Errorf("-maxrows applies per set: %+v", extra)
	}
}

func TestMessagesAreCaptured(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "PRINT 'hello from print'; RAISERROR('low severity', 10, 1); SELECT 1 AS a;", nil,
		options{maxRows: 50, timeoutSec: 120}, resolve)
	if code != exitOK {
		t.Fatalf("code %d, error %+v", code, res.Error)
	}
	joined := strings.Join(res.Messages, "\n")
	if !strings.Contains(joined, "hello from print") || !strings.Contains(joined, "low severity") {
		t.Errorf("messages = %q", res.Messages)
	}
}

func TestErrorAfterFirstSetKeepsRows(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; SELECT 1/0 AS b;", nil,
		options{maxRows: 50, timeoutSec: 120}, resolve)
	if code != exitSQL || res.Error == nil || res.Error.Number != 8134 {
		t.Fatalf("want exit 2 with error 8134, got %d %+v", code, res.Error)
	}
	if len(res.Rows) != 1 {
		t.Errorf("rows read before the error must be kept: %+v", res.Rows)
	}
}

func TestTimeoutIsAnErrorNotPartialSuccess(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; WAITFOR DELAY '00:00:05'; SELECT 2 AS b;", nil,
		options{maxRows: 5, timeoutSec: 2}, resolve)
	if code == exitOK || res.Error == nil {
		t.Errorf("a timed-out batch must fail, got code %d", code)
	}
}

func TestManyMessagesDoNotHang(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "SELECT 1 AS a;\n" + strings.Repeat("PRINT 'm';\n", 40) + "SELECT 2 AS b;"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 120}, resolve)
	if code != exitOK || len(res.Messages) != 40 || len(res.MoreResults) != 1 {
		t.Errorf("code %d, %d messages, %d extra sets, error %+v", code, len(res.Messages), len(res.MoreResults), res.Error)
	}
}

// The driver's message queue holds 15 entries and is filled from the goroutine
// that reads rows. More than 15 notices inside one result set, here one STATISTICS
// IO line per table, must not stall the query while collect sits in scanAll.
func TestManyNoticesInsideOneSetDoNotHang(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "SET STATISTICS IO ON; SELECT (SELECT COUNT(*) FROM sys.objects) + (SELECT COUNT(*) FROM sys.columns) + " +
		"(SELECT COUNT(*) FROM sys.indexes) + (SELECT COUNT(*) FROM sys.index_columns) + (SELECT COUNT(*) FROM sys.partitions) + " +
		"(SELECT COUNT(*) FROM sys.allocation_units) + (SELECT COUNT(*) FROM sys.types) + (SELECT COUNT(*) FROM sys.schemas) + " +
		"(SELECT COUNT(*) FROM sys.sql_modules) + (SELECT COUNT(*) FROM sys.foreign_keys) + (SELECT COUNT(*) FROM sys.database_principals) + " +
		"(SELECT COUNT(*) FROM sys.database_permissions) + (SELECT COUNT(*) FROM sys.xml_schema_collections) + " +
		"(SELECT COUNT(*) FROM sys.fulltext_catalogs) + (SELECT COUNT(*) FROM sys.assemblies) + (SELECT COUNT(*) FROM sys.certificates) + " +
		"(SELECT COUNT(*) FROM sys.symmetric_keys) + (SELECT COUNT(*) FROM sys.asymmetric_keys) AS total;"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 10}, resolve)
	if code != exitOK || res.Error != nil {
		t.Fatalf("code %d, error %+v", code, res.Error)
	}
	if len(res.Rows) != 1 || len(res.Messages) < 16 {
		t.Errorf("%d rows, %d messages, want 1 row and at least 16 messages", len(res.Rows), len(res.Messages))
	}
}

// A parameterized batch runs under sp_executesql, where a PRINT emits no DONE
// token, so the notices pile up while collect waits in rows.NextResultSet for
// the next statement. The 16th must not stall the query either.
func TestManyNoticesInAParameterizedBatchDoNotHang(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "SELECT 1 AS a;\n" + strings.Repeat("PRINT 'm';\n", 20) + "SELECT @p AS b;"
	res, code := execute(p, batch, []any{sql.Named("p", 1)}, options{maxRows: 5, timeoutSec: 10}, resolve)
	if code != exitOK || res.Error != nil {
		t.Fatalf("code %d, error %+v", code, res.Error)
	}
	if len(res.Messages) != 20 || len(res.MoreResults) != 1 {
		t.Errorf("%d messages, %d extra sets, want 20 and 1", len(res.Messages), len(res.MoreResults))
	}
}

func TestSetCutShortByErrorIsIncomplete(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; SELECT n, 10/(4-n) AS x FROM (VALUES(1),(2),(3),(4),(5)) v(n); "+
		"SELECT 3 AS c; RAISERROR('late', 16, 1); SELECT 1/0 AS d;", nil, options{maxRows: 50, timeoutSec: 120}, resolve)
	if code != exitSQL || res.Error == nil || res.Error.Number != 8134 {
		t.Fatalf("want exit 2 with error 8134, got %d %+v", code, res.Error)
	}
	if res.Incomplete || len(res.MoreResults) != 3 {
		t.Fatalf("first set complete, 3 more: incomplete=%v more=%+v", res.Incomplete, res.MoreResults)
	}
	got := []bool{res.MoreResults[0].Incomplete, res.MoreResults[1].Incomplete, res.MoreResults[2].Incomplete}
	if got[0] != true || got[1] != false || got[2] != true {
		t.Errorf("incomplete per set = %v, want [true false true]", got)
	}
	// An error after a closed set (the RAISERROR after c) must not mark it.
	if joined := strings.Join(res.Messages, "\n"); !strings.Contains(joined, "late") || !strings.Contains(joined, "error 8134") {
		t.Errorf("later errors must be in messages: %q", res.Messages)
	}
}

func TestOverrideBindsTypedValuesOnServer(t *testing.T) {
	p, resolve := testProfile(t)
	src := "-- When\n-- sqlq: name=when params=d,n\nSET DATEFORMAT ydm;\nDECLARE @d datetime = '2000-01-01';\nDECLARE @n varchar(10) = 'x';\nSELECT CONVERT(char(10), @d, 23) AS d, @n AS n;\n"
	ps, err := sqlq.AnalyseOverrides(src, []string{"d", "n"})
	if err != nil {
		t.Fatal(err)
	}
	e := sqlq.Entry{Name: "when", Source: sqlq.SourceTsqlScripts, SQL: src, Overrides: ps}
	text, args, _, err := prepareSaved(e, paramList{"d=2026-10-04", "n=ROW"})
	if err != nil {
		t.Fatal(err)
	}
	res, code := execute(p, text, args, options{maxRows: 5, timeoutSec: 120}, resolve)
	if code != exitOK || len(res.Rows) != 1 || res.Rows[0]["d"] != "2026-10-04" || res.Rows[0]["n"] != "ROW" {
		t.Errorf("code %d rows %+v error %+v", code, res.Rows, res.Error)
	}
}

func TestNothingIsSavedWhenTheRunFailed(t *testing.T) {
	p, _ := testProfile(t)
	q := t.TempDir()
	t.Setenv("DB_AI_TOOLKIT_QUERIES", q)
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", filepath.Join(t.TempDir(), "v.json"))
	o := options{profileName: p.Name, profilesPath: os.Getenv("SQLQ_TEST_PROFILES"), query: "SELECT 1/0 AS x;",
		saveQuery: "will-fail", summary: "Fails.", maxRows: 5, timeoutSec: 120, queriesDir: "../../internal/sqlq/testdata/catalog/bundled"}
	if _, code := captureRun(t, o); code != exitSQL {
		t.Fatalf("code %d", code)
	}
	if matches, _ := filepath.Glob(filepath.Join(q, "profiles", "*", "will-fail.sql")); len(matches) != 0 {
		t.Errorf("a failed run left %v", matches)
	}
}

func TestSaveWritesVerifiedEntry(t *testing.T) {
	p, _ := testProfile(t)
	q := t.TempDir()
	t.Setenv("DB_AI_TOOLKIT_QUERIES", q)
	reg := filepath.Join(t.TempDir(), "v.json")
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", reg)
	o := options{profileName: p.Name, profilesPath: os.Getenv("SQLQ_TEST_PROFILES"),
		query: "SELECT TOP (1) name FROM sys.objects WHERE name LIKE @pattern;", params: paramList{"pattern=sys%"},
		saveQuery: "objects-like", summary: "Objects matching a pattern.", maxRows: 5, timeoutSec: 120, queriesDir: "../../internal/sqlq/testdata/catalog/bundled"}
	if out, code := captureRun(t, o); code != exitOK {
		t.Fatalf("code %d: %s", code, out)
	}
	o2 := options{listQueries: true, profileName: p.Name, profilesPath: os.Getenv("SQLQ_TEST_PROFILES"), queriesDir: o.queriesDir}
	out, _ := captureRun(t, o2)
	var cat struct {
		Queries []struct {
			Name, Source, Rejected string
			Verified               *sqlq.Verified
		} `json:"queries"`
	}
	if err := json.Unmarshal([]byte(out), &cat); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, q := range cat.Queries {
		if q.Name == "objects-like" {
			found = true
			if q.Source != "personal" || q.Rejected != "" || q.Verified == nil {
				t.Errorf("saved entry: %+v", q)
			}
		}
	}
	if !found {
		t.Errorf("saved entry missing: %s", out)
	}
}

func TestLaterErrorsKeepTheirPlaceInMessages(t *testing.T) {
	p, resolve := testProfile(t)
	res, _ := execute(p, "PRINT 'p1'; RAISERROR('e16', 16, 1); SELECT 1/0 AS x; PRINT 'p2';", nil,
		options{maxRows: 50, timeoutSec: 120}, resolve)
	got := strings.Join(res.Messages, "|")
	if i, j := strings.Index(got, "error 8134"), strings.Index(got, "p2"); i < 0 || j < 0 || i > j {
		t.Errorf("messages out of order: %q", res.Messages)
	}
}

// labSecret returns the lab password, failing (without printing it) when there
// is none: redacting an empty secret is a no-op, so these tests would prove
// nothing.
func labSecret(t *testing.T, p sqlq.Profile, resolve sqlq.SecretResolver) string {
	t.Helper()
	secret, err := resolve(p)
	if err != nil {
		t.Fatalf("resolving the lab secret failed")
	}
	if secret == "" {
		t.Fatalf("the lab profile has no secret; these tests would prove nothing")
	}
	return secret
}

// The failure messages below print counts and booleans only: printing
// res.Messages, res.Plan or res.Error would write the password into the test
// log, which is the leak under test.

func TestNoticesAreRedacted(t *testing.T) {
	p, resolve := testProfile(t)
	secret := labSecret(t, p, resolve)
	batch := "PRINT '" + strings.ReplaceAll(secret, "'", "''") + "'; SELECT 1 AS a;"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 120}, resolve)
	if code != exitOK {
		t.Fatalf("exit %d; want %d (error present: %v)", code, exitOK, res.Error != nil)
	}
	leaked, marked := 0, 0
	for _, m := range res.Messages {
		if strings.Contains(m, secret) {
			leaked++
		}
		if strings.Contains(m, "[redacted]") {
			marked++
		}
	}
	if leaked > 0 || marked == 0 {
		t.Errorf("%d messages: %d carry the secret, %d carry [redacted]; want 0 and at least 1",
			len(res.Messages), leaked, marked)
	}
}

func TestPlanIsRedacted(t *testing.T) {
	p, resolve := testProfile(t)
	secret := labSecret(t, p, resolve)
	batch := "SELECT COUNT(*) AS n FROM sys.objects WHERE name = '" +
		strings.ReplaceAll(secret, "'", "''") + "';"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 120, wantPlan: true}, resolve)
	if code != exitOK {
		t.Fatalf("exit %d; want %d (error present: %v)", code, exitOK, res.Error != nil)
	}
	if res.Plan == nil {
		t.Fatalf("no plan returned")
	}
	for i, form := range secretForms(secret) {
		if strings.Contains(*res.Plan, form) {
			t.Errorf("the plan carries secret form #%d", i)
		}
	}
	if !strings.Contains(*res.Plan, "[redacted]") {
		t.Errorf("the plan (%d bytes) carries no [redacted] marker", len(*res.Plan))
	}
}

func TestErrorTextIsRedacted(t *testing.T) {
	p, resolve := testProfile(t)
	secret := labSecret(t, p, resolve)
	literal := strings.ReplaceAll(strings.ReplaceAll(secret, "'", "''"), "%", "%%")
	res, code := execute(p, "RAISERROR('"+literal+"', 16, 1);", nil,
		options{maxRows: 5, timeoutSec: 120}, resolve)
	if code != exitSQL {
		t.Fatalf("exit %d; want %d", code, exitSQL)
	}
	if res.Error == nil {
		t.Fatalf("no error returned")
	}
	leaked := strings.Contains(res.Error.Message, secret)
	marked := strings.Contains(res.Error.Message, "[redacted]")
	if leaked || !marked {
		t.Errorf("error text: carries the secret %v, carries [redacted] %v; want false and true",
			leaked, marked)
	}
}

func TestFirstPlanStaysAtTheRoot(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns;"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 120, wantPlan: true}, resolve)
	if code != exitOK {
		t.Fatalf("exit %d; want %d (error present: %v)", code, exitOK, res.Error != nil)
	}
	if res.Plan == nil || !strings.Contains(*res.Plan, "FROM sys.objects") {
		t.Fatalf("plan should be the first statement's (FROM sys.objects); got nil or another plan")
	}
	if len(res.MorePlans) != 1 {
		t.Fatalf("more_plans has %d entries; want 1", len(res.MorePlans))
	}
	if !strings.Contains(res.MorePlans[0], "FROM sys.columns") {
		t.Errorf("more_plans[0] should be the second statement's (FROM sys.columns)")
	}
}

// The caps limit what is stored, never what is read: a loop that stopped
// reading at the cap would leave the server waiting on a full connection.
// Each test below therefore checks something that arrives after the cap.

func TestMessageCapStopsStoringNotReading(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "SELECT 1 AS a;\n" + strings.Repeat("PRINT 'm';\n", 40) + "SELECT 2 AS b;"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 120, maxMessages: 10}, resolve)
	if code != exitOK {
		t.Fatalf("exit %d; want %d (error %+v)", code, exitOK, res.Error)
	}
	if len(res.Messages) != 10 || !res.MessagesTruncated {
		t.Errorf("%d messages, truncated %v; want 10 and true", len(res.Messages), res.MessagesTruncated)
	}
	if len(res.MoreResults) != 1 {
		t.Errorf("%d extra sets; want 1 (set b, after the 40 notices, must still be read)", len(res.MoreResults))
	}
}

func TestResultSetCapStopsStoringNotReading(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "SELECT 1 AS a; SELECT 2 AS b; SELECT 3 AS c; SELECT 4 AS d; RAISERROR('after the cap', 16, 1);"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 120, maxResultSets: 2}, resolve)
	if code != exitSQL || res.Error == nil || !strings.Contains(res.Error.Message, "after the cap") {
		t.Fatalf("exit %d, error %+v; want %d and 'after the cap'", code, res.Error, exitSQL)
	}
	if len(res.Rows) != 1 || res.Rows[0]["a"] != int64(1) {
		t.Errorf("root set should be a: %+v", res.Rows)
	}
	if len(res.MoreResults) != 1 {
		t.Fatalf("%d extra sets; want 1", len(res.MoreResults))
	}
	if rs := res.MoreResults[0].Rows; len(rs) != 1 || rs[0]["b"] != int64(2) {
		t.Errorf("more_results[0] should be b: %+v", rs)
	}
	if !res.MoreResultsTruncated {
		t.Errorf("more_results_truncated is false; want true")
	}
}

func TestLaterErrorBeyondMessageCapIsDropped(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "RAISERROR('e1', 16, 1); RAISERROR('e2', 16, 1); RAISERROR('e3', 16, 1); SELECT 1 AS a;"
	res, code := execute(p, batch, nil, options{maxRows: 5, timeoutSec: 120, maxMessages: 1}, resolve)
	if code != exitSQL || res.Error == nil || res.Error.Message != "e1" {
		t.Fatalf("exit %d, error %+v; want %d and e1", code, res.Error, exitSQL)
	}
	if len(res.Messages) != 1 || res.Messages[0] != "error 50000: e2" {
		t.Errorf("messages = %q; want exactly [\"error 50000: e2\"]", res.Messages)
	}
	if !res.MessagesTruncated {
		t.Errorf("messages_truncated is false; want true")
	}
	if len(res.Rows) != 1 {
		t.Errorf("%d rows; want 1 (the set after the errors must be read)", len(res.Rows))
	}
}

func TestPlanCapStopsStoringNotReading(t *testing.T) {
	p, resolve := testProfile(t)
	batch := "SELECT COUNT(*) AS a FROM sys.objects; SELECT COUNT(*) AS b FROM sys.columns; RAISERROR('after the plans', 16, 1);"
	res, code := execute(p, batch, nil,
		options{maxRows: 5, timeoutSec: 120, wantPlan: true, maxResultSets: 1}, resolve)
	if code != exitSQL || res.Error == nil || !strings.Contains(res.Error.Message, "after the plans") {
		t.Fatalf("exit %d, error present %v; want %d and 'after the plans'", code, res.Error != nil, exitSQL)
	}
	if res.Plan == nil {
		t.Errorf("no plan at the root")
	}
	if len(res.MorePlans) != 0 || !res.MorePlansTruncated {
		t.Errorf("%d more plans, truncated %v; want 0 and true", len(res.MorePlans), res.MorePlansTruncated)
	}
	if len(res.Columns) != 1 || res.Columns[0].Name != "a" || len(res.Rows) != 1 {
		t.Errorf("root set should be a: columns %+v, %d rows", res.Columns, len(res.Rows))
	}
}

// The statistics text is not parsed: it follows the session language, so the
// tests accept either wording and the second one forces French.

func TestStatisticsArriveInMessages(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT COUNT(*) AS n FROM sys.objects;", nil,
		options{maxRows: 5, timeoutSec: 120, wantStatistics: true}, resolve)
	if code != exitOK {
		t.Fatalf("exit %d; want %d (error %+v)", code, exitOK, res.Error)
	}
	joined := strings.Join(res.Messages, "\n")
	if !strings.Contains(joined, "logical reads") && !strings.Contains(joined, "lectures logiques") {
		t.Errorf("no IO statistics in messages: %q", res.Messages)
	}
	if !strings.Contains(joined, "CPU time") && !strings.Contains(joined, "Temps UC") {
		t.Errorf("no TIME statistics in messages: %q", res.Messages)
	}
}

func TestStatisticsFollowTheSessionLanguage(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SET LANGUAGE French; SELECT COUNT(*) AS n FROM sys.objects;", nil,
		options{maxRows: 5, timeoutSec: 120, wantStatistics: true}, resolve)
	if code != exitOK {
		t.Fatalf("exit %d; want %d (error %+v)", code, exitOK, res.Error)
	}
	joined := strings.Join(res.Messages, "\n")
	if !strings.Contains(joined, "lectures logiques") {
		t.Errorf("no French IO statistics in messages: %q", res.Messages)
	}
	if !strings.Contains(joined, "Temps UC") {
		t.Errorf("no French TIME statistics in messages: %q", res.Messages)
	}
}

// A set cut short by -timeout keeps the rows it read and says it is not whole.
// TOP bounds the server's work: the cross join alone runs for a minute and a
// half on the lab, and with TOP it still outlasts the one-second timeout.
func TestSetCutByTimeoutKeepsItsRows(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p,
		"SELECT TOP (20000000) a.object_id FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b;", nil,
		options{maxRows: 5, timeoutSec: 1}, resolve)
	if code == exitOK || res.Error == nil {
		t.Fatalf("a timed-out set must fail, got code %d", code)
	}
	if !res.Incomplete || res.RowCount == 0 || len(res.Rows) > 5 {
		t.Errorf("incomplete %v, row_count %d, %d rows kept", res.Incomplete, res.RowCount, len(res.Rows))
	}
}

// The DONE of the WAITFOR arrives before the timeout: only a MsgNextResultSet
// received after ctx expired is the cut.
func TestQueryEndingBeforeTheTimeoutSucceeds(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "WAITFOR DELAY '00:00:01'; SELECT 1 AS a;", nil,
		options{maxRows: 5, timeoutSec: 3}, resolve)
	if code != exitOK || res.Error != nil || res.Incomplete || len(res.Rows) != 1 {
		t.Errorf("code %d, error %+v, incomplete %v, %d rows", code, res.Error, res.Incomplete, len(res.Rows))
	}
}

// The first SQL error explains the rest, the timeout included (spec § 4.2).
func TestFirstErrorWinsOverTheTimeout(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p,
		"RAISERROR('first', 16, 1) WITH NOWAIT; RAISERROR('second', 16, 1) WITH NOWAIT; WAITFOR DELAY '00:00:05';", nil,
		options{maxRows: 5, timeoutSec: 1}, resolve)
	if code != exitSQL || res.Error == nil {
		t.Fatalf("code %d, error %+v", code, res.Error)
	}
	if res.Error.Number != 50000 || res.Error.Message != "first" {
		t.Errorf("error %+v, want 50000 first", res.Error)
	}
	found := false
	for _, m := range res.Messages {
		if m == "error 50000: second" {
			found = true
		}
	}
	if !found {
		t.Errorf("messages %q lack the second error", res.Messages)
	}
}

// The timeout falls in NextResultSet, after set a was read to its end: that set
// is whole and must not be marked incomplete. The server buffers output until a
// packet fills or the batch ends, so without the RAISERROR WITH NOWAIT flush set
// a never reaches the client before the timeout and the test checks nothing.
func TestTimeoutAfterACompleteSetLeavesItComplete(t *testing.T) {
	p, resolve := testProfile(t)
	res, code := execute(p, "SELECT 1 AS a; RAISERROR('', 0, 1) WITH NOWAIT; WAITFOR DELAY '00:00:05'; SELECT 2 AS b;", nil,
		options{maxRows: 5, timeoutSec: 1}, resolve)
	if code == exitOK || res.Error == nil {
		t.Fatalf("a timed-out batch must fail, got code %d", code)
	}
	if len(res.Rows) != 1 || res.Incomplete {
		t.Errorf("%d rows, incomplete %v", len(res.Rows), res.Incomplete)
	}
}

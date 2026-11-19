package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

func writeProfiles(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fleetOptions(profiles string) options {
	return options{profilesPath: profiles, query: "SELECT 1", maxRows: 50, timeoutSec: 30,
		connectTimeoutSec: 2, parallel: 4, deadlineSec: 100}
}

func TestSelectionByPatternAndAll(t *testing.T) {
	ps := sqlq.Profiles{"prd/a": {}, "prd/b": {}, "prd/fr/c": {}, "sta/a": {}, "solo": {}}
	cases := []struct {
		o    options
		want string
	}{
		{options{profilesMatch: "prd/*"}, "prd/a prd/b"},
		{options{profilesMatch: "prd/*/*"}, "prd/fr/c"},
		{options{profilesMatch: "*"}, "solo"},
		{options{profilesMatch: "PRD/*"}, ""},
		{options{all: true}, "prd/a prd/b prd/fr/c solo sta/a"},
	}
	for _, tc := range cases {
		got, _, err := selectProfiles(ps, tc.o)
		if err != nil || strings.Join(got, " ") != tc.want {
			t.Errorf("%+v: got %q, %v; want %q", tc.o, got, err, tc.want)
		}
	}
	if _, _, err := selectProfiles(ps, options{profilesMatch: "["}); err == nil {
		t.Error("an invalid pattern was accepted")
	}
}

// A yes given on a preview must cover exactly the profiles shown: a new
// profile, or a changed environment, changes the fingerprint.
func TestFingerprintFollowsNamesAndEnvironments(t *testing.T) {
	a := sqlq.Profiles{"prd/a": {Environment: "prod"}, "prd/b": {}}
	base := fingerprint(a, []string{"prd/b", "prd/a"})
	if base != fingerprint(a, []string{"prd/a", "prd/b"}) {
		t.Error("the fingerprint depends on the order of the names")
	}
	b := sqlq.Profiles{"prd/a": {Environment: "prod"}, "prd/b": {Environment: "prod"}}
	if base == fingerprint(b, []string{"prd/a", "prd/b"}) {
		t.Error("a changed environment kept the fingerprint")
	}
	if base == fingerprint(a, []string{"prd/a"}) {
		t.Error("a smaller selection kept the fingerprint")
	}
	if len(base) != 12 {
		t.Errorf("fingerprint %q is not 12 characters", base)
	}
}

func fleetJSON(t *testing.T, out string) fleetResult {
	t.Helper()
	var r fleetResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out)
	}
	return r
}

func TestProdSelectionNeedsTheFingerprint(t *testing.T) {
	path := writeProfiles(t, `{"prd/a": {"server": "127.0.0.1,1", "auth": "sql", "user": "u",
	  "passwordEnv": "NOPE", "environment": "prod"}}`)
	o := fleetOptions(path)
	o.profilesMatch = "prd/*"
	out, code := captureRun(t, o)
	if code != exitUsage || !strings.Contains(out, "-selection-fingerprint") {
		t.Fatalf("code %d, %s; want a refusal asking for the fingerprint", code, out)
	}
	o.selectionFingerprint = "000000000000"
	out, code = captureRun(t, o)
	if code != exitUsage || !strings.Contains(out, "selection changed") {
		t.Fatalf("code %d, %s; want a refusal of a stale fingerprint", code, out)
	}
	o.listProfiles = true
	o.selectionFingerprint = ""
	out, _ = captureRun(t, o)
	var preview struct {
		Fingerprint string `json:"selection_fingerprint"`
	}
	json.Unmarshal([]byte(out), &preview)
	o.listProfiles = false
	o.selectionFingerprint = preview.Fingerprint
	o.connectRetries = 0
	out, code = captureRun(t, o)
	r := fleetJSON(t, out)
	if r.SelectionFingerprint != preview.Fingerprint || r.ProfilesSelected != 1 {
		t.Fatalf("the preview's fingerprint did not open the run: code %d, %s", code, out)
	}
}

func TestFleetRefusals(t *testing.T) {
	path := writeProfiles(t, `{"a": {"server": "x", "auth": "sql", "user": "u", "passwordEnv": "X"}}`)
	for name, change := range map[string]func(*options){
		"allow-write":   func(o *options) { o.allowWrite = true },
		"save-query":    func(o *options) { o.saveQuery = "x"; o.summary = "S." },
		"plan":          func(o *options) { o.wantPlan = true },
		"with profile":  func(o *options) { o.profileName = "a" },
		"match and all": func(o *options) { o.all = true },
		"no match":      func(o *options) { o.profilesMatch = "zzz/*" },
		"bad pattern":   func(o *options) { o.profilesMatch = "[" },
		"CR":            func(o *options) { o.profilesMatch = "a\r" },
	} {
		o := fleetOptions(path)
		o.profilesMatch = "*"
		change(&o)
		out, code := captureRun(t, o)
		if code != exitUsage {
			t.Errorf("%s: code %d, %s; want 1", name, code, out)
		}
	}
	o := fleetOptions(path)
	o.all = true
	o.query = "DELETE FROM t"
	if _, code := captureRun(t, o); code != exitRefused {
		t.Errorf("a write on a fleet: code %d; want 3", code)
	}
}

// Every selected profile unusable here: nothing was attempted, which is not
// a success.
func TestAllSkippedIsNotASuccess(t *testing.T) {
	path := writeProfiles(t, `{"a ": {"server": "x", "auth": "sql", "user": "u", "passwordEnv": "X"}}`)
	o := fleetOptions(path)
	o.all = true
	out, code := captureRun(t, o)
	r := fleetJSON(t, out)
	if code != exitUsage || r.ProfilesSkipped != 1 || r.Profiles[0].Error.Category != catUsage {
		t.Errorf("code %d, %s; want 1 and one skipped profile", code, out)
	}
}

// Two silent instances, a 3 s deadline: the run must still print one JSON
// object, say the deadline was reached and exit 5.
func TestDeadlineGivesAPartialResult(t *testing.T) {
	f := startFake(t, silent)
	port := strconv.Itoa(f.ln.Addr().(*net.TCPAddr).Port)
	t.Setenv("FLEET_PWD", "pw")
	path := writeProfiles(t, `{
	  "g/a": {"server": "127.0.0.1,`+port+`", "auth": "sql", "user": "u", "passwordEnv": "FLEET_PWD", "encrypt": "disable"},
	  "g/b": {"server": "127.0.0.1,`+port+`", "auth": "sql", "user": "u", "passwordEnv": "FLEET_PWD", "encrypt": "disable"}}`)
	o := fleetOptions(path)
	o.profilesMatch = "g/*"
	o.connectTimeoutSec = 30
	o.deadlineSec = 3
	o.parallel = 1
	out, code := captureRun(t, o)
	r := fleetJSON(t, out)
	if code != exitFleetPartial || !r.DeadlineReached || r.ProfilesFailed != 2 {
		t.Fatalf("code %d, %s; want 5, deadline_reached and two failures", code, out)
	}
	for _, p := range r.Profiles {
		if p.Error == nil || p.Error.Category != catDeadline {
			t.Errorf("%s: %+v; want category deadline", p.Profile, p.Error)
		}
	}
	if len(r.Warnings) == 0 {
		t.Error("no warning that -timeout and the opening exceed -deadline")
	}
}

func col(name, typ string) sqlq.Column { return sqlq.Column{Name: name, Type: typ} }

// The reference schema is the majority one, a profile without a result set
// never empties the rows, and an instance missing from rows is counted.
func TestAggregateCountsEveryInstanceMissingFromRows(t *testing.T) {
	ed := []sqlq.Column{col("edition", "NVARCHAR")}
	names := []string{"a", "b", "c", "d", "e"}
	results := []sqlq.Result{
		{}, // a: no result set
		{Columns: []sqlq.Column{col("version", "INT")}, Rows: []sqlq.Row{{"version": 1}}, RowCount: 1}, // b: differs
		{Columns: ed, Rows: []sqlq.Row{{"edition": "Std"}}, RowCount: 3, Truncated: true},
		{Columns: ed, Rows: []sqlq.Row{{"edition": "Ent"}}, RowCount: 1},
		{Error: &sqlq.SQLError{Message: "x", Category: catSQLError}, Columns: ed, Rows: []sqlq.Row{{"edition": "?"}}},
	}
	codes := []int{0, 0, 0, 0, exitSQL}
	attempt := []bool{true, true, true, true, true}
	var out fleetResult
	aggregate(&out, names, results, codes, attempt)
	if out.ProfilesNoResultSet != 1 || out.ProfilesColumnsDiffer != 1 || out.ProfilesFailed != 1 || out.ProfilesOK != 4 {
		t.Errorf("counters %+v", out)
	}
	if len(out.Rows) != 2 || out.RowCount != 4 || !out.Truncated {
		t.Errorf("rows %v, rowcount %d, truncated %v; want 2 rows of c and d, 4, true", out.Rows, out.RowCount, out.Truncated)
	}
	if out.Columns[0].Name != "profile" || out.Columns[1].Name != "edition" {
		t.Errorf("columns %v", out.Columns)
	}
	if out.Profiles[1].Rows == nil || !out.Profiles[1].ColumnsDiffer || out.Profiles[2].Rows != nil {
		t.Errorf("profiles[].rows: differing kept %v, aggregated kept %v", out.Profiles[1].Rows, out.Profiles[2].Rows)
	}
	if out.Profiles[4].Rows == nil {
		t.Error("the rows of a profile that failed after returning them are lost")
	}
}

// A server column named profile, in any case, moves the added key to
// sqlq_profile for the whole run; it never overwrites a server value.
func TestProfileKeyNeverOverwritesAServerColumn(t *testing.T) {
	results := []sqlq.Result{
		{Columns: []sqlq.Column{col("Profile", "NVARCHAR")}, Rows: []sqlq.Row{{"Profile": "server value"}}, RowCount: 1},
		{Columns: []sqlq.Column{col("Profile", "NVARCHAR")}, Rows: []sqlq.Row{{"Profile": "other"}}, RowCount: 1},
	}
	var out fleetResult
	aggregate(&out, []string{"a", "b"}, results, []int{0, 0}, []bool{true, true})
	if out.Columns[0].Name != "sqlq_profile" || out.Rows[0]["Profile"] != "server value" || out.Rows[0]["sqlq_profile"] != "a" {
		t.Errorf("columns %v, rows %v", out.Columns, out.Rows)
	}
}

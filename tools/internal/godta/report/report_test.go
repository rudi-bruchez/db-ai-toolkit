package report

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	tblT = 101
	tblH = 102
	tblR = 103
)

func asc(id int) model.KeyCol { return model.KeyCol{ColumnID: id} }

// fixture is a workload over the integration data set (plan, annex A), plus
// an index nix_t_b the lab test creates and a table godta_r that exists only
// here, for the redundancies.
func fixture(db string) *model.Workload {
	return &model.Workload{
		Server: conn.Instance{ServerName: "lab01", Edition: "Developer Edition (64-bit)", EngineEdition: 3,
			Major: 16, Level: "RTM", Version: "16.0.4250.1"},
		Database: model.DBInfo{ID: 7, Name: db, Collation: "Latin1_General_CI_AS", Compat: 160, QueryStoreState: "READ_WRITE"},
		Window: model.Window{From: time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC),
			Until: time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC)},
		Source:      model.SourceQueryStore,
		CaptureMode: "ALL",
		Queries: []model.Query{
			{QueryID: 11, Text: "SELECT c FROM dbo.godta_t\n  WHERE a = @a AND b = @b", Executions: 100},
			{QueryID: 12, Text: "SELECT TOP 10 c FROM dbo.godta_t WHERE b = 3", Executions: 40},
			{QueryID: 13, Text: "SELECT y FROM dbo.godta_h WHERE x = 42 AND y > 'a'", Executions: 25},
			{QueryID: 14, Text: "SELECT a FROM dbo.godta_t WHERE c = '7'", Executions: 5},
			{QueryID: 15, Text: "SELECT `pad` FROM dbo.godta_t WHERE pad = ''", Executions: 2},
		},
		Writes: []model.Query{
			{QueryID: 21, Text: "UPDATE dbo.godta_t SET c = c + 'x' WHERE a = 500", Executions: 10, IsWrite: true},
		},
		Tables: map[int]*model.Table{
			tblT: {ObjectID: tblT, Schema: "dbo", Name: "godta_t", Rows: 1000000,
				Columns: []model.Column{{ID: 1, Name: "id"}, {ID: 2, Name: "a"}, {ID: 3, Name: "b"}, {ID: 4, Name: "c"}, {ID: 5, Name: "pad"}},
				Indexes: []model.Index{
					{ObjectID: tblT, IndexID: 1, Name: "pk_godta_t", Kind: model.KindClustered, Keys: []model.KeyCol{asc(1)},
						Unique: true, PrimaryKey: true, DataSpace: "PRIMARY"},
					{ObjectID: tblT, IndexID: 2, Name: "godta_filt", Kind: model.KindNonclustered, Keys: []model.KeyCol{asc(4)},
						Filter: "([a]=(933))", DataSpace: "PRIMARY"},
					{ObjectID: tblT, IndexID: 3, Name: "nix_t_b", Kind: model.KindNonclustered, Keys: []model.KeyCol{asc(3)},
						Compression: "PAGE", FillFactor: 90, DataSpace: "PRIMARY",
						Usage: &model.IndexUsage{Seeks: 4, Scans: 1, Updates: 9, LeafInserts: 3}},
				}},
			tblH: {ObjectID: tblH, Schema: "dbo", Name: "godta_h", Rows: 200000, IsHeap: true,
				Columns: []model.Column{{ID: 1, Name: "x"}, {ID: 2, Name: "y"}},
				Indexes: []model.Index{
					{ObjectID: tblH, IndexID: 0, Kind: model.KindHeap, DataSpace: "PRIMARY"},
					{ObjectID: tblH, IndexID: 2, Name: "nix_h_x", Kind: model.KindNonclustered, Keys: []model.KeyCol{asc(1)},
						Compression: "ROW", DataSpace: "PRIMARY", Usage: &model.IndexUsage{Seeks: 120, Lookups: 0, Updates: 2}},
				}},
			tblR: {ObjectID: tblR, Schema: "dbo", Name: "godta_r",
				Columns: []model.Column{{ID: 1, Name: "k"}, {ID: 2, Name: "v"}},
				Indexes: []model.Index{
					{ObjectID: tblR, IndexID: 2, Name: "ix_r1", Kind: model.KindNonclustered, Keys: []model.KeyCol{asc(1)}},
					{ObjectID: tblR, IndexID: 3, Name: "ix_r2", Kind: model.KindNonclustered, Keys: []model.KeyCol{asc(1)}},
					{ObjectID: tblR, IndexID: 4, Name: "ix_r3", Kind: model.KindNonclustered, Keys: []model.KeyCol{asc(1), asc(2)}},
					{ObjectID: tblR, IndexID: 5, Name: "ix_r4", Kind: model.KindNonclustered, Keys: []model.KeyCol{asc(1)}, Include: []int{2}},
				}},
		},
		Hypotheticals: []model.Index{{ObjectID: tblT, IndexID: 9, Name: "_dta_index_godta_t_5_1", Hypothetical: true}},
		Skipped:       []string{"godta_cs: columnstore table: not evaluated"},
		Warnings:      []string{"Query Store capture mode is ALL"},
	}
}

const formula = "gain = sum((before - after) x executions); maintenance = sum(operator cost x executions); recommended when gain >= 10% of the cost and maintenance <= gain / 2"

func measure(q int64, before, after float64) model.Measure {
	return model.Measure{QueryID: q, Before: before, After: after, Status: "measured"}
}

func recs() []model.Recommendation {
	return []model.Recommendation{
		{
			Candidate: model.Candidate{ID: "a1b2c3d4e5f6", Table: tblT, Keys: []model.KeyCol{asc(2), asc(3)}, EqCount: 2, Include: []int{4},
				Origin: "algorithm", Rules: []string{"eq-first", "include-outputs"}, Queries: []int64{11, 21}, Decision: model.DecisionNew},
			Gain: 900, Maintenance: 12.5, SizeMB: 21.4, Decision: Recommended, Formula: formula,
			Queries: []model.Measure{measure(11, 10, 1), {QueryID: 21, Status: "measured", Before: 2, After: 2, Maintenance: 1.25}},
			Notes:   []string{"estimates use sampled statistics; a real index has full-scan statistics"},
		},
		{
			Candidate: model.Candidate{ID: "0f0f0f0f0f0f", Table: tblT, Keys: []model.KeyCol{asc(3)}, Include: []int{4},
				Origin: "ai", Reason: "query 12 looks up c after a seek on b", Queries: []int64{12},
				Decision: model.DecisionWiden, Existing: "nix_t_b"},
			Gain: 300, Maintenance: 3, SizeMB: 9.5, Decision: Recommended, Formula: formula,
			Queries: []model.Measure{measure(12, 8.5, 1)},
		},
		{
			Candidate: model.Candidate{ID: "abcabcabcabc", Table: tblH, Keys: []model.KeyCol{asc(1), asc(2)},
				Origin: "missing_index", Queries: []int64{13}, Decision: model.DecisionReplace, Existing: "nix_h_x"},
			Gain: 150, Maintenance: 1, SizeMB: 8, Decision: Recommended, Formula: formula,
			Queries: []model.Measure{measure(13, 7, 1)},
		},
		{
			Candidate: model.Candidate{ID: "dddddddddddd", Table: tblT, Keys: []model.KeyCol{asc(4)}, Include: []int{2},
				Origin: "algorithm", Queries: []int64{14}, Decision: model.DecisionNew},
			Gain: 4, Maintenance: 3, SizeMB: 30, Decision: Discuss, Formula: formula,
			Queries: []model.Measure{measure(14, 2, 1.2)},
			Notes:   []string{"made redundant by a1b2c3d4e5f6"},
		},
		{
			Candidate: model.Candidate{ID: "eeeeeeeeeeee", Table: tblT, Keys: []model.KeyCol{asc(5)},
				Origin: "ai", Reason: "pad is searched", Queries: []int64{15}, Decision: model.DecisionNew},
			Gain: 0, Maintenance: 0, SizeMB: 200, Decision: Rejected, Formula: formula,
			Queries: []model.Measure{measure(15, 3, 3)},
		},
		{
			Candidate: model.Candidate{ID: "ffffffffffff", Table: tblH, Keys: []model.KeyCol{asc(2)},
				Origin: "algorithm", Queries: []int64{13}, Decision: model.DecisionNew},
			Decision: Rejected, Formula: formula,
			Queries: []model.Measure{{QueryID: 13, Status: "not evaluated: timeout", Before: 5.5, After: 2.5}},
		},
	}
}

var autoStats = []string{"[dbo].[godta_t].[_WA_Sys_00000003_0123ABCD]"}
var evalWarnings = []string{"query 99 failed to compile: error 8180"}

// write runs Write in a temporary workspace, after an evaluation, and
// returns the three files.
func write(t *testing.T, w *model.Workload, rs []model.Recommendation) (rep, script, res string) {
	t.Helper()
	return writeEval(t, w, rs, true)
}

func writeEval(t *testing.T, w *model.Workload, rs []model.Recommendation, evaluated bool) (rep, script, res string) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.DBDir(w.Server.ServerName, w.Database.Name)
	if err := Write(ws, dir, w, rs, evaluated, autoStats, evalWarnings); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return read(ReportFile), read(ScriptFile), read(ResultFile)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file; got:\n%s", name, got)
	}
}

func TestReportGolden(t *testing.T) {
	rep, _, res := write(t, fixture("godta_it_golden"), recs())
	golden(t, "rapport.md.golden", rep)
	golden(t, "result.json.golden", res)
	var r Result
	if err := json.Unmarshal([]byte(res), &r); err != nil {
		t.Fatalf("result.json does not decode into Result: %v", err)
	}
}

func TestScriptGolden(t *testing.T) {
	_, script, _ := write(t, fixture("godta_it_golden"), recs())
	golden(t, "indexes.sql.golden", script)
}

// TestScriptNeverDropsProtected replaces nix_h_x after making it, one cause
// at a time, an index the script must never drop.
func TestScriptNeverDropsProtected(t *testing.T) {
	cases := map[string]func(ix *model.Index){
		"unique":            func(ix *model.Index) { ix.Unique = true },
		"primary key":       func(ix *model.Index) { ix.PrimaryKey = true },
		"unique constraint": func(ix *model.Index) { ix.UniqueConstraint = true },
		"clustered":         func(ix *model.Index) { ix.Kind = model.KindClustered },
		"named in a hint":   func(ix *model.Index) { ix.NamedIn = []string{"plan_guide:pg1"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := fixture("godta_it_golden")
			mutate(&w.Tables[tblH].Indexes[1])
			_, script, res := write(t, w, recs()[2:3])
			if strings.Contains(script, "DROP INDEX") {
				t.Errorf("the script drops a protected index:\n%s", script)
			}
			if !strings.Contains(script, "CREATE NONCLUSTERED INDEX") {
				t.Errorf("the replacing index is not created:\n%s", script)
			}
			if strings.Contains(res, "DROP INDEX") {
				t.Errorf("result.json lists a DROP of a protected index")
			}
		})
	}
	// The control: the same replacement of a plain index drops it.
	_, script, _ := write(t, fixture("godta_it_golden"), recs()[2:3])
	if !strings.Contains(script, "DROP INDEX [nix_h_x] ON [dbo].[godta_h]") {
		t.Errorf("control: the plain replaced index is not dropped:\n%s", script)
	}
}

// section returns the part of rep from the heading title to the next one.
func section(rep, title string) string {
	_, rest, ok := strings.Cut(rep, "\n## "+title+"\n")
	if !ok {
		return ""
	}
	s, _, _ := strings.Cut(rest, "\n## ")
	return s
}

func TestUnmeasuredHasNoNumbers(t *testing.T) {
	rep, _, res := write(t, fixture("godta_it_golden"), recs()[5:6])
	s := section(rep, "Not evaluated")
	if !strings.Contains(s, "ffffffffffff") || !strings.Contains(s, "not evaluated: timeout") {
		t.Fatalf("the not evaluated candidate is missing, or without its status:\n%s", s)
	}
	for _, bad := range []string{"Gain", "5.50", "2.50", "before", "after", "MB"} {
		if strings.Contains(s, bad) {
			t.Errorf("the not evaluated block contains %q:\n%s", bad, s)
		}
	}
	if strings.Contains(section(rep, "Rejected"), "ffffffffffff") {
		t.Errorf("the not evaluated candidate is also listed as rejected")
	}
	var r Result
	if err := json.Unmarshal([]byte(res), &r); err != nil {
		t.Fatal(err)
	}
	rr := r.Recommendations[0]
	if rr.Section != "not_evaluated" || rr.Gain != nil || rr.Maintenance != nil || rr.Queries[0].Before != nil || rr.Queries[0].After != nil {
		t.Errorf("result.json gives numbers to a not evaluated candidate: %+v", rr)
	}
}

// TestConfrontDiscussSection: a candidate score recommends but the
// confrontation left to discuss is listed under To discuss, not scripted.
func TestConfrontDiscussSection(t *testing.T) {
	r := recs()[0]
	r.Candidate.Decision, r.Candidate.Existing = model.DecisionDiscuss, "pk_godta_t"
	rep, script, _ := write(t, fixture("godta_it_golden"), []model.Recommendation{r})
	if strings.Contains(section(rep, "Recommended indexes"), r.Candidate.ID) {
		t.Errorf("the candidate is listed under Recommended indexes:\n%s", rep)
	}
	if s := section(rep, "To discuss"); !strings.Contains(s, r.Candidate.ID) || !strings.Contains(s, "to discuss with pk_godta_t") {
		t.Errorf("the candidate is not under To discuss, with its existing index:\n%s", s)
	}
	if !strings.Contains(script, "-- No recommended index.") {
		t.Errorf("the script has a block for it:\n%s", script)
	}
}

// TestNotRunStatus: before evaluate, a candidate says evaluate has not run,
// not that it has no query; after an evaluation that measured none of its
// queries, it says it has no query.
func TestNotRunStatus(t *testing.T) {
	r := model.Recommendation{Candidate: recs()[0].Candidate, Decision: Rejected}
	rep, _, res := writeEval(t, fixture("godta_it_golden"), []model.Recommendation{r}, false)
	s := section(rep, "Not evaluated")
	if !strings.Contains(s, "- Status: "+StatusNotRun+"\n") || strings.Contains(s, "no query") {
		t.Errorf("before evaluate, the status is not %q:\n%s", StatusNotRun, s)
	}
	if !strings.Contains(res, `"status": "`+StatusNotRun+`"`) {
		t.Errorf("result.json lacks the status %q:\n%s", StatusNotRun, res)
	}
	rep, _, _ = write(t, fixture("godta_it_golden"), []model.Recommendation{r})
	if s := section(rep, "Not evaluated"); !strings.Contains(s, "- Status: not evaluated: no query\n") {
		t.Errorf("after an evaluation, the status is not no query:\n%s", s)
	}
}

// TestRowsWrittenLine: the rows of the unmeasured writes are shown, in rows,
// next to the cost figures, and in result.json.
func TestRowsWrittenLine(t *testing.T) {
	r := recs()[3]
	r.RowsWritten = 50
	rep, _, res := write(t, fixture("godta_it_golden"), []model.Recommendation{r})
	if s := section(rep, "To discuss"); !strings.Contains(s, "- Rows written by the unmeasured writes: 50.00\n") {
		t.Errorf("the rows written line is missing:\n%s", s)
	}
	if !strings.Contains(res, `"rows_written": 50`) {
		t.Errorf("result.json lacks rows_written:\n%s", res)
	}
}

func TestPlanCacheWording(t *testing.T) {
	w := fixture("godta_it_golden")
	w.Source = model.SourcePlanCache
	w.CaptureMode = ""
	rep, script, res := write(t, w, recs())
	for name, s := range map[string]string{ReportFile: rep, ScriptFile: script, ResultFile: res} {
		for _, bad := range []string{"24 h", "last 24 hours"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s in plan_cache contains %q", name, bad)
			}
		}
		if !strings.Contains(s, "since each plan was compiled") {
			t.Errorf("%s in plan_cache does not say the counters run since compilation", name)
		}
	}
}

// TestPlanCacheQueryHash: from the plan cache, a query is named by its
// query_hash, with the Query Store query_ids it covers, never by the bigint
// goDTA made of the hash.
func TestPlanCacheQueryHash(t *testing.T) {
	w := fixture("godta_it_golden")
	w.Source = model.SourcePlanCache
	const fake = -7793925287569940523
	w.Queries[0].QueryID, w.Queries[0].Hash, w.Queries[0].QueryIDs = fake, "0x93D8F6AC4A11D2D5", []int64{3, 8}
	w.Queries[1].QueryID, w.Queries[1].Hash = 2744070552120781213, "0x2614A0E2B3C4D59D"
	r := recs()[0]
	r.Queries = []model.Measure{measure(fake, 10, 1), {QueryID: 2744070552120781213, Status: "not evaluated: timeout"}}
	rep, _, res := write(t, w, []model.Recommendation{r})
	for _, want := range []string{"query_hash 0x93D8F6AC4A11D2D5 (Query Store query_id 3, 8) (read)", "query_hash 0x2614A0E2B3C4D59D (read)"} {
		if !strings.Contains(rep, want) {
			t.Errorf("rapport.md lacks %q:\n%s", want, rep)
		}
	}
	for _, bad := range []string{"-7793925287569940523", "2744070552120781213"} {
		if strings.Contains(rep, bad) || strings.Contains(res, bad) {
			t.Errorf("the made-up identifier %s is shown", bad)
		}
	}
	var got Result
	if err := json.Unmarshal([]byte(res), &got); err != nil {
		t.Fatal(err)
	}
	q := got.Recommendations[0].Queries[0]
	if q.QueryID != 0 || q.QueryHash != "0x93D8F6AC4A11D2D5" || len(q.QueryIDs) != 2 {
		t.Errorf("result.json query: %+v, want the hash and its query_ids, no query_id", q)
	}
}

func TestRedundancies(t *testing.T) {
	got := Redundancies(fixture("x").Tables[tblR])
	want := []string{
		"[dbo].[godta_r].[ix_r2] is an exact duplicate of [dbo].[godta_r].[ix_r1]",
		"the keys of [dbo].[godta_r].[ix_r1] are a prefix of the keys of [dbo].[godta_r].[ix_r3]",
		"[dbo].[godta_r].[ix_r4] has the same keys as [dbo].[godta_r].[ix_r1], with other included columns",
		"the keys of [dbo].[godta_r].[ix_r2] are a prefix of the keys of [dbo].[godta_r].[ix_r3]",
		"[dbo].[godta_r].[ix_r4] has the same keys as [dbo].[godta_r].[ix_r2], with other included columns",
		"the keys of [dbo].[godta_r].[ix_r4] are a prefix of the keys of [dbo].[godta_r].[ix_r3]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Redundancies:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSummary(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rows := []model.SummaryRow{
		{Server: "lab01", Database: "zeta", Status: "evaluated", Recommended: 2, Discuss: 1, Rejected: 3, Gain: 1234.5},
		{Server: "lab01", Database: "alpha", Status: "skipped: offline", Warnings: []string{"a|b"}},
	}
	if err := Summary(ws, rows); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(ws.Root, SummaryFile))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "synthese.md.golden", string(b))
	if !strings.Contains(string(b), "| lab01 | zeta | evaluated | 2 | 1234.50 | 1 |") {
		t.Errorf("the total gain of zeta is not in its row:\n%s", b)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/report"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// reportFix is a project with database "app" of server lab01 collected,
// one candidate on godta_t for query 1 (100 executions), and its server
// evaluation: cost 10 before, 8 after, a gain of 200 out of 1000, 20 %.
type reportFix struct {
	args []string
	ws   workspace.WS
	dir  string
	w    *model.Workload
	c    model.Candidate
	ev   evaluateFile
	// plan is plan-evaluate.json, written when its RunID is set.
	plan runplan.Plan
}

func newReportFix(t *testing.T) *reportFix {
	t.Helper()
	f := &reportFix{args: baseArgs(t, writeProfiles(t), "rw")}
	ws, err := workspace.Open(f.args[1])
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws
	inst := conn.Instance{ServerName: "lab01", MachineName: "LABM", Major: 16}
	tbl := &model.Table{ObjectID: 1001, Schema: "dbo", Name: "godta_t", Rows: 1000,
		Columns: []model.Column{{ID: 1, Name: "id", Type: "int", AvgWidth: 4}, {ID: 2, Name: "a", Type: "int", AvgWidth: 4}, {ID: 3, Name: "b", Type: "int", AvgWidth: 4}},
		Indexes: []model.Index{{ObjectID: 1001, IndexID: 1, Name: "pk", Kind: model.KindClustered, Keys: []model.KeyCol{{ColumnID: 1}}, Unique: true, PrimaryKey: true}}}
	f.w = &model.Workload{Server: inst, Database: model.DBInfo{ID: 5, Name: "app"}, Source: model.SourceQueryStore,
		Queries: []model.Query{{QueryID: 1, Text: "SELECT b FROM dbo.godta_t WHERE a = @a", Executions: 100}},
		Tables:  map[int]*model.Table{1001: tbl}}
	f.c = model.Candidate{Table: 1001, Keys: []model.KeyCol{{ColumnID: 2}}, Include: []int{3}, Origin: candidate.OriginAlgorithm,
		Queries: []int64{1}, Decision: model.DecisionNew}
	f.c.ID = candidate.ID(f.c)
	f.w.CollectID = "c0000001"
	f.plan = runplan.Plan{RunID: "r0000001", Phase: runplan.PhaseEvaluate, CollectID: f.w.CollectID}
	f.ev = evaluateFile{RunID: "r0000001", CollectID: f.w.CollectID,
		Measures: []model.Measure{{QueryID: 1, Candidate: f.c.ID, Before: 10, After: 8, Status: "measured"}}}
	f.dir = ws.DBDir("lab01", "app")
	f.write(t)
	return f
}

// write writes the files of the fixture as they are.
func (f *reportFix) write(t *testing.T) {
	t.Helper()
	for name, v := range map[string]any{
		workspace.CollectFile:        f.w,
		workspace.CandidatesFile:     []model.Candidate{f.c},
		evaluateCandidatesFile:       []model.Candidate{f.c},
		workspace.EvaluateServerFile: f.ev,
	} {
		if err := f.ws.WriteJSON(f.dir, name, v); err != nil {
			t.Fatal(err)
		}
	}
	if f.plan.RunID != "" {
		if err := f.ws.WriteJSON(f.dir, workspace.PlanFile(runplan.PhaseEvaluate), f.plan); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *reportFix) report(t *testing.T, flags ...string) (int, output, string) {
	t.Helper()
	code, out := invoke(t, noOpen(t), append(append([]string{"report"}, f.args...), flags...)...)
	rep, _ := os.ReadFile(filepath.Join(f.dir, report.ReportFile))
	return code, out, string(rep)
}

// TestReportThresholdFlags: the thresholds of the decision are flags of
// report, with their defaults in --help, and the formula of rapport.md gives
// the values used.
func TestReportThresholdFlags(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	f := newReportFix(t)

	_, help := invoke(t, noOpen(t), "report", "--help")
	usage := strings.Join(help.Warnings, "\n")
	for _, want := range []string{"-min-gain-ratio", "(default 0.1)", "-max-maintenance-ratio", "(default 0.5)", "-min-queries", "(default 1)"} {
		if !strings.Contains(usage, want) {
			t.Errorf("report --help lacks %q:\n%s", want, usage)
		}
	}

	code, out, rep := f.report(t)
	if code != exitOK || len(out.Databases) != 1 {
		t.Fatalf("report with the defaults: code %d, output %+v", code, out)
	}
	if !strings.Contains(rep, ">= 10% of the summed cost") || !strings.Contains(section(rep, "Recommended indexes"), f.c.ID) {
		t.Fatalf("control, defaults: the 20%% gain is not recommended under 10%%:\n%s", rep)
	}

	code, out, rep = f.report(t, "--min-gain-ratio", "0.5", "--max-maintenance-ratio", "0.25", "--min-queries", "2")
	if code != exitOK {
		t.Fatalf("report with thresholds: code %d, output %+v", code, out)
	}
	for _, want := range []string{">= 50% of the summed cost", "maintenance <= 25% of gain", "at least 2 query is measured"} {
		if !strings.Contains(rep, want) {
			t.Errorf("the formula does not give the value used, %q:\n%s", want, rep)
		}
	}
	if strings.Contains(section(rep, "Recommended indexes"), f.c.ID) {
		t.Errorf("a gain of 20%% is recommended under --min-gain-ratio 0.5:\n%s", rep)
	}

	for _, bad := range [][]string{{"--min-gain-ratio", "-1"}, {"--max-maintenance-ratio", "NaN"}, {"--min-queries", "0"}} {
		if code, out, _ := f.report(t, bad...); code != exitUsage {
			t.Errorf("report %v: code %d, want %d (output %+v)", bad, code, exitUsage, out)
		}
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

// TestReportRefusesStaleEvaluation: report refuses, as a usage error and
// before writing anything, an evaluation that measured another collection
// (collect ran again since) or whose plan a later --dry-run replaced, and
// says to run evaluate again.
func TestReportRefusesStaleEvaluation(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	f := newReportFix(t)
	code, out, _ := f.report(t)
	if code != exitOK || hasWarning(out.Databases[0], "does not record the collection") {
		t.Fatalf("control, a consistent workspace: code %d, output %+v", code, out)
	}

	cases := map[string]struct {
		change func(f *reportFix)
		want   string
	}{
		"collect again": {func(f *reportFix) { f.w.CollectID = "c0000002" }, "collect ran again since"},
		"new dry-run":   {func(f *reportFix) { f.plan.RunID = "r0000002" }, "a later evaluate --dry-run"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newReportFix(t)
			c.change(f)
			f.write(t)
			code, out, rep := f.report(t)
			if code != exitUsage || out.Error == nil || out.Error.Kind != "usage" {
				t.Fatalf("code %d, output %+v, want a usage error, exit %d", code, out, exitUsage)
			}
			if !strings.Contains(out.Error.Message, c.want) || !strings.Contains(out.Error.Message, "run evaluate again") {
				t.Errorf("the error %q does not say %q and to run evaluate again", out.Error.Message, c.want)
			}
			if rep != "" {
				t.Errorf("rapport.md was written:\n%s", rep)
			}
		})
	}

	// An evaluation that does not record its collection (evaluate --target
	// lab does not yet) is scored, with a warning.
	f = newReportFix(t)
	f.ev.CollectID = ""
	f.write(t)
	code, out, _ = f.report(t)
	if code != exitOK || !hasWarning(out.Databases[0], "does not record the collection") {
		t.Errorf("an evaluation without collect_id: code %d, output %+v, want a warning", code, out)
	}
}

// TestEvaluateRefusesRecollected: an evaluate plan whose collection was
// replaced by a new collect is refused before any write.
func TestEvaluateRefusesRecollected(t *testing.T) {
	f := newEvalFix(t)
	f.w.CollectID = "c0000001"
	if err := f.ws.WriteJSON(f.dir, workspace.CollectFile, f.w); err != nil {
		t.Fatal(err)
	}
	file, plan := f.dryRun(t)
	if plan.CollectID != "c0000001" {
		t.Fatalf("the plan records collection %q, want c0000001", plan.CollectID)
	}
	f.w.CollectID = "c0000002"
	if err := f.ws.WriteJSON(f.dir, workspace.CollectFile, f.w); err != nil {
		t.Fatal(err)
	}
	code, out := f.execute(t, context.Background(), f.fake.open, file)
	if code != exitRefused || len(out.Databases) != 1 || !strings.Contains(out.Databases[0].Error, "collect ran again") {
		t.Fatalf("code %d, output %+v, want refused because collect ran again", code, out)
	}
	if got := f.sent("CREATE"); len(got) > 0 {
		t.Errorf("statements sent: %q", got)
	}
}

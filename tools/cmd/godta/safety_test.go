package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// evalFix is a collected database "app" of server lab01 with one candidate,
// in a fresh project, and a lenient fake server that can run its evaluate
// plan.
type evalFix struct {
	args []string // --project and the connection flags
	ws   workspace.WS
	dir  string
	w    *model.Workload
	tbl  *model.Table
	fake *fakeServer
}

func newEvalFix(t *testing.T) *evalFix {
	t.Helper()
	t.Setenv(testPasswordEnv, "pw")
	f := &evalFix{args: baseArgs(t, writeProfiles(t), "rw")}
	ws, err := workspace.Open(f.args[1])
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws
	inst := conn.Instance{ServerName: "lab01", MachineName: "LABM", Edition: "Developer Edition (64-bit)",
		EngineEdition: 3, Major: 16, Level: "RTM", Version: "16.0.4250.1"}
	f.tbl = &model.Table{ObjectID: 1001, Schema: "dbo", Name: "godta_t", Rows: 1000,
		Columns: []model.Column{{ID: 1, Name: "id", Type: "int", AvgWidth: 4}, {ID: 2, Name: "a", Type: "int", AvgWidth: 4}, {ID: 3, Name: "b", Type: "int", AvgWidth: 4}},
		Indexes: []model.Index{{ObjectID: 1001, IndexID: 1, Name: "pk", Kind: model.KindClustered, Keys: []model.KeyCol{{ColumnID: 1}}, Unique: true, PrimaryKey: true}}}
	f.w = &model.Workload{Server: inst, Database: model.DBInfo{ID: 5, Name: "app"}, Source: model.SourceQueryStore,
		Tables: map[int]*model.Table{1001: f.tbl}}
	f.dir = ws.DBDir("lab01", "app")
	if err := ws.WriteJSON(f.dir, workspace.CollectFile, f.w); err != nil {
		t.Fatal(err)
	}
	f.writeCandidates(t, f.cand(2, 3))
	f.fake = &fakeServer{inst: inst, tables: []*model.Table{f.tbl}, lenient: true, dbName: "app"}
	return f
}

// cand is a new candidate (key) INCLUDE (include) on godta_t for query 1.
func (f *evalFix) cand(key, include int) model.Candidate {
	c := model.Candidate{Table: 1001, Keys: []model.KeyCol{{ColumnID: key}}, Include: []int{include}, Origin: candidate.OriginAlgorithm,
		Queries: []int64{1}, Decision: model.DecisionNew}
	c.ID = candidate.ID(c)
	return c
}

func (f *evalFix) writeCandidates(t *testing.T, cands ...model.Candidate) {
	t.Helper()
	if err := f.ws.WriteJSON(f.dir, workspace.CandidatesFile, cands); err != nil {
		t.Fatal(err)
	}
}

// dryRun runs evaluate --dry-run and returns the plan it wrote.
func (f *evalFix) dryRun(t *testing.T) (string, runplan.Plan) {
	t.Helper()
	code, out := invoke(t, f.fake.open, append([]string{"evaluate", "--target", "server", "--dry-run"}, f.args...)...)
	if code != exitOK || out.Plan == "" {
		t.Fatalf("dry-run: code %d, output %+v", code, out)
	}
	var plan runplan.Plan
	data, err := os.ReadFile(out.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	return out.Plan, plan
}

// execute runs evaluate --allow-write --plan file under ctx.
func (f *evalFix) execute(t *testing.T, ctx context.Context, open opener, file string) (int, output) {
	t.Helper()
	return invokeCtx(t, ctx, open, append([]string{"evaluate", "--target", "server", "--allow-write", "--plan", file}, f.args...)...)
}

// invokeCtx is invoke under ctx.
func invokeCtx(t *testing.T, ctx context.Context, open opener, args ...string) (int, output) {
	t.Helper()
	var buf bytes.Buffer
	d := deps{stdout: &buf, open: open, resolve: sqlq.NewResolver(os.Getenv, "", func(string) {}).Resolve}
	code := run(ctx, args, d)
	var out output
	dec := json.NewDecoder(&buf)
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, buf.String())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("more than one JSON object on stdout: %s", buf.String())
	}
	return code, out
}

func (f *evalFix) sent(prefix string) []string {
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	var out []string
	for _, q := range f.fake.execs {
		if strings.HasPrefix(q, prefix) {
			out = append(out, q)
		}
	}
	return out
}

func hasWarning(r dbResult, subs ...string) bool {
	return slices.ContainsFunc(r.Warnings, func(w string) bool {
		for _, s := range subs {
			if !strings.Contains(w, s) {
				return false
			}
		}
		return true
	})
}

// TestEvaluateLeftoverFails: every drop of the hypothetical index fails on
// its lock timeout (error 1222). The index stays "created" in the journal,
// so the run is not a success: exit 2, ok false, the status of the database
// says objects are left, and a warning names the index and says to run
// cleanup.
func TestEvaluateLeftoverFails(t *testing.T) {
	f := newEvalFix(t)
	file, plan := f.dryRun(t)
	f.fake.failExec = func(q string) error {
		if strings.HasPrefix(q, "DROP INDEX") {
			return mssql.Error{Number: 1222, Message: "Lock request time out period exceeded."}
		}
		return nil
	}
	code, out := f.execute(t, context.Background(), f.fake.open, file)
	if n := len(f.sent("DROP INDEX")); n != 3 {
		t.Errorf("%d DROP INDEX sent, want 3 attempts", n)
	}
	name := "godta_" + plan.RunID + "_1"
	if code != exitFailed || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, output %+v; want code 2, ok false, one database", code, out)
	}
	r := out.Databases[0]
	if !strings.Contains(r.Status, "left for cleanup") {
		t.Errorf("status %q, want it to say objects are left for cleanup", r.Status)
	}
	if !hasWarning(r, name, "godta cleanup") {
		t.Errorf("warnings %q, want one naming %s and telling to run godta cleanup", r.Warnings, name)
	}
}

// TestEvaluateInterruptedStillDrops: the context is canceled (SIGINT,
// SIGTERM) as the creation of the hypothetical index returns, so the
// measuring that follows fails on it. The drop is
// still sent, on a context of its own, and journaled "removed"; the run says
// it was interrupted: exit 2, ok false.
func TestEvaluateInterruptedStillDrops(t *testing.T) {
	f := newEvalFix(t)
	file, _ := f.dryRun(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fake.onExec = func(q string) {
		if strings.HasPrefix(q, "CREATE") {
			cancel()
		}
	}
	code, out := f.execute(t, ctx, f.fake.open, file)
	if len(f.sent("CREATE")) != 1 || len(f.sent("DROP INDEX")) != 1 {
		t.Errorf("CREATE sent %d times, DROP INDEX %d times; want 1 and 1", len(f.sent("CREATE")), len(f.sent("DROP INDEX")))
	}
	pending, err := f.ws.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(pending, func(e workspace.Entry) bool { return e.State == workspace.StateCreated }) {
		t.Errorf("journal pending %+v: the index was not journaled removed", pending)
	}
	if code != exitFailed || out.OK || len(out.Databases) != 1 || !strings.Contains(out.Databases[0].Error, "interrupted") {
		t.Errorf("code %d, output %+v; want code 2, ok false, the database failed as interrupted", code, out)
	}
}

// TestSignalsCancel: the context of main is canceled by SIGINT and by
// SIGTERM, instead of the process dying with the indexes on the server.
func TestSignalsCancel(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		ctx, stop := signalContext()
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Errorf("%v: the context was not canceled", sig)
		}
		stop()
	}
}

// TestPlanReplayRefused: a plan whose run already has entries in the
// journal (a run killed with its indexes created) is refused before any
// connection, with exit 3 and a message that says to run cleanup and then a
// new dry-run.
func TestPlanReplayRefused(t *testing.T) {
	f := newEvalFix(t)
	file, plan := f.dryRun(t)
	var create runplan.Step
	for _, st := range plan.Steps {
		if st.Journal != nil && st.Resolve != "" {
			create = st
		}
	}
	if create.Journal == nil {
		t.Fatalf("no creation step in %+v", plan.Steps)
	}
	for _, state := range []string{workspace.StatePlanned, workspace.StateCreated} {
		e := *create.Journal
		e.State, e.At = state, time.Now().UTC()
		if err := f.ws.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	code, out := f.execute(t, context.Background(), noOpen(t), file)
	if code != exitRefused || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, output %+v; want code 3, ok false, one database", code, out)
	}
	r := out.Databases[0]
	for _, want := range []string{plan.RunID, "cleanup", "--dry-run"} {
		if !strings.Contains(r.Error, want) {
			t.Errorf("error %q does not contain %q", r.Error, want)
		}
	}
}

// TestCloneReplayRefused: the same for a clone plan.
func TestCloneReplayRefused(t *testing.T) {
	f := newEvalFix(t)
	plan := runplan.ForClone(f.w, []model.Candidate{f.cand(2, 3)}, workspace.NewRunID(), nil, "/var/opt/mssql/backup", false, 1<<20)
	plan.Server, plan.MachineName = "lab01", "LABM"
	plan = plan.Seal()
	if err := f.ws.WriteJSON(f.dir, workspace.PlanFile(runplan.PhaseClone), plan); err != nil {
		t.Fatal(err)
	}
	if err := f.ws.Record(workspace.Entry{RunID: plan.RunID, Server: "lab01", Database: "app", Kind: workspace.KindStatistics,
		Schema: "dbo", Table: "godta_t", Name: "godta_" + plan.RunID + "_s1", State: workspace.StatePlanned, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.dir, workspace.PlanFile(runplan.PhaseClone))
	code, out := invoke(t, noOpen(t), append([]string{"clone", "--allow-write", "--plan", file, "--host-dir", t.TempDir()}, f.args...)...)
	if code != exitRefused || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, output %+v; want code 3, ok false, one database", code, out)
	}
	for _, want := range []string{plan.RunID, "cleanup", "--dry-run"} {
		if !strings.Contains(out.Databases[0].Error, want) {
			t.Errorf("error %q does not contain %q", out.Databases[0].Error, want)
		}
	}
}

// TestSupersededPlanRefused: a plan approved, then replaced by a newer
// --dry-run (here with one more candidate), is refused with exit 3 before any
// connection; the message names the current plan and says the approved one
// was replaced.
func TestSupersededPlanRefused(t *testing.T) {
	f := newEvalFix(t)
	file, old := f.dryRun(t)
	approved := filepath.Join(t.TempDir(), "approved-plan.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approved, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f.writeCandidates(t, f.cand(2, 3), f.cand(3, 2))
	current, cur := f.dryRun(t)
	code, out := f.execute(t, context.Background(), noOpen(t), approved)
	if code != exitRefused || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, output %+v; want code 3, ok false, one database", code, out)
	}
	msg := out.Databases[0].Error
	for _, want := range []string{old.RunID, cur.RunID, current, "replaced"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
}

// TestCleanupLeftoverReported: cleanup cannot drop a journaled hypothetical
// index (lock timeout on every attempt). The run fails, and the database of
// the index is in the output with a status that says an object is left and
// a warning that names it.
func TestCleanupLeftoverReported(t *testing.T) {
	f := newEvalFix(t)
	name := "godta_abcd1234_1"
	if err := f.ws.Record(workspace.Entry{RunID: "abcd1234", Server: "lab01", Database: "app", Kind: workspace.KindHypotheticalIndex,
		Schema: "dbo", Table: "godta_t", Name: name, State: workspace.StateCreated, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	code, out := invoke(t, f.fake.open, append([]string{"cleanup", "--dry-run"}, f.args...)...)
	if code != exitOK || out.Plan == "" {
		t.Fatalf("cleanup --dry-run: code %d, output %+v", code, out)
	}
	f.fake.failExec = func(q string) error {
		if strings.HasPrefix(q, "DROP INDEX") {
			return mssql.Error{Number: 1222, Message: "Lock request time out period exceeded."}
		}
		return nil
	}
	code, out = invoke(t, f.fake.open, append([]string{"cleanup", "--allow-write", "--plan", out.Plan}, f.args...)...)
	if len(f.sent("DROP INDEX")) == 0 {
		t.Fatalf("no DROP INDEX sent: %q", f.fake.execs)
	}
	if code != exitFailed || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, output %+v; want code 2, ok false, one database", code, out)
	}
	r := out.Databases[0]
	if r.Name != "app" || !strings.Contains(r.Status, "1 object left") || !hasWarning(r, name, "godta cleanup") {
		t.Errorf("database %+v: want app, a status with 1 object left, a warning naming %s and cleanup", r, name)
	}
}

// TestNoteLeftCountsCreatedOfRun: noteLeft, which ends clone, evaluate and
// the lab mode, counts the objects of the run whose last state is
// "created": not those removed, not those of another run, not a temporary
// procedure. Here the clone of a run left one statistics object and dropped
// its clone database.
func TestNoteLeftCountsCreatedOfRun(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	stats := workspace.Entry{RunID: "run00001", Server: "s", Database: "app", Kind: workspace.KindStatistics, Schema: "dbo", Table: "t", Name: "godta_run00001_s1"}
	clone := workspace.Entry{RunID: "run00001", Server: "s", Database: "master", Kind: workspace.KindCloneDatabase, Name: "godta_run00001_c"}
	proc := workspace.Entry{RunID: "run00001", Server: "s", Database: "app", Kind: workspace.KindTempProcedure, Name: "#godta_run00001_r1"}
	other := workspace.Entry{RunID: "run00002", Server: "s", Database: "app", Kind: workspace.KindStatistics, Schema: "dbo", Table: "t", Name: "godta_run00002_s1"}
	for _, e := range []workspace.Entry{stats, clone, proc, other} {
		for _, state := range []string{workspace.StatePlanned, workspace.StateCreated} {
			e.State, e.At = state, at
			if err := ws.Record(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	clone.State = workspace.StateRemoved
	if err := ws.Record(clone); err != nil {
		t.Fatal(err)
	}
	a := &app{ws: ws}
	r := dbResult{Status: "cloned: 100 bytes"}
	a.noteLeft(&r, "run00001")
	if !a.failed || r.Status != "cloned: 100 bytes, 1 object left for cleanup" || len(r.Warnings) != 1 ||
		!hasWarning(r, "statistics dbo.t.godta_run00001_s1", "godta cleanup") ||
		hasWarning(r, "godta_run00001_c") || hasWarning(r, "#godta") || hasWarning(r, "run00002") {
		t.Errorf("failed %v, result %+v; want failed, one object left, a warning naming the statistics alone", a.failed, r)
	}
	a = &app{ws: ws}
	r = dbResult{Status: "evaluated"}
	a.noteLeft(&r, "run00003")
	if a.failed || r.Status != "evaluated" || len(r.Warnings) != 0 {
		t.Errorf("a run with nothing in the journal: failed %v, result %+v; want unchanged", a.failed, r)
	}
}

// editPlan rewrites the plan file with one more side effect and without
// sealing it again: a change that no other guard than the checksum sees.
func editPlan(t *testing.T, file string) {
	t.Helper()
	var p runplan.Plan
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	p.SideEffects = append(p.SideEffects, "edited after the dry run")
	if data, err = json.Marshal(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// refusals are the changes after a dry run that runplan.Verify must refuse
// at execution, each with what its message names.
var refusals = []struct {
	name   string
	change func(t *testing.T, f *evalFix, file string)
	want   string
}{
	{"edited plan", func(t *testing.T, _ *evalFix, file string) { editPlan(t, file) }, "checksum"},
	{"other machine", func(_ *testing.T, f *evalFix, _ string) { f.fake.inst.MachineName = "OTHERM" }, "wrong server"},
}

// TestEvaluateRefusedByVerify: evaluate --allow-write --plan refuses, with
// exit 3 and before any statement that writes, a plan edited after its dry
// run, a plan for another machine, and a plan whose table is gone since.
func TestEvaluateRefusedByVerify(t *testing.T) {
	cases := append(slices.Clone(refusals), struct {
		name   string
		change func(t *testing.T, f *evalFix, file string)
		want   string
	}{"schema changed", func(_ *testing.T, f *evalFix, _ string) { f.fake.tables = nil }, "schema changed"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEvalFix(t)
			file, _ := f.dryRun(t)
			c.change(t, f, file)
			code, out := f.execute(t, context.Background(), f.fake.open, file)
			if code != exitRefused || out.OK || len(out.Databases) != 1 {
				t.Fatalf("code %d, output %+v; want code 3, ok false, one database", code, out)
			}
			if r := out.Databases[0]; r.Status != "refused" || !strings.Contains(r.Error, c.want) {
				t.Errorf("database %+v: want refused on %q", r, c.want)
			}
			if sent := f.sent(""); len(sent) != 0 {
				t.Errorf("statements executed: %q, want none", sent)
			}
		})
	}
}

// TestCleanupPlanRefused: cleanup --allow-write --plan refuses with exit 3,
// the code of "refused, nothing written", and sends no drop, a cleanup plan
// edited after its dry run, made for another machine, or whose step was
// edited to drop a real index and sealed again.
func TestCleanupPlanRefused(t *testing.T) {
	resealed := struct {
		name   string
		change func(t *testing.T, f *evalFix, file string)
		want   string
	}{"step edited and sealed again", func(t *testing.T, _ *evalFix, file string) {
		var p runplan.Plan
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		p.Steps[0].SQL = "DROP INDEX [pk] ON [dbo].[godta_t]"
		if data, err = json.Marshal(p.Seal()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}, "not the cleanup of a goDTA object"}
	for _, c := range append(slices.Clone(refusals), resealed) {
		t.Run(c.name, func(t *testing.T) {
			f := newEvalFix(t)
			if err := f.ws.Record(workspace.Entry{RunID: "abcd1234", Server: "lab01", Database: "app", Kind: workspace.KindHypotheticalIndex,
				Schema: "dbo", Table: "godta_t", Name: "godta_abcd1234_1", State: workspace.StateCreated, At: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			code, out := invoke(t, f.fake.open, append([]string{"cleanup", "--dry-run"}, f.args...)...)
			if code != exitOK || out.Plan == "" {
				t.Fatalf("cleanup --dry-run: code %d, output %+v", code, out)
			}
			c.change(t, f, out.Plan)
			code, out = invoke(t, f.fake.open, append([]string{"cleanup", "--allow-write", "--plan", out.Plan}, f.args...)...)
			if code != exitRefused || out.OK || out.Error == nil || !strings.Contains(out.Error.Message, c.want) {
				t.Errorf("code %d, output %+v; want code 3, refused on %q", code, out, c.want)
			}
			if sent := f.sent(""); len(sent) != 0 {
				t.Errorf("statements executed: %q, want none", sent)
			}
		})
	}
}

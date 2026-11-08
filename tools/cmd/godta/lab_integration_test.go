package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/evaluate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/report"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/score"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// labQueries are the four queries of the spike on godta_t (spec section 15,
// point 2: correlated columns a and b, and c), plus the query of godta_h
// that reads the real index nix_h_x, whose declaration under AUTOPILOT on
// the lab depends on the plan's table being recognized there.
var labQueries = []string{
	`SELECT id FROM dbo.godta_t WHERE a = 0 AND b = 1;`,
	`SELECT id FROM dbo.godta_t WHERE a = 933 AND b = 9332;`,
	`SELECT id FROM dbo.godta_t WHERE a = 0 AND b = 9332;`,
	`SELECT id FROM dbo.godta_t WHERE c = '123';`,
	`SELECT y FROM dbo.godta_h WHERE x = 42;`,
}

// labRun is one source database loaded with the data set and labQueries,
// the lab, and a project to run the binary's phases in.
type labRun struct {
	t                     *testing.T
	ctx                   context.Context
	source, db, labServer *sql.DB
	name                  string
	project               string
	ws                    workspace.WS
	common                []string
	labArgs               []string
	hostDir               string
	serverName            string
}

func newLabRun(t *testing.T) *labRun {
	t.Helper()
	hostDir, containerDir := os.Getenv("GODTA_TEST_LAB_HOSTDIR"), os.Getenv("GODTA_TEST_LAB_CONTAINERDIR")
	if hostDir == "" || containerDir == "" {
		t.Skip("GODTA_TEST_LAB_HOSTDIR and GODTA_TEST_LAB_CONTAINERDIR not set")
	}
	lp, lr := ittest.Profile(t, "GODTA_TEST_LAB_PROFILE")
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	t.Cleanup(cancel)
	lr0 := &labRun{t: t, ctx: ctx, hostDir: hostDir}
	lr0.source = ittest.OpenDB(t, p, r, "")
	lr0.labServer = ittest.OpenDB(t, lp, lr, "")
	lr0.name = ittest.NewDatabase(t, lr0.source, "")
	lr0.db = ittest.OpenDB(t, p, r, lr0.name)
	ittest.LoadDataset(t, lr0.db)
	// A first pass creates the single-column automatic statistics on the
	// source, as production queries would have long before the window.
	lr0.runQueries(1)
	// The Query Store captured the load of the data set and the first
	// pass: the load is a write of the window that drives the maintenance
	// of every candidate of the table above the threshold (TestEndToEndServer).
	if _, err := lr0.source.ExecContext(ctx, "ALTER DATABASE ["+lr0.name+"] SET QUERY_STORE CLEAR;"); err != nil {
		t.Fatal(err)
	}
	lr0.runQueries(3)
	if _, err := lr0.db.ExecContext(ctx, "EXEC sys.sp_query_store_flush_db;"); err != nil {
		t.Fatal(err)
	}
	lr0.project = filepath.Join(t.TempDir(), "project")
	lr0.ws = workspace.WS{Root: lr0.project}
	profiles := os.Getenv("GODTA_TEST_PROFILES")
	lr0.common = []string{"--project", lr0.project, "--profiles", profiles,
		"--profile", os.Getenv("GODTA_TEST_PROFILE"), "--env", filepath.Join(t.TempDir(), "absent.env"),
		"--include", lr0.name}
	lr0.labArgs = []string{"evaluate", "--target", "lab", "--lab-profile", os.Getenv("GODTA_TEST_LAB_PROFILE"),
		"--host-dir", hostDir, "--container-dir", containerDir, "--allow-write"}
	return lr0
}

func (lr *labRun) runQueries(n int) {
	lr.t.Helper()
	for range n {
		for _, q := range labQueries {
			rows, err := lr.db.QueryContext(lr.ctx, q)
			if err != nil {
				lr.t.Fatalf("%s: %v", q, err)
			}
			for rows.Next() {
			}
			if err := rows.Err(); err != nil {
				lr.t.Fatalf("%s: %v", q, err)
			}
			rows.Close()
		}
	}
}

// step runs one phase through run and requires exit code 0.
func (lr *labRun) step(args ...string) output {
	lr.t.Helper()
	var buf bytes.Buffer
	code := run(lr.ctx, append(args, lr.common...), realDepsTo(&buf))
	var out output
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		lr.t.Fatalf("%s: stdout is not JSON: %v\n%s", args[0], err, buf.String())
	}
	if code != exitOK || !out.OK {
		lr.t.Fatalf("%s: code %d\n%s", strings.Join(args, " "), code, buf.String())
	}
	return out
}

func readJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// collectAndClone runs collect and candidates, then clone --dry-run and
// clone --allow-write, and registers the removal of what the clone may
// leave: on the source, on the lab, in the host directory.
func (lr *labRun) collectAndClone() (dir string) {
	t := lr.t
	out := lr.step("collect")
	if len(out.Databases) != 1 || out.Databases[0].Status != "collected" {
		t.Fatalf("collect: %+v", out.Databases)
	}
	lr.serverName = out.Databases[0].Server
	dir = lr.ws.DBDir(lr.serverName, lr.name)
	lr.step("candidates")
	lr.cloneOnly(dir)
	return dir
}

// backupDirOf is the directory of the BACKUP step of a clone plan.
func backupDirOf(p runplan.Plan) string {
	for _, s := range p.Steps {
		if s.Journal != nil && s.Journal.Kind == workspace.KindBackupFile {
			return s.Journal.Path[:strings.LastIndexAny(s.Journal.Path, `/\`)+1]
		}
	}
	return ""
}

func dropDBOnCleanup(t *testing.T, server *sql.DB, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		q := tsql.QuoteName(name)
		if _, err := server.ExecContext(ctx, "IF DB_ID("+tsql.QuoteString(name)+") IS NOT NULL BEGIN ALTER DATABASE "+q+
			" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE "+q+"; END"); err != nil {
			t.Errorf("dropping %s: %v", name, err)
		}
	})
}

// measureKey identifies a measure across the two evaluations.
func measureKey(m model.Measure) string {
	if len(m.Combined) > 0 {
		return fmt.Sprintf("%d|combined %s", m.QueryID, strings.Join(m.Combined, "+"))
	}
	return fmt.Sprintf("%d|%s", m.QueryID, m.Candidate)
}

// runName is the run part of a goDTA object name: the hypothetical indexes
// of the two evaluations are of two runs.
var runName = regexp.MustCompile(`godta_[a-z0-9]{8}_`)

func sameRows(a, b float64) bool {
	return a == b || math.Abs(a-b) < 1e-6*math.Max(math.Abs(a), math.Abs(b))
}

// TestLabEqualsServer: the same candidates measured on the source (evaluate
// --target server) and on the clone restored on the lab, another instance
// (evaluate --target lab), give for every (query, candidate) the same status,
// the same Before and After costs (showplan.SameCost), the same root row
// estimates and the same plan shape (plan, task 19; spec section 15, "still
// open": 2017 and 2022 had not been measured with the multicolumn statistics
// set on the hypothetical index then dropped from the lab).
func TestLabEqualsServer(t *testing.T) {
	lr := newLabRun(t)
	out := lr.step("collect")
	if len(out.Databases) != 1 || out.Databases[0].Status != "collected" {
		t.Fatalf("collect: %+v", out.Databases)
	}
	lr.serverName = out.Databases[0].Server
	dir := lr.ws.DBDir(lr.serverName, lr.name)
	lr.step("candidates")
	out = lr.step("evaluate", "--target", "server", "--dry-run")
	lr.step("evaluate", "--target", "server", "--allow-write", "--plan", out.Plan)
	// collect and candidates again would draw the same files; clone reads
	// candidates.json, which the server evaluation did not change.
	lr.cloneOnly(dir)
	lr.step(lr.labArgs...)

	var srv, labEv evaluateFile
	readJSONFile(t, filepath.Join(dir, workspace.EvaluateServerFile), &srv)
	readJSONFile(t, filepath.Join(dir, workspace.EvaluateLabFile), &labEv)
	w := &model.Workload{}
	readJSONFile(t, filepath.Join(dir, workspace.CollectFile), w)
	if w.CollectID == "" || labEv.CollectID != w.CollectID {
		t.Errorf("collect_id: collect.json %q, evaluate-lab.json %q, want the same", w.CollectID, labEv.CollectID)
	}
	var cands []model.Candidate
	readJSONFile(t, filepath.Join(dir, labCandidatesFile), &cands)
	defs := map[string]string{}
	for _, c := range cands {
		defs[c.ID] = candDef(w, c)
	}
	texts := map[int64]string{}
	for _, q := range slices.Concat(w.Queries, w.Writes) {
		texts[q.QueryID] = q.Text
	}
	t.Logf("server warnings %q; lab warnings %q; lab auto-created statistics %q", srv.Warnings, labEv.Warnings, labEv.AutoCreatedStats)

	byKey := func(ms []model.Measure) map[string]model.Measure {
		out := map[string]model.Measure{}
		for _, m := range ms {
			out[measureKey(m)] = m
		}
		return out
	}
	s, l := byKey(srv.Measures), byKey(labEv.Measures)
	var keys []string
	for k := range s {
		keys = append(keys, k)
	}
	for k := range l {
		if _, ok := s[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	measuredT := map[int64]bool{}
	measuredH := map[int64]bool{}
	tid, hid := tableID(w, "godta_t"), tableID(w, "godta_h")
	var table strings.Builder
	table.WriteString("\n| query | candidate | status server / lab | before server | before lab | after server | after lab | rows before server / lab | rows after server / lab |")
	for _, k := range keys {
		ms, okS := s[k]
		ml, okL := l[k]
		m := ms
		if !okS {
			m = ml
		}
		cand := m.Candidate
		if cand != "" {
			cand += " " + defs[cand]
		} else {
			cand = "combined " + strings.Join(m.Combined, "+")
		}
		fmt.Fprintf(&table, "\n| %d %s | %s | %s / %s | %.6f | %.6f | %.6f | %.6f | %.1f / %.1f | %.1f / %.1f |",
			m.QueryID, excerpt(texts[m.QueryID]), cand, ms.Status, ml.Status, ms.Before, ml.Before, ms.After, ml.After,
			ms.BeforeRows, ml.BeforeRows, ms.AfterRows, ml.AfterRows)
		switch {
		case !okS:
			t.Errorf("%s: measured on the lab only", k)
			continue
		case !okL:
			t.Errorf("%s: measured on the server only", k)
			continue
		}
		if ms.Status != ml.Status {
			t.Errorf("%s: status %q on the server, %q on the lab (lab error %q)", k, ms.Status, ml.Status, ml.Error)
			continue
		}
		if ms.Status != evaluate.StatusMeasured {
			continue
		}
		if !showplan.SameCost(ms.Before, ml.Before) || !showplan.SameCost(ms.After, ml.After) {
			t.Errorf("%s: costs before %g / %g, after %g / %g (server / lab)", k, ms.Before, ml.Before, ms.After, ml.After)
		}
		if !sameRows(ms.BeforeRows, ml.BeforeRows) || !sameRows(ms.AfterRows, ml.AfterRows) {
			t.Errorf("%s: row estimates before %g / %g, after %g / %g (server / lab)", k, ms.BeforeRows, ml.BeforeRows, ms.AfterRows, ml.AfterRows)
		}
		if runName.ReplaceAllString(ms.Shape, "godta_<run>_") != runName.ReplaceAllString(ml.Shape, "godta_<run>_") {
			t.Errorf("%s: shape %s on the server, %s on the lab", k, ms.Shape, ml.Shape)
		}
		for _, c := range cands {
			if c.ID == m.Candidate && c.Table == tid {
				measuredT[m.QueryID] = true
			}
			if c.ID == m.Candidate && c.Table == hid {
				measuredH[m.QueryID] = true
			}
		}
	}
	t.Log(table.String())
	// Not "nothing differs because nothing was measured": the four queries
	// of godta_t and the one of godta_h are measured with a candidate.
	if len(measuredT) < 4 || len(measuredH) < 1 {
		t.Errorf("measured queries: %d of godta_t, %d of godta_h; want 4 and 1", len(measuredT), len(measuredH))
	}

	// A second evaluate --target lab finds the lab prepared: no RESTORE,
	// CheckCloneDB, and the same measures.
	lr.step(lr.labArgs...)
	var again evaluateFile
	readJSONFile(t, filepath.Join(dir, workspace.EvaluateLabFile), &again)
	a := byKey(again.Measures)
	if len(a) != len(l) {
		t.Errorf("second lab evaluation: %d measures, the first %d", len(a), len(l))
	}
	for k, m := range l {
		if am, ok := a[k]; !ok || am.Status != m.Status || !showplan.SameCost(am.Before, m.Before) || !showplan.SameCost(am.After, m.After) {
			t.Errorf("%s: second lab evaluation %+v, first %+v", k, am, m)
		}
	}

	out = lr.step("cleanup", "--dry-run")
	var cplan runplan.Plan
	readJSONFile(t, out.Plan, &cplan)
	if len(cplan.Steps) != 0 {
		t.Errorf("cleanup --dry-run on the source found leftovers: %+v", cplan.Steps)
	}
	lr.checkSourceClean()
}

// cloneOnly runs clone --dry-run and --allow-write after collect and
// candidates, and returns the output of the execution.
func (lr *labRun) cloneOnly(dir string) output {
	t := lr.t
	out := lr.step("clone", "--dry-run")
	var plan runplan.Plan
	readJSONFile(t, out.Plan, &plan)
	if !slices.ContainsFunc(plan.SideEffects, func(s string) bool { return strings.HasPrefix(s, "Size estimate") }) {
		t.Errorf("the clone plan gives no size estimate: %q", plan.SideEffects)
	}
	clone := runplan.CloneDatabaseName(plan.RunID)
	dropDBOnCleanup(t, lr.source, clone)
	dropDBOnCleanup(t, lr.labServer, clone)
	t.Cleanup(func() { os.Remove(filepath.Join(lr.hostDir, clone+".bak")) })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		path := runplan.BackupPath(backupDirOf(plan), plan.RunID)
		var file, d, parent int
		if err := lr.source.QueryRowContext(ctx, "EXEC master.dbo.xp_fileexist "+tsql.QuoteString(path)).Scan(&file, &d, &parent); err == nil && file == 1 {
			if _, err := lr.source.ExecContext(ctx, tsql.DeleteBackupFile(path)); err != nil {
				t.Errorf("deleting %s: %v", path, err)
			}
		}
	})
	done := lr.step("clone", "--allow-write", "--plan", out.Plan, "--host-dir", lr.hostDir)
	st, err := lab.LoadState(lr.ws, dir)
	if err != nil || st.File.HostPath == "" {
		t.Fatalf("lab.json after clone: %+v, %v", st, err)
	}
	return done
}

// checkSourceClean: no statistics, index or database of goDTA's run form
// left on the source.
func (lr *labRun) checkSourceClean() {
	t := lr.t
	var left int
	if err := lr.db.QueryRowContext(lr.ctx, `SELECT (SELECT COUNT(*) FROM sys.indexes WHERE name LIKE @p1)
  + (SELECT COUNT(*) FROM sys.stats WHERE name LIKE @p1)
  + (SELECT COUNT(*) FROM sys.databases WHERE name LIKE @p1)`, tsql.RunPattern()).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d objects named LIKE %s left on the source", left, tsql.RunPattern())
	}
}

func tableID(w *model.Workload, name string) int {
	for id, t := range w.Tables {
		if t.Name == name {
			return id
		}
	}
	return 0
}

// candDef is the keys and includes of c by column name.
func candDef(w *model.Workload, c model.Candidate) string {
	t := w.Tables[c.Table]
	if t == nil {
		return "?"
	}
	name := func(id int) string {
		for _, col := range t.Columns {
			if col.ID == id {
				return col.Name
			}
		}
		return "?"
	}
	var keys, inc []string
	for _, k := range c.Keys {
		keys = append(keys, name(k.ColumnID))
	}
	for _, id := range c.Include {
		inc = append(inc, name(id))
	}
	s := t.Name + "(" + strings.Join(keys, ",") + ")"
	if len(inc) > 0 {
		s += " INCLUDE (" + strings.Join(inc, ",") + ")"
	}
	return s
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ")SELECT"); i >= 0 && strings.HasPrefix(s, "(") {
		s = s[i+1:]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// TestLateSuggestionUnreliable: a suggestion (b, c) that arrives after the
// clone has no statistics on the lab. evaluate --target lab --suggestions
// measures it with the note "unreliable estimate" on each of its measures,
// and the report of the lab evaluation does not recommend it (spec section
// 8.4).
func TestLateSuggestionUnreliable(t *testing.T) {
	lr := newLabRun(t)
	dir := lr.collectAndClone()

	w := &model.Workload{}
	readJSONFile(t, filepath.Join(dir, workspace.CollectFile), w)
	var before []model.Candidate
	readJSONFile(t, filepath.Join(dir, workspace.CandidatesFile), &before)
	tid := tableID(w, "godta_t")
	tbl := w.Tables[tid]
	keys := []model.KeyCol{{ColumnID: candidate.ColumnByName(w, tbl, "b")}, {ColumnID: candidate.ColumnByName(w, tbl, "c")}}
	for _, c := range before {
		if c.Table == tid && len(c.Keys) >= 2 && c.Keys[0].ColumnID == keys[0].ColumnID && c.Keys[1].ColumnID == keys[1].ColumnID {
			t.Fatalf("(b, c) is already a candidate before the suggestion: %+v; the test needs a suggestion the clone did not prepare", c)
		}
	}
	sugg := filepath.Join(t.TempDir(), "suggestions.json")
	if err := os.WriteFile(sugg, []byte(`{"suggestions": [{"table": "dbo.godta_t", "keys": [{"column": "b"}, {"column": "c"}], "reason": "late suggestion of the test"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lr.step(append(slices.Clone(lr.labArgs), "--suggestions", sugg)...)

	st, err := lab.LoadState(lr.ws, dir)
	if err != nil || st.Prepared == nil {
		t.Fatalf("lab.json after evaluate --target lab: %+v, %v", st, err)
	}
	if _, ok := st.Prepared.Streams[lab.StreamKey(tid, keys)]; ok {
		t.Fatalf("the lab has a stream for (b, c): the suggestion is not late")
	}
	var cands []model.Candidate
	readJSONFile(t, filepath.Join(dir, labCandidatesFile), &cands)
	var id string
	for _, c := range cands {
		if c.Origin == "ai" && c.Table == tid && len(c.Keys) == 2 && c.Keys[0].ColumnID == keys[0].ColumnID && c.Keys[1].ColumnID == keys[1].ColumnID {
			id = c.ID
		}
	}
	if id == "" {
		t.Fatalf("no candidate (b, c) of origin ai among %d candidates", len(cands))
	}
	var ev evaluateFile
	readJSONFile(t, filepath.Join(dir, workspace.EvaluateLabFile), &ev)
	n := 0
	for _, m := range ev.Measures {
		if m.Candidate != id {
			continue
		}
		n++
		t.Logf("query %d: status %s, before %.6f after %.6f, notes %q", m.QueryID, m.Status, m.Before, m.After, m.Notes)
		if !slices.Contains(m.Notes, lab.NoteStatsNotTransferred) {
			t.Errorf("query %d: the measure of (b, c) has notes %q, without %q", m.QueryID, m.Notes, lab.NoteStatsNotTransferred)
		}
	}
	if n == 0 {
		t.Fatalf("no measure of (b, c) in evaluate-lab.json")
	}

	lr.step("report")
	var res report.Result
	readJSONFile(t, filepath.Join(dir, report.ResultFile), &res)
	found := false
	for _, rec := range res.Recommendations {
		if rec.ID != id {
			continue
		}
		found = true
		g := "nil"
		if rec.Gain != nil {
			g = fmt.Sprintf("%.4f", *rec.Gain)
		}
		t.Logf("(b, c): section %s, decision %s, gain %s, notes %q", rec.Section, rec.Decision, g, rec.Notes)
		if rec.Decision == report.Recommended || rec.Section == report.Recommended {
			t.Errorf("the report recommends the late suggestion (b, c): %+v", rec)
		}
		// The reason, not only the outcome: (b, c) is also capped as made
		// redundant by the other candidates (measured), which alone would
		// keep it from being recommended.
		if !slices.Contains(rec.Notes, score.NoteUnreliable) {
			t.Errorf("the report's notes of (b, c) %q do not have %q", rec.Notes, score.NoteUnreliable)
		}
	}
	if !found {
		t.Errorf("the report has no entry for (b, c)")
	}
	lr.checkSourceClean()
}

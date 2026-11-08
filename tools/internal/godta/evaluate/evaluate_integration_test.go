package evaluate

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/collect"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/replay"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// The queries of the tests. Each runs as the application would, under the
// options of setOpts.
const (
	qSeek     = "SELECT y FROM dbo.godta_h WHERE x = 42"
	qCount    = "SELECT COUNT(*) FROM dbo.godta_h WHERE x < 3000"
	qScan     = "(@a int,@b int)SELECT c FROM dbo.godta_t WHERE a = @a AND b = @b"
	qJoin     = "SELECT t.c, h.y FROM dbo.godta_t t JOIN dbo.godta_h h ON h.x = t.a"
	qSort     = "SELECT TOP 10 c FROM dbo.godta_t WHERE a = 0 ORDER BY b DESC"
	qUpdate   = "UPDATE dbo.godta_t SET c = c + 'x' WHERE a = 500"
	qView     = "SELECT a, COUNT_BIG(*) FROM dbo.godta_t GROUP BY a"
	qViewJoin = "SELECT COUNT(*) FROM dbo.godta_t t JOIN dbo.godta_h h ON h.x = t.a"
	qNoStats  = "SELECT c FROM dbo.godta_t WHERE a = 0 AND b = 1"
	qInsert   = "INSERT dbo.godta_t (a, b, c) VALUES (1, 2, '3')"
	qDelete   = "DELETE dbo.godta_t WHERE id = 1"
	qMerge    = "MERGE dbo.godta_t AS t USING (SELECT 1 AS id) AS s ON t.id = s.id WHEN MATCHED THEN UPDATE SET c = 'm';"
)

// setOpts are ANSI_PADDING, CONCAT_NULL_YIELDS_NULL, ANSI_WARNINGS,
// ANSI_NULLS, QUOTED_IDENTIFIER and ARITHABORT on: the options an indexed
// view and a filtered index need.
const setOpts = 4347

type fixture struct {
	ctx    context.Context
	server *sql.DB
	db     *sql.DB
	name   string
	inst   conn.Instance
	w      *model.Workload
	ws     workspace.WS
	next   int64
}

// newFixture creates a throwaway database with the data set (annex A) and
// no workload: b and c of godta_t have no statistics, and pk_godta_t's are
// stale. extra runs after the data set, and tables names more tables for
// the catalog. The catalog is read without compiling anything.
func newFixture(t *testing.T, extra string, tables ...string) *fixture {
	t.Helper()
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	f := &fixture{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	f.ctx = ctx
	f.server = ittest.OpenDB(t, p, r, "")
	f.name = ittest.NewDatabase(t, f.server, "")
	f.db = ittest.OpenDB(t, p, r, f.name)
	ittest.LoadDataset(t, f.db)
	if extra != "" {
		if _, err := f.db.ExecContext(ctx, extra); err != nil {
			t.Fatalf("extra setup: %v", err)
		}
	}
	inst, err := conn.Identify(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.inst = inst
	f.w = &model.Workload{Server: inst, Tables: map[int]*model.Table{}}
	f.w.Database.Name = f.name
	if err := f.db.QueryRowContext(ctx, "SELECT DB_ID(), CAST(DATABASEPROPERTYEX(DB_NAME(), 'Collation') AS nvarchar(128))").
		Scan(&f.w.Database.ID, &f.w.Database.Collation); err != nil {
		t.Fatal(err)
	}
	all := append([]string{"godta_t", "godta_h", "godta_cs"}, tables...)
	f.w.Queries = []model.Query{{QueryID: -1, Plan: readOf(all...)}}
	if err := collect.ReadCatalog(ctx, f.db, f.w); err != nil {
		t.Fatal(err)
	}
	f.w.Queries = nil
	for _, n := range append(all, "godta_v") {
		if f.table(n) == nil {
			t.Fatalf("table %s not read from the catalog", n)
		}
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws
	return f
}

func (f *fixture) table(name string) *model.Table {
	return candidate.TableByName(f.w, "dbo", name)
}

func (f *fixture) col(table, name string) int {
	t := f.table(table)
	return candidate.ColumnByName(f.w, t, name)
}

// addQuery adds a query of the given text to the workload. Its plan is the
// list of the tables it touches, as collect would have read them from the
// Query Store; params are the compiled values of a parameterized text.
func (f *fixture) addQuery(text string, write bool, tables []string, params ...showplan.Param) model.Query {
	f.next++
	plan := readOf(tables...)
	plan.Statements[0].Params = params
	q := model.Query{QueryID: f.next, Text: text, SetOptions: setOpts, DateFirst: 7, DateFormat: 1, IsWrite: write, Plan: plan}
	if write {
		f.w.Writes = append(f.w.Writes, q)
	} else {
		f.w.Queries = append(f.w.Queries, q)
	}
	return q
}

func (f *fixture) session(t *testing.T, compileTimeout time.Duration) *Session {
	t.Helper()
	s, err := NewSession(f.ctx, f.db, f.inst, compileTimeout, 2000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func (f *fixture) cand(table string, keys []string, include []string, queries ...model.Query) model.Candidate {
	c := model.Candidate{Table: f.table(table).ObjectID, Origin: candidate.OriginAlgorithm, Decision: model.DecisionNew}
	for _, k := range keys {
		c.Keys = append(c.Keys, model.KeyCol{ColumnID: f.col(table, k)})
	}
	for _, i := range include {
		c.Include = append(c.Include, f.col(table, i))
	}
	slices.Sort(c.Include)
	for _, q := range queries {
		c.Queries = append(c.Queries, q.QueryID)
	}
	c.ID = candidate.ID(c)
	return c
}

// run builds and seals the plan of cands and runs it.
func (f *fixture) run(t *testing.T, s *Session, cands []model.Candidate, h Hooks) (Result, runplan.Plan) {
	t.Helper()
	plan := runplan.ForEvaluate(f.w, cands, workspace.NewRunID(), nil, 4, 2000).Seal()
	res, err := Run(f.ctx, s, f.ws, &plan, f.w, cands, h)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, plan
}

func (f *fixture) hypotheticals(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM sys.indexes WHERE is_hypothetical = 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) noneLeft(t *testing.T) {
	t.Helper()
	if n := f.hypotheticals(t); n != 0 {
		t.Errorf("%d hypothetical indexes left", n)
	}
	pending, err := f.ws.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("journal entries not removed: %+v", pending)
	}
}

func (f *fixture) reference(t *testing.T, s *Session, q model.Query) *ref {
	t.Helper()
	rf := &ref{q: q, write: q.IsWrite}
	if err := measureReference(f.ctx, s, f.w, rf, "it000000"); err != nil {
		t.Fatalf("reference of %q: %v", q.Text, err)
	}
	return rf
}

func measureOf(t *testing.T, res Result, cand string, q model.Query) model.Measure {
	t.Helper()
	for _, m := range res.Measures {
		if m.Candidate == cand && m.QueryID == q.QueryID {
			return m
		}
	}
	t.Fatalf("no measure of query %q for candidate %s in %+v", q.Text, cand, res.Measures)
	return model.Measure{}
}

func (f *fixture) build(s *Session, q model.Query) replay.Replay {
	return replay.Build(q, lastStatement(q.Plan), f.w, s.tempProc("it000000"))
}

// TestReferenceMatchesShowplan: for the six queries of task 4, in the order
// of Run (SHOWPLAN_XML first), the reference under AUTOPILOT has the shape
// and the cost of the plain SHOWPLAN_XML. no_stats comes first, on the
// fresh database: of the seven, it is the one whose cost moves when
// AUTOPILOT compiles before the statistics on b exist (scan_param, which
// also filters on b, keeps the same shape and cost in either order,
// measured), so it is what makes this test fail when the order of step 3 is
// reversed.
func TestReferenceMatchesShowplan(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	queries := []struct {
		name string
		q    model.Query
	}{
		{"no_stats", f.addQuery(qNoStats, false, []string{"godta_t"})},
		{"seek_lookup", f.addQuery(qSeek, false, []string{"godta_h"})},
		{"scan_param", f.addQuery(qScan, false, []string{"godta_t"},
			showplan.Param{Name: "@b", DataType: "int", CompiledValue: "(9332)"},
			showplan.Param{Name: "@a", DataType: "int", CompiledValue: "(933)"})},
		{"join", f.addQuery(qJoin, false, []string{"godta_t", "godta_h"})},
		{"sort", f.addQuery(qSort, false, []string{"godta_t"})},
		{"update", f.addQuery(qUpdate, true, []string{"godta_t"})},
		{"view", f.addQuery(qView, false, []string{"godta_t"})},
	}
	for _, c := range queries {
		rf := f.reference(t, s, c.q)
		if c.name == "scan_param" && rf.rp.Kind != replay.KindProc {
			t.Errorf("scan_param replayed as %q, want the proc kind (sp_executesql returns no inner plan under AUTOPILOT)", rf.rp.Kind)
		}
		t.Logf("%s: SHOWPLAN_XML %g, AUTOPILOT %g, status %q", c.name, rf.showplanCost, rf.before, rf.status)
		if rf.status != StatusMeasured {
			t.Errorf("%s: status %q (%s), want %q", c.name, rf.status, rf.errText, StatusMeasured)
		}
	}
}

// TestReferenceOrderMatters keeps the proof that step 3 must compile under
// SHOWPLAN_XML first: on a fresh database, b has no statistics, and AUTOPILOT
// compiled first estimates differently from the SHOWPLAN_XML that follows,
// which creates them; AUTOPILOT compiled after it agrees.
func TestReferenceOrderMatters(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	q := f.addQuery(qNoStats, false, []string{"godta_t"})
	rp := f.build(s, q)
	tables := []*model.Table{f.table("godta_t")}

	first, err := compileDeclared(f.ctx, s, rp, tables, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.Compile(f.ctx, rp, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := compileDeclared(f.ctx, s, rp, tables, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AUTOPILOT first: cost %g, rows %g; SHOWPLAN_XML: cost %g, rows %g; AUTOPILOT after: cost %g, rows %g",
		first.Cost, first.Root.EstimateRows, sp.Cost, sp.Root.EstimateRows, again.Cost, again.Root.EstimateRows)
	if showplan.SameCost(first.Cost, sp.Cost) && first.Root.EstimateRows == sp.Root.EstimateRows {
		t.Errorf("AUTOPILOT before SHOWPLAN_XML agrees with it (cost %g): the order no longer matters, or b has statistics", sp.Cost)
	}
	if !showplan.SameCost(again.Cost, sp.Cost) || showplan.Shape(*again) != showplan.Shape(*sp) {
		t.Errorf("AUTOPILOT after SHOWPLAN_XML: cost %g, want %g", again.Cost, sp.Cost)
	}
}

// TestHypotheticalIsMeasured: the candidate (x) INCLUDE (y) on godta_h makes
// x = 42 cheaper, the plan after reads it, and nothing is left behind.
func TestHypotheticalIsMeasured(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	q := f.addQuery(qSeek, false, []string{"godta_h"})
	c := f.cand("godta_h", []string{"x"}, []string{"y"}, q)
	res, _ := f.run(t, s, []model.Candidate{c}, Hooks{})
	m := measureOf(t, res, c.ID, q)
	t.Logf("Before %g, After %g, Status %q, UsedHypothetical %v, Shape %s", m.Before, m.After, m.Status, m.UsedHypothetical, m.Shape)
	if m.Status != StatusMeasured {
		t.Fatalf("status %q (%s), want measured", m.Status, m.Error)
	}
	if !(m.After < m.Before) {
		t.Errorf("After %g, want below Before %g", m.After, m.Before)
	}
	if !slices.Equal(m.UsedHypothetical, []string{c.ID}) {
		t.Errorf("UsedHypothetical %v, want [%s]", m.UsedHypothetical, c.ID)
	}
	f.noneLeft(t)
}

// TestRealIndexHiddenWhenNotDeclared: without DeclareReal, the reference of
// x = 42 loses its seek on nix_h_x.
func TestRealIndexHiddenWhenNotDeclared(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	q := f.addQuery(qSeek, false, []string{"godta_h"})
	rp := f.build(s, q)
	sp, err := s.Compile(f.ctx, rp, false)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := compileDeclared(f.ctx, s, rp, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	declared, err := compileDeclared(f.ctx, s, rp, []*model.Table{f.table("godta_h")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SHOWPLAN_XML %g, AUTOPILOT undeclared %g, declared %g", sp.Cost, hidden.Cost, declared.Cost)
	if !strings.Contains(showplan.Shape(*sp), "nix_h_x") {
		t.Fatalf("SHOWPLAN_XML does not seek nix_h_x: %s", showplan.Shape(*sp))
	}
	if strings.Contains(showplan.Shape(*hidden), "nix_h_x") {
		t.Errorf("undeclared, the plan still reads nix_h_x: %s", showplan.Shape(*hidden))
	}
	if showplan.Shape(*declared) != showplan.Shape(*sp) {
		t.Errorf("declared, shape %s, want %s", showplan.Shape(*declared), showplan.Shape(*sp))
	}
}

// TestWidenHidesExisting: (x) INCLUDE (y) widens nix_h_x (same effective
// keys on the heap); the measure after does not use nix_h_x. The COUNT(*)
// over a range of x needs only x: nix_h_x, narrower than the candidate,
// would serve it if it were still declared (with x = 42 the optimizer picks
// the candidate either way, measured: the test then proved nothing).
func TestWidenHidesExisting(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	q := f.addQuery(qSeek, false, []string{"godta_h"})
	qc := f.addQuery(qCount, false, []string{"godta_h"})
	sp, err := s.Compile(f.ctx, f.build(s, qc), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(showplan.Shape(*sp), "nix_h_x") {
		t.Fatalf("the COUNT(*) does not read nix_h_x before the widening: %s", showplan.Shape(*sp))
	}
	cands := candidate.ConfrontAll([]model.Candidate{f.cand("godta_h", []string{"x"}, []string{"y"}, q, qc)}, f.w)
	c := cands[0]
	if c.Decision != model.DecisionWiden || c.Existing != "nix_h_x" {
		t.Fatalf("decision %q of %q, want widen of nix_h_x", c.Decision, c.Existing)
	}
	res, _ := f.run(t, s, cands, Hooks{})
	for _, qq := range []model.Query{q, qc} {
		m := measureOf(t, res, c.ID, qq)
		t.Logf("%s: Before %g, After %g, Shape %s", qq.Text, m.Before, m.After, m.Shape)
		if m.Status != StatusMeasured {
			t.Fatalf("status %q (%s)", m.Status, m.Error)
		}
		if strings.Contains(m.Shape, "nix_h_x") {
			t.Errorf("%s: the measure after reads nix_h_x: %s", qq.Text, m.Shape)
		}
		if len(m.UsedHypothetical) == 0 {
			t.Errorf("%s: the measure after does not read the candidate: %s", qq.Text, m.Shape)
		}
	}
	f.noneLeft(t)
}

// TestIndexedViewDeclaredByType6: the reference of the GROUP BY served by
// godta_v equals SHOWPLAN_XML when the view is declared by type 6 and not
// without it, and a join served by the view has its reference right too,
// godta_t declared through ViewBase.
func TestIndexedViewDeclaredByType6(t *testing.T) {
	f := newFixture(t, "")
	if !f.inst.IsEnterpriseFeatures {
		t.Skipf("edition %q does not substitute indexed views by itself", f.inst.Edition)
	}
	s := f.session(t, time.Minute)
	qv := f.addQuery(qView, false, []string{"godta_t"})
	rf := f.reference(t, s, qv)
	t.Logf("view: SHOWPLAN_XML %g, AUTOPILOT %g, shape %s", rf.showplanCost, rf.before, rf.showplanShape)
	if !strings.Contains(rf.showplanShape, "godta_v_cx") {
		t.Fatalf("the plan does not use the view: %s", rf.showplanShape)
	}
	if rf.status != StatusMeasured {
		t.Errorf("status %q (%s), want measured", rf.status, rf.errText)
	}
	s.enterprise = false
	without, err := compileDeclared(f.ctx, s, rf.rp, rf.tables, nil, nil)
	s.enterprise = true
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("view without type 6: AUTOPILOT %g, shape %s", without.Cost, showplan.Shape(*without))
	if showplan.Shape(*without) == rf.showplanShape {
		t.Errorf("without type 6 the view is still substituted")
	}

	qj := f.addQuery(qViewJoin, false, []string{"godta_t", "godta_h"})
	rj := f.reference(t, s, qj)
	t.Logf("join: SHOWPLAN_XML %g, AUTOPILOT %g, shape %s", rj.showplanCost, rj.before, rj.showplanShape)
	if !strings.Contains(rj.showplanShape, "godta_v_cx") {
		t.Errorf("the join is not served by the view: %s", rj.showplanShape)
	}
	if !slices.Contains(rj.tables, f.table("godta_t")) {
		t.Errorf("godta_t not among the tables of the join: ViewBase not read")
	}
	if rj.status != StatusMeasured {
		t.Errorf("join: status %q (%s), want measured", rj.status, rj.errText)
	}
}

// spy observes the statements of a session.
type spy struct {
	inner   execer
	onQuery func(ctx context.Context, q string)
}

func (p *spy) Exec(ctx context.Context, s string) error { return p.inner.Exec(ctx, s) }
func (p *spy) Query(ctx context.Context, q string) ([][]any, error) {
	p.onQuery(ctx, q)
	return p.inner.Query(ctx, q)
}

func hypDeclared(s *Session) int {
	n := 0
	for _, d := range s.declared {
		var db, obj, ix, pages int
		if _, err := fmt.Sscanf(d, "DBCC AUTOPILOT(0, %d, %d, %d, %d)", &db, &obj, &ix, &pages); err == nil && pages != 0 {
			n++
		}
	}
	return n
}

// TestCombinedSeesAllCandidates: two candidates on godta_h measured
// together; the combined measure of x = 42 reads a hypothetical index, and
// both indexes exist when it is compiled.
func TestCombinedSeesAllCandidates(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	q := f.addQuery(qSeek, false, []string{"godta_h"})
	c1 := f.cand("godta_h", []string{"x"}, []string{"y"}, q)
	c2 := f.cand("godta_h", []string{"x"}, nil, q)

	var mu sync.Mutex
	var seen []int
	wrap := func(ex execer) execer {
		return &spy{inner: ex, onQuery: func(ctx context.Context, sql string) {
			if sql != "SELECT y FROM dbo.godta_h WHERE x = 42" || hypDeclared(s) < 2 {
				return
			}
			var n int
			if err := f.server.QueryRowContext(ctx, "SELECT COUNT(*) FROM ["+f.name+"].sys.indexes WHERE is_hypothetical = 1").Scan(&n); err != nil {
				t.Error(err)
			}
			mu.Lock()
			seen = append(seen, n)
			mu.Unlock()
		}}
	}
	s.ex = wrap(s.ex)
	reopen := s.reopen
	s.reopen = func(ctx context.Context) (execer, error) {
		ex, err := reopen(ctx)
		if err != nil {
			return nil, err
		}
		return wrap(ex), nil
	}

	res, _ := f.run(t, s, []model.Candidate{c1, c2}, Hooks{})
	var combined []model.Measure
	for _, m := range res.Measures {
		if len(m.Combined) > 0 {
			combined = append(combined, m)
		}
	}
	if len(combined) != 1 {
		t.Fatalf("%d combined measures, want 1: %+v", len(combined), res.Measures)
	}
	m := combined[0]
	t.Logf("combined: Before %g, After %g, UsedHypothetical %v, indexes seen %v", m.Before, m.After, m.UsedHypothetical, seen)
	if m.Status != StatusMeasured || len(m.UsedHypothetical) == 0 {
		t.Errorf("combined status %q, UsedHypothetical %v, want measured and not empty", m.Status, m.UsedHypothetical)
	}
	want := []string{c1.ID, c2.ID}
	slices.Sort(want)
	got := slices.Clone(m.Combined)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Combined %v, want %v", m.Combined, want)
	}
	if len(seen) == 0 {
		t.Fatal("no compilation with both candidates declared")
	}
	for _, n := range seen {
		if n != 2 {
			t.Errorf("%d hypothetical indexes when the combined measure compiled, want 2", n)
		}
	}
	f.noneLeft(t)
}

// TestAutoCreatedStatsListed: on the fresh database, the evaluation creates
// a _WA_Sys_ statistics on godta_t, and Run lists it.
func TestAutoCreatedStatsListed(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	q := f.addQuery(qNoStats, false, []string{"godta_t"})
	c := f.cand("godta_t", []string{"a", "b"}, []string{"c"}, q)
	res, _ := f.run(t, s, []model.Candidate{c}, Hooks{})
	t.Logf("AutoCreatedStats %v", res.AutoCreatedStats)
	if !slices.ContainsFunc(res.AutoCreatedStats, func(n string) bool { return strings.HasPrefix(n, "[dbo].[godta_t].[_WA_Sys_") }) {
		t.Errorf("AutoCreatedStats %v, want a _WA_Sys_ statistics of godta_t", res.AutoCreatedStats)
	}
	f.noneLeft(t)
}

// opStats sums the leaf counters of every index of godta_t.
func (f *fixture) opStats(t *testing.T) [3]int64 {
	t.Helper()
	var c [3]int64
	if err := f.server.QueryRowContext(f.ctx, fmt.Sprintf(`SELECT ISNULL(SUM(leaf_insert_count), 0), ISNULL(SUM(leaf_update_count), 0), ISNULL(SUM(leaf_delete_count), 0)
FROM sys.dm_db_index_operational_stats(%d, OBJECT_ID(N'[%s].dbo.godta_t'), NULL, NULL)`, f.w.Database.ID, f.name)).Scan(&c[0], &c[1], &c[2]); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) checksum(t *testing.T) [2]int64 {
	t.Helper()
	var c [2]int64
	if err := f.db.QueryRowContext(f.ctx, "SELECT COUNT_BIG(*), ISNULL(CAST(CHECKSUM_AGG(CHECKSUM(*)) AS bigint), 0) FROM dbo.godta_t").Scan(&c[0], &c[1]); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestWritesAreOnlyCompiled: under AUTOPILOT, the four write forms are
// compiled, not run: the leaf counters and the data do not move. A real
// UPDATE, rolled back, moves the counters first, or the detector proves
// nothing.
func TestWritesAreOnlyCompiled(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	before := f.opStats(t)
	if _, err := f.db.ExecContext(f.ctx, "BEGIN TRAN; UPDATE dbo.godta_t SET c = c + 'x' WHERE a = 500; ROLLBACK;"); err != nil {
		t.Fatal(err)
	}
	moved := f.opStats(t)
	if moved == before {
		t.Fatalf("a real UPDATE left the counters at %v: the detector does not work", moved)
	}
	data := f.checksum(t)
	for _, text := range []string{qUpdate, qInsert, qDelete, qMerge} {
		q := f.addQuery(text, true, []string{"godta_t"})
		rp := f.build(s, q)
		if rp.Kind == replay.KindUnreplayable {
			t.Fatalf("%q unreplayable: %s", text, rp.Reason)
		}
		st, err := compileDeclared(f.ctx, s, rp, []*model.Table{f.table("godta_t"), f.table("godta_v")}, nil, nil)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if len(st.Writes) == 0 {
			t.Errorf("%q: the plan has no write operator", text)
		}
	}
	if after := f.opStats(t); after != moved {
		t.Errorf("counters %v after the compilations, %v before", after, moved)
	}
	if after := f.checksum(t); after != data {
		t.Errorf("data %v after the compilations, %v before", after, data)
	}
}

// TestMaintenanceFromWritePlan: the wide UPDATE maintains (c) INCLUDE (a),
// not (b), which it does not touch; the narrow INSERT maintains (c) INCLUDE
// (a) inside its clustered operator, for less than the statement's cost.
func TestMaintenanceFromWritePlan(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	upd := f.addQuery(qUpdate, true, []string{"godta_t"})
	ins := f.addQuery(qInsert, true, []string{"godta_t"})
	cc := f.cand("godta_t", []string{"c"}, []string{"a"}, upd, ins)
	cb := f.cand("godta_t", []string{"b"}, nil, upd)
	res, _ := f.run(t, s, []model.Candidate{cc, cb}, Hooks{})

	mu := measureOf(t, res, cc.ID, upd)
	t.Logf("UPDATE with (c) INCLUDE (a): Before %g, After %g, Maintenance %g, Status %q", mu.Before, mu.After, mu.Maintenance, mu.Status)
	if mu.Status != StatusMeasured || !(mu.Maintenance > 0) {
		t.Errorf("UPDATE with (c) INCLUDE (a): status %q (%s), maintenance %g, want measured and > 0", mu.Status, mu.Error, mu.Maintenance)
	}
	mb := measureOf(t, res, cb.ID, upd)
	t.Logf("UPDATE with (b): Before %g, After %g, Maintenance %g, Status %q", mb.Before, mb.After, mb.Maintenance, mb.Status)
	if mb.Status != StatusMeasured || mb.Maintenance != 0 {
		t.Errorf("UPDATE with (b): status %q (%s), maintenance %g, want measured and 0", mb.Status, mb.Error, mb.Maintenance)
	}
	mi := measureOf(t, res, cc.ID, ins)
	t.Logf("INSERT with (c) INCLUDE (a): Before %g, After %g, Maintenance %g, Status %q", mi.Before, mi.After, mi.Maintenance, mi.Status)
	if mi.Status != StatusMeasured || !(mi.Maintenance > 0) || !(mi.Maintenance < mi.After) {
		t.Errorf("INSERT with (c) INCLUDE (a): status %q (%s), maintenance %g (statement %g), want measured, > 0 and below the statement",
			mi.Status, mi.Error, mi.Maintenance, mi.After)
	}
	f.noneLeft(t)
}

// TestDropRetriedThenJournaled: a second connection holds a shared table
// lock, incompatible with the Sch-M of the DROP, while Run drops: after three
// attempts the entry stays "created" and a warning says so. Released, the
// test drops the index itself.
func TestDropRetriedThenJournaled(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Minute)
	q := f.addQuery(qSeek, false, []string{"godta_h"})
	c := f.cand("godta_h", []string{"x"}, []string{"y"}, q)

	blocker, err := f.db.Conn(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	locked := false
	release := func() {
		if locked {
			if _, err := blocker.ExecContext(f.ctx, "ROLLBACK"); err != nil {
				t.Error(err)
			}
			locked = false
		}
	}
	defer release()
	hook := Hooks{AfterCreateHypothetical: func(ctx context.Context, _ *Session, _ model.Candidate, _ string) ([]string, error) {
		_, err := blocker.ExecContext(ctx, "BEGIN TRAN; SELECT TOP (1) 1 FROM dbo.godta_h WITH (TABLOCK, HOLDLOCK);")
		locked = err == nil
		return nil, err
	}}
	start := time.Now()
	res, _ := f.run(t, s, []model.Candidate{c}, hook)
	t.Logf("Run took %s, warnings %q", time.Since(start), res.Warnings)
	if !locked {
		t.Fatal("the blocking transaction did not start")
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "not dropped after 3 attempts") }) {
		t.Errorf("warnings %q, want one for the index left behind", res.Warnings)
	}
	pending, err := f.ws.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].State != workspace.StateCreated {
		t.Fatalf("journal pending %+v, want one entry in state created", pending)
	}
	release()
	if n := f.hypotheticals(t); n != 1 {
		t.Errorf("%d hypothetical indexes before the manual drop, want 1", n)
	}
	h := f.table("godta_h")
	if _, err := f.db.ExecContext(f.ctx, tsql.DropIndex(tsql.Obj{Schema: h.Schema, Name: h.Name}, pending[0].Name)); err != nil {
		t.Fatal(err)
	}
	if n := f.hypotheticals(t); n != 0 {
		t.Errorf("%d hypothetical indexes after the manual drop", n)
	}
}

// TestWeirdNames: a table [Ord]]er s] with a column [x y]: the candidate is
// created, measured and dropped without a syntax error.
func TestWeirdNames(t *testing.T) {
	f := newFixture(t, `CREATE TABLE dbo.[Ord]]er s] ([x y] int NOT NULL, z varchar(40) NOT NULL);
INSERT dbo.[Ord]]er s] SELECT TOP (50000) ABS(CHECKSUM(NEWID())) % 5000, CONVERT(varchar(36), NEWID()) FROM sys.all_columns a CROSS JOIN sys.all_columns b;`, "Ord]er s")
	s := f.session(t, time.Minute)
	q := f.addQuery("SELECT z FROM dbo.[Ord]]er s] WHERE [x y] = 42", false, []string{"Ord]er s"})
	c := f.cand("Ord]er s", []string{"x y"}, []string{"z"}, q)
	res, _ := f.run(t, s, []model.Candidate{c}, Hooks{})
	m := measureOf(t, res, c.ID, q)
	t.Logf("Before %g, After %g, Status %q", m.Before, m.After, m.Status)
	if m.Status != StatusMeasured || len(m.UsedHypothetical) == 0 {
		t.Errorf("status %q (%s), UsedHypothetical %v, want measured on the candidate", m.Status, m.Error, m.UsedHypothetical)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings %q", res.Warnings)
	}
	f.noneLeft(t)
}

// TestCompileTimeout: a 1 ms compile timeout makes the join "not evaluated:
// timeout", and the next compilation of the same session works on a new
// connection of the pool.
func TestCompileTimeout(t *testing.T) {
	f := newFixture(t, "")
	s := f.session(t, time.Millisecond)
	qj := f.addQuery(qJoin, false, []string{"godta_t", "godta_h"})
	rj := f.reference(t, s, qj)
	t.Logf("join: status %q (%s)", rj.status, rj.errText)
	if rj.status != StatusTimeout {
		t.Errorf("status %q (%s), want %q", rj.status, rj.errText, StatusTimeout)
	}
	s.compileTimeout = time.Minute
	rs := f.reference(t, s, f.addQuery(qSeek, false, []string{"godta_h"}))
	if rs.status != StatusMeasured {
		t.Errorf("the next compilation: status %q (%s), want measured", rs.status, rs.errText)
	}
}

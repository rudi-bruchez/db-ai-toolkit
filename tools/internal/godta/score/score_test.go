package score

import (
	"math"
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// workload: table 1, read 1 (100 executions), write 2 (10 executions,
// 5 rows each), Query Store.
func workload() *model.Workload {
	return &model.Workload{
		Source: model.SourceQueryStore,
		Queries: []model.Query{
			{QueryID: 1, Executions: 100},
		},
		Writes: []model.Query{
			{QueryID: 2, Executions: 10, AvgRowcount: 5, IsWrite: true},
		},
		Tables: map[int]*model.Table{
			1: {ObjectID: 1, Rows: 1000},
		},
	}
}

func cand(id string, queries ...int64) model.Candidate {
	return model.Candidate{ID: id, Table: 1, Keys: []model.KeyCol{{ColumnID: 2}}, Queries: queries}
}

func measured(q int64, c string, before, after float64) model.Measure {
	return model.Measure{QueryID: q, Candidate: c, Before: before, After: after, Status: StatusMeasured}
}

func one(t *testing.T, rs []model.Recommendation) model.Recommendation {
	t.Helper()
	if len(rs) != 1 {
		t.Fatalf("got %d recommendations, want 1", len(rs))
	}
	return rs[0]
}

func TestGainTimesExecutions(t *testing.T) {
	r := one(t, Score(workload(), []model.Candidate{cand("A", 1)},
		[]model.Measure{measured(1, "A", 10, 1)}, DefaultThresholds()))
	if r.Gain != 900 {
		t.Fatalf("gain = %v, want 900", r.Gain)
	}
	if !slices.Contains(r.Notes, NoteSampled) {
		t.Errorf("notes %q lack %q", r.Notes, NoteSampled)
	}
}

func TestMaintenanceRejects(t *testing.T) {
	// Read: (2 - 1) x 100 = gain 100. Write: no read-side saving, its cost
	// after is the cost before plus the maintenance, 8 x 10 = 80, above
	// half the gain.
	ms := func(maint float64) []model.Measure {
		w := measured(2, "A", 5, 5+maint)
		w.Maintenance = maint
		return []model.Measure{measured(1, "A", 2, 1), w}
	}
	c := []model.Candidate{cand("A", 1, 2)}

	ok := one(t, Score(workload(), c, ms(0), DefaultThresholds()))
	if ok.Decision != Recommended {
		t.Fatalf("control without maintenance: decision %q, want recommended (notes %q)", ok.Decision, ok.Notes)
	}
	r := one(t, Score(workload(), c, ms(8), DefaultThresholds()))
	if r.Gain != 100 || r.Maintenance != 80 {
		t.Fatalf("gain %v, maintenance %v, want 100 and 80", r.Gain, r.Maintenance)
	}
	if r.Decision != Rejected {
		t.Fatalf("decision %q, want rejected", r.Decision)
	}
}

func TestPlanCacheNeverRecommended(t *testing.T) {
	c := []model.Candidate{cand("A", 1)}
	ms := []model.Measure{measured(1, "A", 10, 1)}

	ok := one(t, Score(workload(), c, ms, DefaultThresholds()))
	if ok.Decision != Recommended {
		t.Fatalf("control on the Query Store: decision %q, want recommended", ok.Decision)
	}
	w := workload()
	w.Source = model.SourcePlanCache
	r := one(t, Score(w, c, ms, DefaultThresholds()))
	if r.Decision != Discuss {
		t.Fatalf("decision %q, want discuss", r.Decision)
	}
	if !slices.Contains(r.Notes, NotePlanCache) {
		t.Errorf("notes %q lack %q", r.Notes, NotePlanCache)
	}
}

func TestUnmeasuredHasNoGain(t *testing.T) {
	m := measured(1, "A", 10, 1)
	m.Status = "not evaluated: timeout"
	r := one(t, Score(workload(), []model.Candidate{cand("A", 1)}, []model.Measure{m}, DefaultThresholds()))
	if r.Gain != 0 {
		t.Errorf("gain = %v, want 0", r.Gain)
	}
	if r.Decision != Rejected {
		t.Errorf("decision %q, want rejected", r.Decision)
	}
	if len(r.Queries) != 1 || r.Queries[0].Status != "not evaluated: timeout" {
		t.Errorf("queries %+v, want the measure with its status kept", r.Queries)
	}
}

// TestConfrontDiscussNeverRecommended: a candidate the confrontation left
// to discuss (an existing index it would widen supports a constraint) is at
// best discuss, whatever its gain.
func TestConfrontDiscussNeverRecommended(t *testing.T) {
	ms := []model.Measure{measured(1, "A", 10, 1)}
	ok := one(t, Score(workload(), []model.Candidate{cand("A", 1)}, ms, DefaultThresholds()))
	if ok.Decision != Recommended {
		t.Fatalf("control, a new candidate: decision %q, want recommended", ok.Decision)
	}
	c := cand("A", 1)
	c.Decision, c.Existing = model.DecisionDiscuss, "pk_Activite"
	r := one(t, Score(workload(), []model.Candidate{c}, ms, DefaultThresholds()))
	if r.Decision != Discuss {
		t.Fatalf("decision %q, want discuss (notes %q)", r.Decision, r.Notes)
	}
	if !slices.Contains(r.Notes, NoteConfrontDiscuss+" with pk_Activite") {
		t.Errorf("notes %q do not say why", r.Notes)
	}
}

func TestFormulaStable(t *testing.T) {
	run := func() string {
		return one(t, Score(workload(), []model.Candidate{cand("A", 1)},
			[]model.Measure{measured(1, "A", 10, 1)}, DefaultThresholds())).Formula
	}
	a, b := run(), run()
	if a == "" || a != b {
		t.Fatalf("formula not stable: %q then %q", a, b)
	}
}

func TestCombinedRedundant(t *testing.T) {
	ms := []model.Measure{
		measured(1, "A", 10, 2),
		measured(1, "B", 10, 1),
		{QueryID: 1, Combined: []string{"A", "B"}, Before: 10, After: 1,
			UsedHypothetical: []string{"B"}, Status: StatusMeasured},
	}
	rs := Score(workload(), []model.Candidate{cand("A", 1), cand("B", 1)}, ms, DefaultThresholds())
	if len(rs) != 2 {
		t.Fatalf("got %d recommendations, want 2", len(rs))
	}
	a, b := rs[0], rs[1]
	if a.Decision != Discuss {
		t.Errorf("A: decision %q, want discuss", a.Decision)
	}
	if !slices.Contains(a.Notes, "made redundant by B") {
		t.Errorf("A: notes %q lack %q", a.Notes, "made redundant by B")
	}
	if b.Decision != Recommended {
		t.Errorf("B: decision %q, want recommended (notes %q)", b.Decision, b.Notes)
	}
}

// TestCombinedCostsNotScored (review finding 14): a combined measure records
// no maintenance, so its costs are not in the terms of the measures of one
// candidate, whose write gain is Before - (After - Maintenance). The score
// must read only which hypothetical indexes the combined plan uses: adding
// combined measures with other costs and no maintenance changes neither the
// gain, nor the maintenance, nor the decision of any candidate.
func TestCombinedCostsNotScored(t *testing.T) {
	write := func(c string) model.Measure {
		m := measured(2, c, 5, 5.5)
		m.Maintenance = 0.5
		return m
	}
	alone := []model.Measure{measured(1, "A", 10, 1), write("A"), measured(1, "B", 10, 2), write("B")}
	combined := append(slices.Clone(alone),
		model.Measure{QueryID: 1, Combined: []string{"A", "B"}, Before: 10, After: 9.9,
			UsedHypothetical: []string{"A", "B"}, Status: StatusMeasured},
		model.Measure{QueryID: 2, Combined: []string{"A", "B"}, Before: 5, After: 50,
			UsedHypothetical: []string{"A", "B"}, Status: StatusMeasured})
	cands := []model.Candidate{cand("A", 1, 2), cand("B", 1, 2)}
	want := Score(workload(), cands, alone, DefaultThresholds())
	got := Score(workload(), cands, combined, DefaultThresholds())
	for i := range want {
		w, g := want[i], got[i]
		if g.Gain != w.Gain || g.Maintenance != w.Maintenance || g.Decision != w.Decision || !slices.Equal(g.Notes, w.Notes) {
			t.Errorf("%s: gain %v maintenance %v decision %q notes %q, want %v %v %q %q without the combined measures",
				w.Candidate.ID, g.Gain, g.Maintenance, g.Decision, g.Notes, w.Gain, w.Maintenance, w.Decision, w.Notes)
		}
	}
	if want[0].Maintenance != 5 || want[0].Gain != 900 {
		t.Errorf("A alone: gain %v maintenance %v, want 900 and 5", want[0].Gain, want[0].Maintenance)
	}
}

func TestUnreliableNeverRecommended(t *testing.T) {
	c := []model.Candidate{cand("A", 1)}
	m := measured(1, "A", 10, 1)
	ok := one(t, Score(workload(), c, []model.Measure{m}, DefaultThresholds()))
	if ok.Decision != Recommended {
		t.Fatalf("control without the note: decision %q, want recommended", ok.Decision)
	}
	m.Notes = []string{"unreliable estimate"}
	r := one(t, Score(workload(), c, []model.Measure{m}, DefaultThresholds()))
	if r.Decision == Recommended {
		t.Fatalf("decision %q with an unreliable estimate", r.Decision)
	}
}

func TestEstimatedPages(t *testing.T) {
	if got := EstimatedPages(1_000_000, 12.78, 4); got != 3308 {
		t.Errorf("EstimatedPages(1e6, 12.78, 4) = %d, want 3308", got)
	}
	if got := EstimatedPages(0, 12.78, 4); got != 1 {
		t.Errorf("EstimatedPages(0, …) = %d, want at least 1", got)
	}
}

func TestWriteMaintenanceNotDoubleCounted(t *testing.T) {
	w := workload()
	w.Queries[0].Executions = 10
	w.Writes[0].Executions = 1
	wm := measured(2, "A", 10, 12)
	wm.Maintenance = 3
	r := one(t, Score(w, []model.Candidate{cand("A", 1, 2)},
		[]model.Measure{measured(1, "A", 10, 1), wm}, DefaultThresholds()))
	if r.Gain != 91 || r.Maintenance != 3 {
		t.Fatalf("gain %v, maintenance %v, want 91 (90 + 1) and 3", r.Gain, r.Maintenance)
	}
}

// Beyond the gate of the brief: the rows-written fallback and the size.

func TestRowsWrittenFallback(t *testing.T) {
	w := measured(2, "A", 5, 5)
	w.Status = "not evaluated: unreplayable (temporary table)"
	w.Maintenance = 99 // must not count: not measured
	r := one(t, Score(workload(), []model.Candidate{cand("A", 1, 2)},
		[]model.Measure{measured(1, "A", 10, 1), w}, DefaultThresholds()))
	if r.Maintenance != 0 || r.RowsWritten != 50 { // 10 executions x 5 rows
		t.Errorf("maintenance = %v, rows written = %v, want 0 and 50", r.Maintenance, r.RowsWritten)
	}
	if !slices.Contains(r.Notes, NoteRowsWritten) {
		t.Errorf("notes %q lack %q", r.Notes, NoteRowsWritten)
	}
}

// A bulk load of 200000 rows that could not be measured must not be
// compared, in rows, with a gain in cost units: it used to reject a candidate
// whose real gain was 95 % of the query cost.
func TestUnmeasuredWriteDowngradesToDiscuss(t *testing.T) {
	w := workload()
	w.Writes[0].Executions = 3
	w.Writes[0].AvgRowcount = 200000
	u := measured(2, "A", 5, 5)
	u.Status = "not evaluated: unreplayable (temporary table)"
	r := one(t, Score(w, []model.Candidate{cand("A", 1, 2)},
		[]model.Measure{measured(1, "A", 100, 5), u}, DefaultThresholds()))
	if r.Decision != Discuss {
		t.Fatalf("decision %q, want discuss (notes %q)", r.Decision, r.Notes)
	}
	if r.Maintenance != 0 || r.RowsWritten != 600000 {
		t.Errorf("maintenance %v, rows written %v, want 0 and 600000", r.Maintenance, r.RowsWritten)
	}
	for _, n := range []string{NoteRowsWritten, noteMaintenanceUnknown} {
		if !slices.Contains(r.Notes, n) {
			t.Errorf("notes %q lack %q", r.Notes, n)
		}
	}
}

// Other rejections stay rejections, even with an unmeasured write.
func TestUnmeasuredWriteKeepsOtherRejections(t *testing.T) {
	u := measured(2, "A", 5, 5)
	u.Status = "not evaluated: unreplayable (temporary table)"
	r := one(t, Score(workload(), []model.Candidate{cand("A", 1, 2)},
		[]model.Measure{measured(1, "A", 1.05, 1), u}, DefaultThresholds()))
	if r.Decision != Rejected {
		t.Fatalf("decision %q, want rejected (notes %q)", r.Decision, r.Notes)
	}
}

// writePlan is the plan of a write that reads the table from and writes the
// table to (INSERT INTO to SELECT ... FROM from).
func writePlan(from, to string) *showplan.Plan {
	return &showplan.Plan{Statements: []showplan.Statement{{
		Accesses: []showplan.Access{{Obj: showplan.ObjRef{Schema: "dbo", Table: from}, LookupOf: -1}},
		Writes:   []showplan.Write{{Op: "Clustered Index Insert", Objs: []showplan.ObjRef{{Schema: "dbo", Table: to}}}},
	}}}
}

// TestReadOnlyWriteNotAWrite: INSERT INTO Customers SELECT ... FROM Orders is
// bound to a candidate on Orders, which it reads; unmeasured, it must not add
// its rows to the rows written of that candidate, which it never maintains.
// The control, the same write into Orders, does.
func TestReadOnlyWriteNotAWrite(t *testing.T) {
	score := func(to string) model.Recommendation {
		w := workload()
		w.Tables[1].Schema, w.Tables[1].Name = "dbo", "Orders"
		w.Tables[2] = &model.Table{ObjectID: 2, Schema: "dbo", Name: "Customers"}
		w.Writes[0].AvgRowcount = 500
		w.Writes[0].Plan = writePlan("Orders", to)
		u := measured(2, "A", 5, 5)
		u.Status = "not evaluated: unreplayable (temporary table)"
		return one(t, Score(w, []model.Candidate{cand("A", 1, 2)},
			[]model.Measure{measured(1, "A", 10, 1), u}, DefaultThresholds()))
	}
	r := score("Customers")
	if r.RowsWritten != 0 || slices.Contains(r.Notes, NoteRowsWritten) {
		t.Errorf("rows written %v, notes %q: a write into Customers counted for an index on Orders", r.RowsWritten, r.Notes)
	}
	if r.Decision != Recommended {
		t.Errorf("decision %q, want recommended (notes %q)", r.Decision, r.Notes)
	}
	ctl := score("Orders")
	if ctl.RowsWritten != 5000 || ctl.Decision != Discuss { // 10 executions x 500 rows
		t.Errorf("control: rows written %v, decision %q, want 5000 and discuss", ctl.RowsWritten, ctl.Decision)
	}
}

func TestSizeMB(t *testing.T) {
	w := workload()
	w.Tables[1] = &model.Table{
		ObjectID: 1, Rows: 1_000_000,
		Columns: []model.Column{{ID: 1, AvgWidth: 4}, {ID: 2, AvgWidth: 8.78}, {ID: 3, AvgWidth: 4}},
		Indexes: []model.Index{{Kind: model.KindClustered, Keys: []model.KeyCol{{ColumnID: 1}}}},
	}
	c := cand("A", 1)
	c.Include = []int{3}
	r := one(t, Score(w, []model.Candidate{c}, nil, DefaultThresholds()))
	want := 3308.0 * 8 / 1024
	if math.Abs(r.SizeMB-want) > 1e-9 {
		t.Errorf("SizeMB = %v, want %v", r.SizeMB, want)
	}
}

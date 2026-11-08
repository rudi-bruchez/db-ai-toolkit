package score

import (
	"math"
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
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
	// Read: (2 - 1) x 100 = gain 100. Write: same cost before and after,
	// maintenance 8 x 10 = 80, above half the gain.
	ms := func(maint float64) []model.Measure {
		w := measured(2, "A", 5, 5)
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

// Beyond the gate of the brief: the rows-written fallback and the size.

func TestRowsWrittenFallback(t *testing.T) {
	w := measured(2, "A", 5, 5)
	w.Status = "not evaluated: unreplayable (temporary table)"
	w.Maintenance = 99 // must not count: not measured
	r := one(t, Score(workload(), []model.Candidate{cand("A", 1, 2)},
		[]model.Measure{measured(1, "A", 10, 1), w}, DefaultThresholds()))
	if r.Maintenance != 50 { // 10 executions x 5 rows
		t.Errorf("maintenance = %v, want 50", r.Maintenance)
	}
	if !slices.Contains(r.Notes, NoteRowsWritten) {
		t.Errorf("notes %q lack %q", r.Notes, NoteRowsWritten)
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

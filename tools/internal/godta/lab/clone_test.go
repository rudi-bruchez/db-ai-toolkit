package lab_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// statsWorkload has table t (object_id 100) with columns a, b, c (1, 2, 3)
// and no statistics.
func statsWorkload() *model.Workload {
	return &model.Workload{
		Database: model.DBInfo{Name: "Sales"},
		Tables: map[int]*model.Table{
			100: {ObjectID: 100, Schema: "dbo", Name: "t",
				Columns: []model.Column{{ID: 1, Name: "a", Type: "int"}, {ID: 2, Name: "b", Type: "int"}, {ID: 3, Name: "c", Type: "varchar"}}},
		},
	}
}

func candAB() []model.Candidate {
	return []model.Candidate{{ID: "c1", Table: 100, Keys: []model.KeyCol{{ColumnID: 1}, {ColumnID: 2}}, Decision: model.DecisionNew}}
}

type spec struct {
	cols  []int
	multi bool
}

func specs(got []lab.StatSpec) []spec {
	out := make([]spec, len(got))
	for i, s := range got {
		out[i] = spec{s.Columns, s.Multi}
	}
	return out
}

func equalSpecs(a, b []spec) bool {
	return slices.EqualFunc(a, b, func(x, y spec) bool { return x.multi == y.multi && slices.Equal(x.cols, y.cols) })
}

// TestStatsToCreate: a candidate (a, b) on columns without statistics gives
// three statistics, (a, b), (a), (b), named s1 to s3.
func TestStatsToCreate(t *testing.T) {
	got := lab.StatsToCreate(statsWorkload(), candAB())
	want := []spec{{[]int{1, 2}, true}, {[]int{1}, false}, {[]int{2}, false}}
	if !equalSpecs(specs(got), want) {
		t.Fatalf("StatsToCreate = %+v, want %+v", got, want)
	}
	for i, s := range got {
		if s.Table != 100 || s.Name != "s"+string(rune('1'+i)) {
			t.Errorf("spec %d: table %d name %q", i, s.Table, s.Name)
		}
	}
}

// TestStatsToCreateSkipsColumnWithStats: a column led by an existing
// statistics object (a, of (a, c)) gets no single-column statistics; a column
// that is only second in one (b, of (c, b)) has no histogram and gets its
// own. The multicolumn one is still created.
func TestStatsToCreateSkipsColumnWithStats(t *testing.T) {
	w := statsWorkload()
	w.Tables[100].Stats = []model.Stat{{StatsID: 2, Name: "st_ac", Columns: []int{1, 3}}, {StatsID: 3, Name: "st_cb", Columns: []int{3, 2}}}
	got := lab.StatsToCreate(w, candAB())
	want := []spec{{[]int{1, 2}, true}, {[]int{2}, false}}
	if !equalSpecs(specs(got), want) {
		t.Fatalf("StatsToCreate = %+v, want %+v", got, want)
	}
}

// TestStatsToCreateQueryColumns: a column a query filters on, which no
// candidate has as key, gets its single-column statistics.
func TestStatsToCreateQueryColumns(t *testing.T) {
	w := statsWorkload()
	w.Queries = []model.Query{{QueryID: 1, Plan: &showplan.Plan{Statements: []showplan.Statement{{
		Accesses: []showplan.Access{{
			Obj:    showplan.ObjRef{Database: "Sales", Schema: "dbo", Table: "t"},
			PredEq: []showplan.ColRef{{Database: "Sales", Schema: "dbo", Table: "t", Column: "c"}},
		}},
	}}}}}
	got := lab.StatsToCreate(w, nil)
	want := []spec{{[]int{3}, false}}
	if !equalSpecs(specs(got), want) {
		t.Fatalf("StatsToCreate = %+v, want %+v", got, want)
	}
}

// TestStatsToCreateSkipsCovered: a covered candidate is not evaluated and
// gets no statistics.
func TestStatsToCreateSkipsCovered(t *testing.T) {
	c := candAB()
	c[0].Decision = model.DecisionCovered
	if got := lab.StatsToCreate(statsWorkload(), c); len(got) != 0 {
		t.Fatalf("StatsToCreate of a covered candidate = %+v, want none", got)
	}
}

// TestStateRoundTrip: SaveState then LoadState give back the state.
func TestStateRoundTrip(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.DBDir("SQL01", "Sales")
	s := lab.State{
		File:     lab.File{HostPath: "/x/godta_ab12cd34_c.bak", Bytes: 10, SHA256: "ff", Chunks: 2},
		Prepared: &lab.Prepared{Database: "godta_ab12cd34_c", Streams: map[string][]byte{"100:1,2": {1, 2}}},
	}
	if err := lab.SaveState(ws, dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := lab.LoadState(ws, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.File != s.File || got.Prepared == nil || got.Prepared.Database != s.Prepared.Database ||
		!slices.Equal(got.Prepared.Streams["100:1,2"], []byte{1, 2}) {
		t.Fatalf("LoadState = %+v, want %+v", got, s)
	}
}

// TestCloneRefusesOtherPhase: a plan of another phase is refused before the
// connection is used.
func TestCloneRefusesOtherPhase(t *testing.T) {
	_, err := lab.Clone(context.Background(), nil, workspace.WS{Root: t.TempDir()}, runplan.Plan{Phase: runplan.PhaseEvaluate}, t.TempDir())
	if !errors.Is(err, lab.ErrNotClonePlan) {
		t.Fatalf("Clone of an evaluate plan: %v, want ErrNotClonePlan", err)
	}
}

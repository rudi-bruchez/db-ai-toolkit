package evaluate

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// The measured case of spec section 8.5: 3 308 pages estimated, 3 314 real.
func TestPagesFormula(t *testing.T) {
	if got := HypotheticalPages(1_000_000, 12.78, 4); got != 3308 {
		t.Errorf("HypotheticalPages = %d, want 3308", got)
	}
	if got := HypotheticalPages(0, 4, 4); got != 1 {
		t.Errorf("HypotheticalPages of an empty table = %d, want at least 1", got)
	}
}

const (
	tblH  = 102
	tblCS = 104
)

// unitWorkload is godta_h (a heap with nix_h_x) and godta_cs (a table with a
// nonclustered columnstore index), written by hand.
func unitWorkload() *model.Workload {
	return &model.Workload{
		Server:   conn.Instance{ServerName: "srv", MachineName: "m"},
		Database: model.DBInfo{ID: 5, Name: "db", Collation: "SQL_Latin1_General_CP1_CI_AS"},
		Tables: map[int]*model.Table{
			tblH: {ObjectID: tblH, Schema: "dbo", Name: "godta_h", Rows: 200000, IsHeap: true,
				Columns: []model.Column{{ID: 1, Name: "x", AvgWidth: 4}, {ID: 2, Name: "y", AvgWidth: 36}},
				Indexes: []model.Index{
					{ObjectID: tblH, IndexID: 0, Kind: model.KindHeap},
					{ObjectID: tblH, IndexID: 2, Name: "nix_h_x", Kind: model.KindNonclustered, Keys: []model.KeyCol{{ColumnID: 1}}},
				}},
			tblCS: {ObjectID: tblCS, Schema: "dbo", Name: "godta_cs", Rows: 10000, HasColumnstore: true,
				Columns: []model.Column{{ID: 1, Name: "k", AvgWidth: 4}, {ID: 2, Name: "v", AvgWidth: 4}},
				Indexes: []model.Index{
					{ObjectID: tblCS, IndexID: 1, Name: "pk_cs", Kind: model.KindClustered, Keys: []model.KeyCol{{ColumnID: 1}}},
					{ObjectID: tblCS, IndexID: 2, Name: "ncci_cs", Kind: model.KindNonclusteredColumnstore},
				}},
		},
	}
}

func readOf(tables ...string) *showplan.Plan {
	st := showplan.Statement{Type: "SELECT", Root: &showplan.RelOp{PhysicalOp: "Table Scan"}}
	for _, t := range tables {
		st.Accesses = append(st.Accesses, showplan.Access{Obj: showplan.ObjRef{Schema: "dbo", Table: t}, LookupOf: -1})
	}
	return &showplan.Plan{Statements: []showplan.Statement{st}}
}

func withID(c model.Candidate) model.Candidate {
	c.ID = candidate.ID(c)
	return c
}

// TestRunRefusesUnplannedWrite: a candidate the plan was not made from is
// refused before anything is sent, and the session refuses any statement
// that is not a step of the plan.
func TestRunRefusesUnplannedWrite(t *testing.T) {
	w := unitWorkload()
	w.Queries = []model.Query{{QueryID: 1, Text: "SELECT y FROM dbo.godta_h WHERE x = 42", SetOptions: 4347,
		DateFirst: 7, DateFormat: 1, Plan: readOf("godta_h")}}
	planned := withID(model.Candidate{Table: tblH, Keys: []model.KeyCol{{ColumnID: 1}}, Include: []int{2},
		Queries: []int64{1}, Decision: model.DecisionNew})
	extra := withID(model.Candidate{Table: tblH, Keys: []model.KeyCol{{ColumnID: 2}},
		Queries: []int64{1}, Decision: model.DecisionNew})
	plan := runplan.ForEvaluate(w, []model.Candidate{planned}, "abcd1234", nil, 4, 2000).Seal()

	cases := []struct {
		name  string
		plan  runplan.Plan
		cands []model.Candidate
	}{
		{"candidate not in the plan", plan, []model.Candidate{planned, extra}},
		{"step that is not the candidate's definition", func() runplan.Plan {
			p := plan
			p.Steps = append([]runplan.Step(nil), plan.Steps...)
			for i, st := range p.Steps {
				if st.Candidate != "" && st.Resolve != "" {
					p.Steps[i].SQL = strings.Replace(st.SQL, "INCLUDE ([y])", "", 1)
				}
			}
			return p
		}(), []model.Candidate{planned}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := NewRecordingSession(w.Server)
			ws, err := workspace.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			_, err = Run(context.Background(), s, ws, &tc.plan, w, tc.cands, Hooks{})
			if !errors.Is(err, ErrNotInPlan) {
				t.Fatalf("Run error %v, want ErrNotInPlan", err)
			}
			if len(rec.Statements) != 0 {
				t.Errorf("statements sent: %q, want none", rec.Statements)
			}
			if pending, _ := ws.Pending(); len(pending) != 0 {
				t.Errorf("journal entries %+v, want none", pending)
			}
		})
	}

	t.Run("session refuses a statement outside the plan", func(t *testing.T) {
		s, rec := NewRecordingSession(w.Server)
		s.usePlan(&plan)
		err := s.Exec(context.Background(), "DROP INDEX [nix_h_x] ON [dbo].[godta_h]")
		if !errors.Is(err, ErrNotInPlan) {
			t.Errorf("Exec error %v, want ErrNotInPlan", err)
		}
		if err := s.Exec(context.Background(), plan.Steps[1].SQL); err != nil {
			t.Errorf("Exec of a plan step: %v", err)
		}
		if len(rec.Statements) != 1 || rec.Statements[0] != plan.Steps[1].SQL {
			t.Errorf("statements sent %q, want only the plan step", rec.Statements)
		}
	})
}

// TestColumnstoreNotSent: a query that touches a table with a columnstore
// index is "not evaluated: columnstore" without any compilation sent, both
// for a candidate on that table (which the plan skips) and for a candidate
// on another table whose query also reads it (error 2510 under AUTOPILOT,
// measured on 2016 to 2025).
func TestColumnstoreNotSent(t *testing.T) {
	w := unitWorkload()
	w.Queries = []model.Query{
		{QueryID: 1, Text: "SELECT COUNT(*) FROM dbo.godta_cs WHERE v = 1", SetOptions: 4347, DateFirst: 7, DateFormat: 1,
			Plan: readOf("godta_cs")},
		{QueryID: 2, Text: "SELECT COUNT(*) FROM dbo.godta_h h JOIN dbo.godta_cs c ON c.k = h.x", SetOptions: 4347,
			DateFirst: 7, DateFormat: 1, Plan: readOf("godta_h", "godta_cs")},
	}
	onCS := withID(model.Candidate{Table: tblCS, Keys: []model.KeyCol{{ColumnID: 2}}, Queries: []int64{1}, Decision: model.DecisionNew})
	onH := withID(model.Candidate{Table: tblH, Keys: []model.KeyCol{{ColumnID: 1}}, Include: []int{2}, Queries: []int64{2}, Decision: model.DecisionNew})
	cands := []model.Candidate{onCS, onH}
	plan := runplan.ForEvaluate(w, cands, "abcd1234", nil, 4, 2000).Seal()

	s, rec := NewRecordingSession(w.Server)
	for _, st := range plan.Steps {
		if st.Resolve != "" {
			rec.Results[st.Resolve] = [][]any{{int64(3)}}
		}
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), s, ws, &plan, w, cands, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range res.Measures {
		got[m.Candidate] = m.Status
		if m.Status != StatusColumnstore {
			t.Errorf("measure of query %d, candidate %s: status %q, want %q", m.QueryID, m.Candidate, m.Status, StatusColumnstore)
		}
	}
	if len(got) != 2 {
		t.Errorf("measures %+v, want one per candidate", res.Measures)
	}
	for _, sub := range []string{"AUTOPILOT", "SHOWPLAN", "godta_cs"} {
		if n := rec.Count(sub); n != 0 {
			t.Errorf("%d statements contain %q, want none: %q", n, sub, rec.Statements)
		}
	}
}

// TestMaintenanceNarrowAndWide: a wide plan maintains the hypothetical index
// in an Index Update of its own, which counts for its own cost; a narrow plan
// lists it among the objects of one Clustered Index Update, which counts for
// the difference of its own cost with the plan without the candidate.
func TestMaintenanceNarrowAndWide(t *testing.T) {
	const hyp = "godta_abcd1234_1"
	pk := showplan.ObjRef{Schema: "dbo", Table: "godta_t", Index: "pk_godta_t"}
	ix := showplan.ObjRef{Schema: "dbo", Table: "godta_t", Index: hyp}
	view := showplan.ObjRef{Schema: "dbo", Table: "godta_v", Index: "godta_v_cx"}

	wide := &showplan.Statement{Cost: 0.5, Writes: []showplan.Write{
		{Op: "Clustered Index Update", Objs: []showplan.ObjRef{pk}, OwnCost: 0.2},
		{Op: "Index Update", Objs: []showplan.ObjRef{ix}, OwnCost: 0.03},
	}}
	wideWithout := &showplan.Statement{Cost: 0.47, Writes: []showplan.Write{
		{Op: "Clustered Index Update", Objs: []showplan.ObjRef{pk}, OwnCost: 0.2},
	}}
	if got := maintenance(wideWithout, wide, hyp); math.Abs(got-0.03) > 1e-12 {
		t.Errorf("wide plan: maintenance %g, want 0.03", got)
	}

	narrow := &showplan.Statement{Cost: 0.04, Writes: []showplan.Write{
		{Op: "Clustered Index Update", Objs: []showplan.ObjRef{pk, ix}, OwnCost: 0.0233},
		{Op: "Clustered Index Update", Objs: []showplan.ObjRef{view}, OwnCost: 0.005},
	}}
	narrowWithout := &showplan.Statement{Cost: 0.03, Writes: []showplan.Write{
		{Op: "Clustered Index Update", Objs: []showplan.ObjRef{view}, OwnCost: 0.005},
		{Op: "Clustered Index Update", Objs: []showplan.ObjRef{pk}, OwnCost: 0.0133},
	}}
	if got := maintenance(narrowWithout, narrow, hyp); math.Abs(got-0.01) > 1e-12 {
		t.Errorf("narrow plan: maintenance %g, want 0.01", got)
	}
	if got := maintenance(narrowWithout, narrow, "godta_abcd1234_2"); got != 0 {
		t.Errorf("another index: maintenance %g, want 0", got)
	}
}

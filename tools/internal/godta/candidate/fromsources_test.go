package candidate

import (
	"os"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/sqlferret"
)

func TestFromSourcesOrigin(t *testing.T) {
	data, err := os.ReadFile("../showplan/testdata/seek_lookup.sqlplan")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := showplan.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.ReadFile("../showplan/testdata/sort.sqlplan")
	if err != nil {
		t.Fatal(err)
	}
	otherPlan, err := showplan.Parse(other)
	if err != nil {
		t.Fatal(err)
	}
	w := workload()
	// Query 9 reads godta_h in the workload; the sqlferret candidates have
	// no query of their own and get it from BindQueries.
	w.Queries = []model.Query{{QueryID: 9, Plan: plan}}
	srcs := []sqlferret.Source{
		{File: "plans/profile/run_1/p_1.sqlplan", Database: dbName, Plan: plan},
		// A plan of godta_t attributed to another database: its candidate
		// would be a second one, on godta_t, were Database not obeyed.
		{File: "plans/profile/run_1/p_2.sqlplan", Database: "otherdb", Plan: otherPlan},
	}
	got := FromSources(srcs, w)
	if len(got) != 1 {
		t.Fatalf("candidates = %+v, want one", got)
	}
	c := got[0]
	if c.Origin != OriginSqlferret {
		t.Errorf("origin = %q, want %q", c.Origin, OriginSqlferret)
	}
	if c.Table != tblH || len(c.Keys) != 1 || c.Keys[0].ColumnID != 1 {
		t.Errorf("candidate = %+v, want godta_h keyed on x", c)
	}
	if len(c.Queries) != 1 || c.Queries[0] != 9 {
		t.Errorf("queries = %v, want [9]", c.Queries)
	}
	if c.Rules == nil || c.ID != ID(c) {
		t.Errorf("rules %v, id %q: not built by the rules of Generate", c.Rules, c.ID)
	}
}

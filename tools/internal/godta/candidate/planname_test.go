package candidate

import (
	"os"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/sqlferret"
)

// renamed is the test workload collected under another name than the one
// its plans were compiled under, as collect records it.
func renamed() *model.Workload {
	w := workload()
	w.Database.Name = "godta_copy"
	w.Database.PlanNames = []string{dbName}
	return w
}

// TestGenerateReadsPlanName: a plan that names the database by the name it
// was compiled under still gives its candidate.
func TestGenerateReadsPlanName(t *testing.T) {
	w := renamed()
	a := scan("godta_t")
	a.PredEq = cs("godta_t", "a")
	w.Queries = []model.Query{query(1, showplan.Statement{Accesses: []showplan.Access{a}})}
	if cands := Generate(w); len(cands) != 1 || cands[0].Table != tblT {
		t.Errorf("candidates %+v, want one on godta_t", cands)
	}
}

// TestFromSourcesReadsPlanName: a sqlferret source attributed to the name
// the plans were compiled under is this database's.
func TestFromSourcesReadsPlanName(t *testing.T) {
	data, err := os.ReadFile("../showplan/testdata/seek_lookup.sqlplan")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := showplan.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	w := renamed()
	srcs := []sqlferret.Source{{File: "p_1.sqlplan", Database: dbName, Plan: plan}}
	if got := FromSources(srcs, w); len(got) != 1 || got[0].Table != tblH {
		t.Errorf("candidates %+v, want one on godta_h", got)
	}
}

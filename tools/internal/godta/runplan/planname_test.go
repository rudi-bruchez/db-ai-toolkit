package runplan_test

import (
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// TestCloneStatsReadsPlanName: a column qualified by the name the plans were
// compiled under is a column of this database.
func TestCloneStatsReadsPlanName(t *testing.T) {
	w := workload()
	w.Database.Name = "SalesCopy"
	w.Database.PlanNames = []string{"Sales"}
	x := showplan.ColRef{Database: "sales", Schema: "dbo", Table: "godta_h", Column: "x"}
	w.Queries = []model.Query{{QueryID: 1, Plan: &showplan.Plan{Statements: []showplan.Statement{{JoinCols: []showplan.ColRef{x}}}}}}
	if got := runplan.CloneStats(w, nil); len(got) != 1 || got[0].Table != 100 {
		t.Errorf("CloneStats %+v, want one statistics on godta_h", got)
	}
}

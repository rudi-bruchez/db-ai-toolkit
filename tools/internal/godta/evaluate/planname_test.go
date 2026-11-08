package evaluate

import (
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// TestTablesOfReadsPlanName: an object qualified by the name the plans were
// compiled under is a table of the workload, the session's database being
// neither name.
func TestTablesOfReadsPlanName(t *testing.T) {
	w := unitWorkload()
	w.Database.Name = "db_copy"
	w.Database.PlanNames = []string{"db"}
	st := &showplan.Statement{Accesses: []showplan.Access{{Obj: showplan.ObjRef{Database: "DB", Schema: "dbo", Table: "godta_h"}, LookupOf: -1}}}
	if got := tablesOf(w, st, "godta_clone"); len(got) != 1 || got[0].ObjectID != tblH {
		t.Errorf("tablesOf = %v, want godta_h", got)
	}
}

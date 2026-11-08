package replay_test

import (
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/replay"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// TestBuildReadsPlanName: an object qualified by the name the plans were
// compiled under is not another database.
func TestBuildReadsPlanName(t *testing.T) {
	w := workload("SQL_Latin1_General_CP1_CI_AS")
	w.Database.Name = "godta_copy"
	w.Database.PlanNames = []string{"godta_db"}
	q := model.Query{Text: "SELECT 1 FROM dbo.godta_t", SetOptions: baseOpts, DateFirst: 7, DateFormat: 1}
	st := &showplan.Statement{Accesses: []showplan.Access{access("GODTA_DB", "dbo", "godta_t")}}
	if got := replay.Build(q, st, w, proc); got.Kind != replay.KindAdhoc {
		t.Errorf("Build: %+v, want an ad hoc replay", got)
	}
}

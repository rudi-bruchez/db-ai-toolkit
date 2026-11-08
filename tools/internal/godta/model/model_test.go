package model

import (
	"encoding/json"
	"testing"
)

func TestDBInfoMatches(t *testing.T) {
	d := DBInfo{Name: "copy", PlanNames: []string{"Prod"}}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"copy", true},
		{"COPY", true},
		{"Prod", true},
		{"prod", true},
		{"other", false},
		{"", false},
	} {
		if got := d.Matches(tc.name); got != tc.want {
			t.Errorf("Matches(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (DBInfo{Name: "copy"}).Matches("Prod") {
		t.Error("Matches(\"Prod\") without PlanNames = true, want false")
	}
}

// TestPlanNamesSurviveJSON: the commands that read collect.json back (and
// re-parse its plans) see the names the plans were compiled under.
func TestPlanNamesSurviveJSON(t *testing.T) {
	data, err := json.Marshal(Workload{Database: DBInfo{Name: "copy", PlanNames: []string{"Prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	var w Workload
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	if !w.Database.Matches("Prod") {
		t.Errorf("read back %+v from %s, want PlanNames [Prod]", w.Database, data)
	}
}

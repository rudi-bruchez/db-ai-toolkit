package collect

import (
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
)

func userDB(name string) dbRow {
	return dbRow{info: model.DBInfo{Name: name}, state: "ONLINE"}
}

func TestDatabaseFilter(t *testing.T) {
	rows := []dbRow{userDB("AU_Prod"), userDB("AU_Test1"), userDB("NZ_Prod"), userDB("au_ref")}
	keep, skipped, err := classify(rows, Options{Include: []string{"AU*"}, Exclude: []string{"AU_Test*"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, k := range keep {
		names = append(names, k.Name)
	}
	if want := []string{"AU_Prod", "au_ref"}; !slices.Equal(names, want) {
		t.Errorf("kept %v, want %v", names, want)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped %v, want AU_Test1 and NZ_Prod", skipped)
	}
}

func TestIndexNamesInText(t *testing.T) {
	got := indexNamesInText("SELECT c FROM dbo.t WITH (INDEX(ix_a), INDEX = [ix b]) WHERE a = 1")
	if want := []string{"ix_a", "ix b"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDedupTopN(t *testing.T) {
	qs := []model.Query{
		{QueryID: 1, Executions: 100, CPUms: 900, Reads: 900, Duration: 900},
		{QueryID: 2, Executions: 50, CPUms: 10, Reads: 10, Duration: 10},
		{QueryID: 3, Executions: 1, CPUms: 500, Reads: 1, Duration: 1},
		{QueryID: 4, Executions: 2, CPUms: 1, Reads: 1, Duration: 1},
	}
	got := dedupTopN(qs, 2)
	var ids []int64
	for _, q := range got {
		ids = append(ids, q.QueryID)
	}
	// Query 1 leads every ranking; 2 is second by executions, 3 by CPU;
	// by reads and duration the second place is 2 again.
	if want := []int64{1, 2, 3}; !slices.Equal(ids, want) {
		t.Errorf("got %v, want %v", ids, want)
	}
}

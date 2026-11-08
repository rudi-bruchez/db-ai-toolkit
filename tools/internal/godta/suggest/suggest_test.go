package suggest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

const tblT = 101

// workload is godta_t of Annex A, clustered on id, with one query, 7, whose
// plan (captured on the lab) reads it.
func workload(t *testing.T) *model.Workload {
	t.Helper()
	data, err := os.ReadFile("../showplan/testdata/sort.sqlplan")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := showplan.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var cols []model.Column
	for i, n := range []string{"id", "a", "b", "c", "pad"} {
		cols = append(cols, model.Column{ID: i + 1, Name: n, Type: "int"})
	}
	return &model.Workload{
		Database: model.DBInfo{Name: "godta_it_3c6x48yu", Collation: "SQL_Latin1_General_CP1_CI_AS"},
		Tables: map[int]*model.Table{
			tblT: {ObjectID: tblT, Schema: "dbo", Name: "godta_t", Columns: cols,
				Indexes: []model.Index{{ObjectID: tblT, IndexID: 1, Name: "pk_godta_t", Kind: model.KindClustered,
					Keys: []model.KeyCol{{ColumnID: 1}}, Unique: true, PrimaryKey: true}}},
		},
		Queries: []model.Query{{QueryID: 7, Plan: plan}},
	}
}

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "suggestions.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSuggestRejectsSQL(t *testing.T) {
	p := write(t, `{"suggestions": [{"table": "dbo.godta_t", "keys": [{"column": "a"}],
		"sql": "CREATE INDEX x ON dbo.godta_t(a); DROP TABLE dbo.godta_t", "reason": "r"}]}`)
	cands, _, err := Load(p, workload(t))
	if err == nil {
		t.Fatalf("a file with a sql field was accepted: %+v", cands)
	}
	if !strings.Contains(err.Error(), `"sql"`) {
		t.Errorf("error does not name the field: %v", err)
	}
}

func TestSuggestUnknownColumn(t *testing.T) {
	p := write(t, `{"suggestions": [
		{"table": "dbo.godta_t", "keys": [{"column": "a"}, {"column": "zz"}], "reason": "bad"},
		{"table": "dbo.godta_t", "keys": [{"column": "b"}], "reason": "good"}]}`)
	cands, rejected, err := Load(p, workload(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0], "suggestion 1") || !strings.Contains(rejected[0], `"zz"`) {
		t.Errorf("rejected = %q, want suggestion 1 rejected for column zz", rejected)
	}
	if len(cands) != 1 || cands[0].Reason != "good" {
		t.Errorf("candidates = %+v, want the second suggestion only", cands)
	}
}

func TestSuggestBecomesCandidate(t *testing.T) {
	p := write(t, `{"suggestions": [{"table": "dbo.godta_t",
		"keys": [{"column": "a"}, {"column": "b", "desc": true}], "include": ["c"],
		"reason": "seek on a, ordered by b descending"}]}`)
	cands, rejected, err := Load(p, workload(t))
	if err != nil || len(rejected) != 0 {
		t.Fatalf("err = %v, rejected = %q", err, rejected)
	}
	want := model.Candidate{
		Table:   tblT,
		Keys:    []model.KeyCol{{ColumnID: 2}, {ColumnID: 3, Desc: true}},
		Include: []int{4},
		Origin:  candidate.OriginAI,
		Reason:  "seek on a, ordered by b descending",
		Queries: []int64{7},
	}
	want.ID = candidate.ID(want)
	if len(cands) != 1 || !reflect.DeepEqual(cands[0], want) {
		t.Errorf("candidates = %+v\nwant %+v", cands, want)
	}
}

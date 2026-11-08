package collect

import (
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// planOf is a query whose plan reads the given objects, each "db.table".
func planOf(id int64, refs ...[2]string) model.Query {
	var st showplan.Statement
	for _, r := range refs {
		st.Accesses = append(st.Accesses, showplan.Access{Obj: showplan.ObjRef{Database: r[0], Schema: "dbo", Table: r[1]}, LookupOf: -1})
	}
	return model.Query{QueryID: id, Plan: &showplan.Plan{Statements: []showplan.Statement{st}}}
}

func TestPlanNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs [][2]string
		want []string
	}{
		// The case measured on pdfcopy_controle: the old name, tempdb and
		// the resource database, never the current name.
		{"single foreign name", [][2]string{{"PachaDataFormation", "t"}, {"tempdb", "#x"}, {"mssqlsystemresource", "sysrowsets"}, {"pachadataformation", "u"}}, []string{"PachaDataFormation"}},
		{"several foreign names", [][2]string{{"PachaDataFormation", "t"}, {"OtherDB", "u"}}, nil},
		{"current name present", [][2]string{{"PachaDataFormation", "t"}, {"COPY", "u"}}, nil},
		// The usual database: its plans name it by its current name, which
		// is no alias of itself.
		{"current name only", [][2]string{{"COPY", "u"}, {"tempdb", "#x"}}, nil},
		{"only system databases", [][2]string{{"tempdb", "#x"}, {"master", "spt_values"}}, nil},
	} {
		w := &model.Workload{Database: model.DBInfo{Name: "copy"}}
		w.Queries = []model.Query{planOf(1, tc.refs...)}
		if got := planNames(w); !slices.Equal(got, tc.want) {
			t.Errorf("%s: planNames = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPlanTablesReadsPlanName: the tables collect resolves include those a
// plan names under the database's PlanNames, and still leave out another
// database.
func TestPlanTablesReadsPlanName(t *testing.T) {
	w := &model.Workload{Database: model.DBInfo{Name: "copy", PlanNames: []string{"Prod"}}}
	w.Queries = []model.Query{planOf(1, [2]string{"prod", "t"}, [2]string{"Other", "u"}, [2]string{"COPY", "v"})}
	got := planTables(w)
	want := map[tableName]bool{{"dbo", "t"}: true, {"dbo", "v"}: true}
	if len(got) != len(want) || !got[tableName{"dbo", "t"}] || !got[tableName{"dbo", "v"}] {
		t.Errorf("planTables = %v, want %v", got, want)
	}
}

func TestWarnNoTable(t *testing.T) {
	w := &model.Workload{Queries: []model.Query{{QueryID: 1}}, Tables: map[int]*model.Table{}}
	warnNoTable(w)
	if !slices.Contains(w.Warnings, WarnNoTable) {
		t.Errorf("warnings %v, want %q", w.Warnings, WarnNoTable)
	}
	w = &model.Workload{Queries: []model.Query{{QueryID: 1}}, Tables: map[int]*model.Table{1: {ObjectID: 1}}}
	warnNoTable(w)
	if len(w.Warnings) != 0 {
		t.Errorf("warnings %v with a table resolved, want none", w.Warnings)
	}
}

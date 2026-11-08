package collect

import (
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// planOf is a query whose plan reads the given objects, each "db.table" in
// schema dbo.
func planOf(id int64, refs ...[2]string) model.Query {
	var objs []showplan.ObjRef
	for _, r := range refs {
		objs = append(objs, showplan.ObjRef{Database: r[0], Schema: "dbo", Table: r[1]})
	}
	return planOfObjs(id, objs...)
}

// planOfObjs is a query whose plan reads the given objects.
func planOfObjs(id int64, objs ...showplan.ObjRef) model.Query {
	var st showplan.Statement
	for _, o := range objs {
		st.Accesses = append(st.Accesses, showplan.Access{Obj: o, LookupOf: -1})
	}
	return model.Query{QueryID: id, Plan: &showplan.Plan{Statements: []showplan.Statement{st}}}
}

func TestPlanNames(t *testing.T) {
	// here is the catalog of the current database, copy.
	here := map[tableName]int{{"dbo", "t"}: 1, {"dbo", "u"}: 2}
	sysRef := func(db, table string) showplan.ObjRef {
		return showplan.ObjRef{Database: db, Schema: "sys", Table: table}
	}
	for _, tc := range []struct {
		name    string
		queries []model.Query
		want    []string
		warning string
	}{
		// The case measured on pdfcopy_controle: the old name, tempdb and
		// the resource database, never the current name.
		{"single foreign name", []model.Query{planOfObjs(1,
			showplan.ObjRef{Database: "PachaDataFormation", Schema: "dbo", Table: "t"},
			showplan.ObjRef{Database: "tempdb", Schema: "dbo", Table: "#x"},
			sysRef("mssqlsystemresource", "sysrowsets"),
			showplan.ObjRef{Database: "pachadataformation", Schema: "dbo", Table: "u"})},
			[]string{"PachaDataFormation"}, WarnPlanName("PachaDataFormation", 1)},
		// A renamed database that keeps running: old plans under the old
		// name, new ones under the new name.
		{"old and new names", []model.Query{planOf(1, [2]string{"Prod", "t"}), planOf(2, [2]string{"COPY", "u"}),
			planOf(3, [2]string{"Prod", "u"}, [2]string{"tempdb", "#x"})},
			[]string{"Prod"}, WarnPlanName("Prod", 2)},
		// goDTA's own catalog reads compiled under the current name: sys
		// and INFORMATION_SCHEMA objects only, which do not count.
		{"current name on system objects", []model.Query{planOf(1, [2]string{"Prod", "t"}),
			planOfObjs(2, sysRef("COPY", "sysidxstats"), showplan.ObjRef{Database: "copy", Schema: "INFORMATION_SCHEMA", Table: "TABLES"})},
			[]string{"Prod"}, WarnPlanName("Prod", 1)},
		{"two foreign names", []model.Query{planOf(1, [2]string{"Prod", "t"}), planOf(2, [2]string{"Other", "u"})},
			nil, WarnPlanNamesSeveral([]string{"Prod", "Other"})},
		{"foreign table missing here", []model.Query{planOf(1, [2]string{"Prod", "t"}, [2]string{"Prod", "gone"})},
			nil, WarnPlanNameMissing("Prod", []string{"[dbo].[gone]"})},
		// A foreign name on system objects alone is no user table: nothing
		// to read through it, nothing to say.
		{"foreign name on system objects", []model.Query{planOfObjs(1, sysRef("Prod", "objects")), planOf(2, [2]string{"copy", "t"})},
			nil, ""},
		// The usual database: its plans name it by its current name, which
		// is no alias of itself.
		{"current name only", []model.Query{planOf(1, [2]string{"COPY", "u"}, [2]string{"tempdb", "#x"})}, nil, ""},
		{"only system databases", []model.Query{planOf(1, [2]string{"tempdb", "#x"}, [2]string{"master", "spt_values"})}, nil, ""},
	} {
		w := &model.Workload{Database: model.DBInfo{Name: "copy"}, Queries: tc.queries}
		got, warning := planNames(w, here)
		if !slices.Equal(got, tc.want) || warning != tc.warning {
			t.Errorf("%s: planNames = %v, %q, want %v, %q", tc.name, got, warning, tc.want, tc.warning)
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

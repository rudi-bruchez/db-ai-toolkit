package sqlferret

import (
	"slices"
	"strings"
	"testing"
)

const project = "testdata/project"

// The fixture follows sqlferret's docs/execution-plans.md: run_1 holds the
// godta_h seek plan (first and worst occurrence), run_2 the godta_t sort plan
// and the same godta_h plan again, all written as UTF-16 LE with a BOM.
func files(srcs []Source) []string {
	var out []string
	for _, s := range srcs {
		out = append(out, s.File)
	}
	return out
}

func TestSqlferretAllRuns(t *testing.T) {
	srcs, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"plans/profile/run_1/p_528646337168E8A7.sqlplan",
		"plans/profile/run_2/p_9C8D053AFE85AF1C.sqlplan",
	}
	if got := files(srcs); !slices.Equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for _, s := range srcs {
		if s.Database != "godta_it_3c6x48yu" {
			t.Errorf("%s: database = %q", s.File, s.Database)
		}
		if s.Plan == nil || len(s.Plan.Statements) == 0 {
			t.Errorf("%s: no statement read", s.File)
		}
	}
}

func TestSqlferretSkipsQueryStoreImport(t *testing.T) {
	srcs, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files(srcs) {
		if !strings.HasPrefix(f, "plans/profile/run_") {
			t.Errorf("read %s, outside every run", f)
		}
	}
}

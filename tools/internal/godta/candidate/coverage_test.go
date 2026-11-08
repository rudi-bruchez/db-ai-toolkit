package candidate

import (
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
)

// The tables of the truth table. godta_t is (id, a, b, c, pad, d) clustered
// on id, as in workload(); the heap is (x, y, z).
const (
	colX = iota + 1
	colY
	colZ
)

func tableT(extra ...model.Index) *model.Table {
	t := *workload().Tables[tblT]
	t.Indexes = append(slices.Clone(t.Indexes), extra...)
	return &t
}

func tableHeap(extra ...model.Index) *model.Table {
	return &model.Table{ObjectID: tblH, Schema: "dbo", Name: "godta_h", IsHeap: true,
		Columns: []model.Column{{ID: colX, Name: "x"}, {ID: colY, Name: "y"}, {ID: colZ, Name: "z"}},
		Indexes: append([]model.Index{{ObjectID: tblH, IndexID: 0, Kind: model.KindHeap}}, extra...)}
}

// nc is a non unique nonclustered index with index_id 2 and above, in the
// order given.
func nc(id int, name string, k []model.KeyCol, include ...int) model.Index {
	return model.Index{IndexID: id, Name: name, Kind: model.KindNonclustered, Keys: k, Include: include}
}

func cand(table int, eq int, k []model.KeyCol, include ...int) model.Candidate {
	return model.Candidate{Table: table, Keys: k, EqCount: eq, Include: include}
}

type probe struct {
	c        model.Candidate
	want     model.Decision
	existing string
	include  []int // checked when not nil
}

func TestCoverageTruthTable(t *testing.T) {
	disabled := nc(2, "nix_a", keys(asc(colA)), colC)
	disabled.Disabled = true
	filtered := nc(2, "nix_a", keys(asc(colA)))
	filtered.Filter = "([b]=(1))"
	unique := nc(2, "ux_a", keys(asc(colA)))
	unique.Unique = true
	uq := nc(2, "uq_a", keys(asc(colA)))
	uq.Unique, uq.UniqueConstraint = true, true
	named := nc(2, "nix_h_x", keys(asc(colX)), colZ)
	named.NamedIn = []string{"query_store_text"}
	clusteredOnA := &model.Table{ObjectID: tblT, Schema: "dbo", Name: "godta_c",
		Columns: []model.Column{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}},
		Indexes: []model.Index{{IndexID: 1, Name: "cx_a", Kind: model.KindClustered, Keys: keys(asc(1))}}}
	caseTable := &model.Table{ObjectID: tblH, Schema: "dbo", Name: "godta_case", IsHeap: true,
		Columns: []model.Column{{ID: 1, Name: "a"}, {ID: 2, Name: "A"}},
		Indexes: []model.Index{{IndexID: 0, Kind: model.KindHeap}, nc(2, "nix_a", keys(asc(1)))}}

	rows := []struct {
		name   string
		table  *model.Table
		probes []probe
	}{
		{"01_same_keys_and_includes", tableT(nc(2, "nix_ab", keys(asc(colA), asc(colB)), colC)),
			[]probe{{c: cand(tblT, 2, keys(asc(colA), asc(colB)), colC), want: model.DecisionCovered, existing: "nix_ab"}}},
		{"02_equality_permutation", tableT(nc(2, "nix_ba", keys(asc(colB), asc(colA)))),
			[]probe{{c: cand(tblT, 2, keys(asc(colA), asc(colB))), want: model.DecisionCovered, existing: "nix_ba"}}},
		{"03_equality_direction", tableT(nc(2, "nix_a", keys(desc(colA)))),
			[]probe{{c: cand(tblT, 1, keys(asc(colA))), want: model.DecisionCovered, existing: "nix_a"}}},
		{"04a_single_sort_key_inverted", tableT(nc(2, "nix_ab", keys(asc(colA), desc(colB)))),
			[]probe{{c: cand(tblT, 1, keys(asc(colA), asc(colB))), want: model.DecisionCovered, existing: "nix_ab"}}},
		{"04b_all_sort_keys_inverted", tableT(nc(2, "nix_ab", keys(desc(colA), asc(colB)))),
			[]probe{{c: cand(tblT, 0, keys(asc(colA), desc(colB))), want: model.DecisionCovered, existing: "nix_ab"}}},
		{"04c_one_of_two_sort_keys_inverted", tableT(nc(2, "nix_ab", keys(asc(colA), asc(colB)))),
			[]probe{{c: cand(tblT, 0, keys(asc(colA), desc(colB))), want: model.DecisionNew}}},
		{"05_clustered_key_implicit", tableT(nc(2, "nix_a", keys(asc(colA)))),
			[]probe{{c: cand(tblT, 1, keys(asc(colA), asc(colID))), want: model.DecisionCovered, existing: "nix_a"}}},
		{"06_unique_has_no_implicit_key", tableT(unique),
			[]probe{{c: cand(tblT, 1, keys(asc(colA), asc(colID))), want: model.DecisionNew}}},
		{"07_disabled", tableT(disabled),
			[]probe{{c: cand(tblT, 1, keys(asc(colA)), colC), want: model.DecisionNew}}},
		{"08_filtered", tableT(filtered),
			[]probe{{c: cand(tblT, 1, keys(asc(colA))), want: model.DecisionNew}}},
		{"09_widen", tableT(nc(2, "nix_a", keys(asc(colA)), colC, colPad)),
			[]probe{{c: cand(tblT, 1, keys(asc(colA)), colC, colD), want: model.DecisionWiden, existing: "nix_a", include: []int{colC, colPad, colD}}}},
		{"10_constraint_discuss", tableT(uq),
			[]probe{{c: cand(tblT, 1, keys(asc(colA)), colC, colD), want: model.DecisionDiscuss, existing: "uq_a"}}},
		{"11a_heap_replace", tableHeap(nc(2, "nix_h_x", keys(asc(colX)), colZ)),
			[]probe{{c: cand(tblH, 1, keys(asc(colX), asc(colY))), want: model.DecisionReplace, existing: "nix_h_x", include: []int{colZ}}}},
		{"11b_clustered_table_new", tableT(nc(2, "nix_a", keys(asc(colA)))),
			[]probe{{c: cand(tblT, 1, keys(asc(colA), asc(colB))), want: model.DecisionNew}}},
		{"12_named_in_hint", tableHeap(named),
			[]probe{{c: cand(tblH, 1, keys(asc(colX), asc(colY))), want: model.DecisionNew}}},
		{"13_clustered_covers_never_target", clusteredOnA, []probe{
			{c: cand(tblT, 1, keys(asc(1)), 2), want: model.DecisionCovered, existing: "cx_a"},
			{c: cand(tblT, 1, keys(asc(1), asc(2))), want: model.DecisionNew},
		}},
		{"14_no_intersection", tableT(nc(2, "nix_a", keys(asc(colA))), nc(3, "nix_b", keys(asc(colB)))),
			[]probe{{c: cand(tblT, 2, keys(asc(colA), asc(colB))), want: model.DecisionNew}}},
		{"15_column_id_not_name", caseTable,
			[]probe{{c: cand(tblH, 1, keys(asc(2))), want: model.DecisionNew}}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			w := &model.Workload{Database: model.DBInfo{Name: dbName, Collation: "SQL_Latin1_General_CP1_CI_AS"},
				Tables: map[int]*model.Table{row.table.ObjectID: row.table}}
			for _, p := range row.probes {
				out := ConfrontAll([]model.Candidate{p.c}, w)
				if len(out) != 1 {
					t.Fatalf("%v: %d candidates out, want 1", p.c.Keys, len(out))
				}
				got := out[0]
				if got.Decision != p.want || got.Existing != p.existing {
					_, _, why := Confront(p.c, row.table)
					t.Errorf("%v include %v: got %q %q, want %q %q (%v)", p.c.Keys, p.c.Include,
						got.Decision, got.Existing, p.want, p.existing, why)
				}
				if p.include != nil && !slices.Equal(got.Include, p.include) {
					t.Errorf("%v: include %v, want %v", p.c.Keys, got.Include, p.include)
				}
			}
		})
	}
}

func TestConfrontAllAddsReplacedQueries(t *testing.T) {
	w := workload()
	w.Queries = []model.Query{
		{QueryID: 1, Plan: loadPlan(t, "seek_lookup.sqlplan")}, // reads nix_h_x
		{QueryID: 2, Plan: loadPlan(t, "sort.sqlplan")},        // reads pk_godta_t only
	}
	c := cand(tblH, 1, keys(asc(1)), 2)
	c.Queries = []int64{7}
	out := ConfrontAll([]model.Candidate{c}, w)
	if out[0].Decision != model.DecisionWiden || out[0].Existing != "nix_h_x" {
		t.Fatalf("got %q %q, want widen of nix_h_x", out[0].Decision, out[0].Existing)
	}
	if !slices.Equal(out[0].Queries, []int64{1, 7}) {
		t.Errorf("queries %v, want [1 7]", out[0].Queries)
	}
	if !slices.Equal(c.Queries, []int64{7}) {
		t.Errorf("input changed: %v", c.Queries)
	}
}
